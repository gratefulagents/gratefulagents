package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

func toolCallItem(name string, input any) agent.RunItem {
	raw, _ := json.Marshal(input)
	return agent.RunItem{Type: agent.RunItemToolCall, ToolCall: &agent.ToolCallData{Name: name, Input: raw}}
}

func TestCollectTurnMemoryActivity(t *testing.T) {
	items := []agent.RunItem{
		{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "hi"}},
		toolCallItem("Bash", map[string]string{"command": "ls"}),
		toolCallItem("memory_save", map[string]string{"title": "Postgres admin access"}),
		toolCallItem("memory_save", map[string]string{"id": "mem_1", "title": "Updated"}),
		toolCallItem("memory_save", map[string]string{"id": "mem_2"}),
		toolCallItem("memory_delete", map[string]string{"id": "mem_3"}),
	}
	got := collectTurnMemoryActivity(items)
	if got.toolCalls != 5 {
		t.Fatalf("toolCalls = %d, want 5", got.toolCalls)
	}
	if strings.Join(got.savedTitles, "|") != "Postgres admin access|mem_1 Updated|mem_2" {
		t.Fatalf("savedTitles = %q", got.savedTitles)
	}
	if strings.Join(got.deletedIDs, ",") != "mem_3" {
		t.Fatalf("deletedIDs = %q", got.deletedIDs)
	}
}

func TestShouldConsolidateMemory(t *testing.T) {
	if !shouldConsolidateMemory(turnMemoryActivity{savedTitles: []string{"x"}}, "") {
		t.Error("a turn that saved memories must be consolidated")
	}
	if shouldConsolidateMemory(turnMemoryActivity{toolCalls: memoryConsolidationMinToolCalls - 1}, "done") {
		t.Error("trivial turns must be skipped")
	}
	if shouldConsolidateMemory(turnMemoryActivity{toolCalls: memoryConsolidationMinToolCalls}, "  ") {
		t.Error("turns without an outcome must be skipped")
	}
	if !shouldConsolidateMemory(turnMemoryActivity{toolCalls: memoryConsolidationMinToolCalls}, "done") {
		t.Error("substantial turns with an outcome must be consolidated")
	}
}

func TestBuildMemoryConsolidationPromptBoundsInput(t *testing.T) {
	activity := turnMemoryActivity{toolCalls: 7, savedTitles: []string{"t1"}, deletedIDs: []string{"mem_9"}}
	prompt := buildMemoryConsolidationPrompt(strings.Repeat("r", 10000), strings.Repeat("o", 10000), activity)
	for _, want := range []string{
		"<user_request>", "<turn_outcome>", "Tool calls this turn: 7", "- t1", "Memories deleted this turn: mem_9",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if len([]rune(prompt)) > 8600 {
		t.Fatalf("prompt length %d exceeds the truncation bounds", len([]rune(prompt)))
	}
}

func TestMemoryConsolidatorInstructionsReferenceRealTools(t *testing.T) {
	for name := range memoryConsolidatorTools {
		if !strings.HasPrefix(name, "memory_") {
			t.Fatalf("consolidator tool %q is not a memory tool", name)
		}
	}
	for _, want := range []string{"memory_search", "memory_save", "memory_delete", "NOOP"} {
		if !strings.Contains(memoryConsolidatorInstructions, want) {
			t.Errorf("consolidator instructions missing %q", want)
		}
	}
}

func TestReleaseClaimsOnExitSkipsShutdownAndNilStore(_ *testing.T) {
	// Nil store and cancelled (pod shutdown) contexts must be no-ops; neither
	// may panic or block.
	releaseClaimsOnExit(context.Background(), nil, "run", runResult{Status: "succeeded"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	releaseClaimsOnExit(ctx, nil, "run", runResult{Status: "failed"})
}

func TestNewMemoryConsolidatorAgentDropsBaseInstructions(t *testing.T) {
	base := &agent.Agent{
		Name:           "main",
		Model:          "base-model",
		FallbackModels: []string{"fallback"},
		InstructionsFn: func(*agent.RunContext, *agent.Agent) string { return "main agent system prompt" },
		Handoffs:       []*agent.Handoff{{}},
		OutputType:     &agent.OutputSchema{},
	}
	got := newMemoryConsolidatorAgent(base, "consolidation-model", nil)
	if instructions := got.GetInstructions(nil); instructions != memoryConsolidatorInstructions {
		t.Fatalf("consolidator instructions = %q, want the consolidator prompt", instructions)
	}
	if got.Model != "consolidation-model" || len(got.Handoffs) != 0 || got.OutputType != nil {
		t.Fatalf("consolidator inherited base agent behavior: %+v", got)
	}
	if len(got.FallbackModels) != 1 || &got.FallbackModels[0] == &base.FallbackModels[0] {
		t.Fatal("fallback models must be copied, not shared")
	}
}
