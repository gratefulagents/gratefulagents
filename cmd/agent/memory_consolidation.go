package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/agentinfra"
	opprojectstate "github.com/gratefulagents/gratefulagents/internal/projectstate"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkruntime "github.com/gratefulagents/sdk/pkg/agentsdk/runtime"
	sdkprojectstatetools "github.com/gratefulagents/sdk/pkg/agentsdk/tools/projectstate"
)

const (
	// memoryConsolidationTimeout bounds the post-turn consolidation pass so a
	// slow provider can never hold the next turn hostage.
	memoryConsolidationTimeout = 90 * time.Second
	// memoryConsolidationMaxTurns bounds the consolidator's tool loop.
	memoryConsolidationMaxTurns = 8
	// memoryConsolidationMinToolCalls skips trivial turns (chit-chat, one-off
	// lookups) that rarely produce durable knowledge.
	memoryConsolidationMinToolCalls = 4
)

// memoryConsolidatorTools are the only tools the consolidator receives.
var memoryConsolidatorTools = map[string]bool{
	"memory_search": true,
	"memory_get":    true,
	"memory_save":   true,
	"memory_verify": true,
	"memory_delete": true,
}

const memoryConsolidatorInstructions = `You are the memory consolidator for a software project's durable agent memory.
You review one completed agent turn and keep the project memory small, correct, and useful for future runs.

Memory kinds: preference (how the user/owner wants work done, including corrections), decision (a durable
product or architecture decision with its rationale), fact (a non-obvious fact or gotcha about the project),
procedure (a repeatable how-to).

Do, in order:
1. For every memory the turn saved, memory_search for overlapping entries. Merge true duplicates into the best
   entry with memory_save (pass its id), then memory_delete the redundant ones. Rewrite a just-saved memory
   that reads like a progress log or PR changelog into reusable knowledge, or delete it.
2. If the turn revealed knowledge that is durable, reusable, and not obvious from the code (most importantly:
   user preferences or corrections stated in the request, decisions and their rationale, gotchas that cost the
   agent time), check with memory_search that it is not already stored, then save at most 3 new memories. Each
   needs a specific title, a body under 1,500 characters, and citations (repository-relative file paths or
   URLs) when possible.
3. If the turn's outcome contradicts an existing memory, correct it (memory_save with its id) or delete it.

Never save: progress reports, PR numbers/status, test results, CI status, run summaries, or anything already
obvious from reading the repository. Most turns need no change at all; that is the expected outcome.
When done, reply with one line: "NOOP" or a terse list of the changes you made.`

// memoryConsolidationEnabled gates the post-turn consolidator. On by default;
// opt out with ENABLE_MEMORY_CONSOLIDATION=false.
func memoryConsolidationEnabled() bool {
	return envFlagEnabled("ENABLE_MEMORY_CONSOLIDATION", true)
}

// turnMemoryActivity summarizes the tool calls of a turn for the consolidation
// trigger and prompt.
type turnMemoryActivity struct {
	toolCalls   int
	savedTitles []string
	deletedIDs  []string
}

func collectTurnMemoryActivity(items []agent.RunItem) turnMemoryActivity {
	var out turnMemoryActivity
	for _, item := range items {
		if item.Type != agent.RunItemToolCall || item.ToolCall == nil {
			continue
		}
		out.toolCalls++
		switch item.ToolCall.Name {
		case "memory_save":
			var in struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			}
			if json.Unmarshal(item.ToolCall.Input, &in) == nil {
				label := strings.TrimSpace(in.Title)
				if id := strings.TrimSpace(in.ID); id != "" {
					label = strings.TrimSpace(id + " " + label)
				}
				if label != "" {
					out.savedTitles = append(out.savedTitles, label)
				}
			}
		case "memory_delete":
			var in struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(item.ToolCall.Input, &in) == nil && strings.TrimSpace(in.ID) != "" {
				out.deletedIDs = append(out.deletedIDs, strings.TrimSpace(in.ID))
			}
		}
	}
	return out
}

// shouldConsolidateMemory decides whether a committed turn is worth a
// consolidation pass: the turn saved memories (dedupe them), or it did real
// work and produced an answer (extract durable lessons).
func shouldConsolidateMemory(activity turnMemoryActivity, finalText string) bool {
	if len(activity.savedTitles) > 0 {
		return true
	}
	return activity.toolCalls >= memoryConsolidationMinToolCalls && strings.TrimSpace(finalText) != ""
}

func buildMemoryConsolidationPrompt(request, finalText string, activity turnMemoryActivity) string {
	var b strings.Builder
	b.WriteString("<user_request>\n")
	b.WriteString(truncateRunes(strings.TrimSpace(request), 3000))
	b.WriteString("\n</user_request>\n\n<turn_outcome>\n")
	b.WriteString(truncateRunes(strings.TrimSpace(finalText), 5000))
	b.WriteString("\n</turn_outcome>\n\n")
	fmt.Fprintf(&b, "Tool calls this turn: %d\n", activity.toolCalls)
	if len(activity.savedTitles) > 0 {
		b.WriteString("Memories saved this turn:\n")
		for _, title := range activity.savedTitles {
			b.WriteString("- " + truncateRunes(title, 200) + "\n")
		}
	} else {
		b.WriteString("Memories saved this turn: none\n")
	}
	if len(activity.deletedIDs) > 0 {
		b.WriteString("Memories deleted this turn: " + strings.Join(activity.deletedIDs, ", ") + "\n")
	}
	b.WriteString("\nConsolidate the project memory for this turn.")
	return b.String()
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// consolidateMemoryAfterTurn runs a bounded, memory-tools-only agent over the
// committed turn (Mem0/Letta "sleep-time" consolidation): it merges and
// prunes what the turn saved and extracts at most a few durable lessons.
// Best-effort: failures are logged and never affect the run.
func (r *chatRuntime) consolidateMemoryAfterTurn(
	ctx context.Context, t *userTurn, result *agent.RunResult, toolAccess agent.ToolAccessLevel,
) {
	if r.psStore == nil || r.runner == nil || r.baseAgent == nil || result == nil || t == nil {
		return
	}
	if !memoryConsolidationEnabled() || r.cfg.DelegatedChild || toolAccess == agent.ToolAccessLevelReadOnly {
		return
	}
	finalText := strings.TrimSpace(result.FinalText())
	if finalText == "" && r.finishSummary != nil {
		finalText = strings.TrimSpace(r.finishSummary.Summary())
	}
	activity := collectTurnMemoryActivity(result.NewItems)
	if !shouldConsolidateMemory(activity, finalText) {
		return
	}
	request := strings.TrimSpace(t.reply)
	if request == "" {
		request = t.prompt
	}
	tools := memoryConsolidationTools(r.psStore, r.cfg.TaskName, r.cfg.RepoDir)
	if len(tools) == 0 {
		return
	}

	// Derived from the turn context so a pod shutdown cancels consolidation
	// instead of eating the termination grace period.
	consolidateCtx, cancel := context.WithTimeout(ctx, memoryConsolidationTimeout)
	defer cancel()
	liveRun := getAgentRun(consolidateCtx, r.crd, r.cfg.TaskName, r.cfg.Namespace)
	model, provider := liveRuntimeModelAndProvider(r.cfg, liveRun)
	if override := strings.TrimSpace(agentinfra.EnvOrDefault("MEMORY_CONSOLIDATION_MODEL", "")); override != "" {
		model = override
	}
	consolidator := newMemoryConsolidatorAgent(r.baseAgent, model, tools)
	runCfg := sdkruntime.BuildRunConfig(sdkruntime.Config{
		Provider:         provider,
		Model:            model,
		WorkDir:          r.cfg.RepoDir,
		MaxTurns:         memoryConsolidationMaxTurns,
		ToolAccess:       agent.ToolAccessLevelFull,
		TracingProcessor: r.tp,
		Trace:            r.runTrace,
		ParentSpanID:     traceID(r.runTrace),
		Features: &sdkruntime.Features{
			Runtime: sdkruntime.RuntimeFeatures{
				Retry:   true,
				Tracing: r.tp != nil,
			},
		},
	}, nil)
	items := []agent.RunItem{{
		Type:    agent.RunItemMessage,
		Message: &agent.MessageOutput{Text: buildMemoryConsolidationPrompt(request, finalText, activity)},
	}}
	started := time.Now()
	res, err := r.runner.Run(consolidateCtx, consolidator, items, runCfg)
	if err != nil {
		log.Printf("WARN: memory consolidation failed after %s: %v", time.Since(started).Round(time.Second), err)
		return
	}
	outcome := strings.TrimSpace(res.FinalText())
	elapsed := time.Since(started).Round(time.Second)
	log.Printf("Memory consolidation finished in %s: %s", elapsed, truncateRunes(outcome, 300))
	if outcome != "" && !strings.EqualFold(outcome, "NOOP") && r.sc != nil {
		summary := "Memory consolidated: " + truncateRunes(outcome, 500)
		_ = r.sc.WriteActivity(consolidateCtx, "memory_consolidation", summary, nil)
	}
}

// newMemoryConsolidatorAgent builds the consolidator from the base agent's
// model settings only. It must not inherit InstructionsFn (the main agent's
// dynamic system prompt, which takes precedence over Instructions), handoffs,
// guardrails, structured output, or stop/final-output tool behavior.
func newMemoryConsolidatorAgent(base *agent.Agent, model string, tools []agent.Tool) *agent.Agent {
	return &agent.Agent{
		Name:           "memory-consolidator",
		Instructions:   memoryConsolidatorInstructions,
		Model:          model,
		FallbackModels: append([]string(nil), base.FallbackModels...),
		ModelSettings:  base.ModelSettings,
		Tools:          tools,
	}
}

// memoryConsolidationTools returns the SDK memory tools bound to the run's
// store, actor and workspace (for citation staleness and commit stamping).
func memoryConsolidationTools(store *opprojectstate.Store, actor, workDir string) []agent.Tool {
	if store == nil {
		return nil
	}
	var out []agent.Tool
	for _, tool := range sdkprojectstatetools.Tools(store, actor, sdkprojectstatetools.WithWorkDir(workDir)) {
		if memoryConsolidatorTools[tool.Name()] {
			out = append(out, tool)
		}
	}
	return out
}

func traceID(trace *agent.Trace) string {
	if trace == nil {
		return ""
	}
	return trace.ID
}
