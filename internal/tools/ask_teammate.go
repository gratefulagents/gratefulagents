package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

// maxTeammateMaterialLen bounds the material (plan/diff/etc.) forwarded to a
// teammate persona so a runaway paste cannot blow up the persona prompt.
const maxTeammateMaterialLen = 24 * 1024

// SoulResolver resolves a teammate handle (e.g. "alice", "agent_alice", "@alice")
// to that user's saved SOUL persona. found is false when there is no such user
// or they have not written a SOUL yet (a non-error condition the tool reports
// back to the model). canonicalName is the resolved username used for framing.
type SoulResolver interface {
	ResolveSoul(ctx context.Context, name string) (canonicalName, content string, found bool, err error)
}

// PersonaRunner runs a one-shot, tool-less persona turn: soul is injected as the
// persona's instructions and prompt is the consult message. It returns the
// persona's reply text. Wired in by the agent runtime, which owns the model
// provider and runner.
type PersonaRunner func(ctx context.Context, soul, prompt string) (string, error)

// RegisterAskTeammateTool registers the ask_teammate tool, which lets the agent
// consult a teammate's persona (their SOUL) for that teammate's likely
// perspective on a question, approach, plan, or diff. The persona runner is
// injected later via SetPersonaRunner once the runtime (and its model provider)
// is built. Returns the tool so the caller can wire the runner; returns nil when
// no resolver is provided.
func RegisterAskTeammateTool(registry *Registry, resolver SoulResolver) *AskTeammateTool {
	if registry == nil || resolver == nil {
		return nil
	}
	t := &AskTeammateTool{resolver: resolver}
	registry.Register(t)
	return t
}

// AskTeammateTool consults a teammate's SOUL persona on demand.
type AskTeammateTool struct {
	resolver SoulResolver

	mu      sync.RWMutex
	persona PersonaRunner
}

// SetPersonaRunner wires the function that executes the persona turn. Until it
// is set, the tool reports that teammate consultation is unavailable.
func (t *AskTeammateTool) SetPersonaRunner(fn PersonaRunner) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.persona = fn
}

func (t *AskTeammateTool) personaRunner() PersonaRunner {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.persona
}

type askTeammateInput struct {
	Name     string `json:"name"`
	Question string `json:"question"`
	Material string `json:"material"`
}

func (t *AskTeammateTool) Name() string { return "ask_teammate" }

func (t *AskTeammateTool) Description() string {
	return "Consult a teammate's persona (their personal SOUL) to get how that " +
		"specific colleague would likely react. Use this when the user wants a " +
		"teammate's perspective — e.g. \"ask agent_alice what she'd think of this\" " +
		"or \"what would Bob say about this plan?\". Works for a question, an " +
		"approach/decision, a whole plan, or a diff. Put the colleague's handle in " +
		"`name` (with or without an `agent_`/`@` prefix), the ask in `question`, and " +
		"the artifact under review (plan, design, or diff) in `material`. Returns " +
		"that teammate's perspective in their own voice; it is advisory, not an action."
}

func (t *AskTeammateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "The teammate's handle/username to consult (e.g. \"alice\", \"agent_alice\", or \"@alice\")."
			},
			"question": {
				"type": "string",
				"description": "What you want the teammate's perspective on (e.g. \"Would you approve this plan?\", \"Any concerns with this approach?\")."
			},
			"material": {
				"type": "string",
				"description": "Optional artifact under review for the teammate to react to: a plan, design, approach, or diff snippet."
			}
		},
		"required": ["name", "question"]
	}`)
}

func (t *AskTeammateTool) IsReadOnly() bool                      { return true }
func (t *AskTeammateTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *AskTeammateTool) NeedsApproval() bool                   { return false }
func (t *AskTeammateTool) TimeoutSeconds() int                   { return 120 }

func (t *AskTeammateTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in askTeammateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	name := normalizeTeammateName(in.Name)
	question := strings.TrimSpace(in.Question)
	material := strings.TrimSpace(in.Material)
	if name == "" {
		return Result{Content: "name is required (the teammate to consult)", IsError: true}, nil
	}
	if question == "" {
		return Result{Content: "question is required (what to ask the teammate)", IsError: true}, nil
	}
	if len(material) > maxTeammateMaterialLen {
		material = truncateRunes(material, maxTeammateMaterialLen) + "\n\n[...truncated...]"
	}

	run := t.personaRunner()
	if run == nil {
		return Result{Content: "teammate consultation is unavailable in this run", IsError: true}, nil
	}

	canonical, soul, found, err := t.resolver.ResolveSoul(ctx, name)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to look up teammate %q: %v", name, err), IsError: true}, nil
	}
	if !found {
		return Result{Content: fmt.Sprintf(
			"No teammate persona found for %q. They may not exist or may not have written a SOUL yet. "+
				"Ask the user to confirm the teammate's handle.", name)}, nil
	}
	soul = strings.TrimSpace(soul)
	if soul == "" {
		return Result{Content: fmt.Sprintf("Teammate %q has not written a SOUL yet, so there is no persona to consult.", canonical)}, nil
	}
	if canonical == "" {
		canonical = name
	}

	reply, err := run(ctx, soul, buildTeammatePrompt(canonical, question, material))
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to consult teammate %q: %v", canonical, err), IsError: true}, nil
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		reply = "(the teammate persona returned no response)"
	}
	return Result{Content: fmt.Sprintf("%s's perspective:\n\n%s", canonical, reply)}, nil
}

// normalizeTeammateName strips an optional agent_/@ handle prefix and trims the
// teammate name so "agent_alice", "@alice", and "alice" all resolve alike.
func normalizeTeammateName(raw string) string {
	name := strings.TrimSpace(raw)
	name = strings.TrimPrefix(name, "@")
	for _, prefix := range []string{"agent_", "agent-"} {
		if len(name) > len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
			name = name[len(prefix):]
			break
		}
	}
	return strings.TrimSpace(name)
}

// truncateRunes returns s limited to at most maxBytes bytes without splitting a
// UTF-8 sequence, so the forwarded material never becomes invalid UTF-8.
func truncateRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// buildTeammatePrompt frames the consult message sent to the teammate persona.
func buildTeammatePrompt(name, question, material string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A colleague's coding agent is consulting you, %s, for your perspective. "+
		"Answer in the first person, in your own voice, as the persona described in your instructions.\n\n", name)
	b.WriteString("Their question:\n")
	b.WriteString(question)
	if material != "" {
		b.WriteString("\n\nMaterial they want your take on:\n")
		b.WriteString(material)
	}
	return b.String()
}
