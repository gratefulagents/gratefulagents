package dashboard

import (
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

func TestAggregateUsageFromEntriesSplitsTopLevelAndSubagentTasks(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{
			Type:                   "llm_attempt",
			TimestampUnix:          100,
			Phase:                  "planning",
			Step:                   "exploring",
			AgentName:              "planner",
			LlmAttemptId:           "top-1",
			LlmAttemptModel:        "claude",
			LlmAttemptProvider:     "anthropic",
			LlmAttemptInputTokens:  10,
			LlmAttemptOutputTokens: 5,
			LlmAttemptTokensKnown:  true,
		},
		{
			Type:                   "llm_attempt",
			TimestampUnix:          200,
			Phase:                  "planning",
			Step:                   "reviewing",
			TaskId:                 "task-1",
			AgentName:              "reviewer",
			LlmAttemptId:           "sub-1",
			LlmAttemptModel:        "gpt-5",
			LlmAttemptProvider:     "openai",
			LlmAttemptInputTokens:  4,
			LlmAttemptOutputTokens: 6,
			LlmAttemptTokensKnown:  true,
		},
		{Type: "result", TimestampUnix: 300},
	}

	resp := aggregateUsageFromEntries(entries)
	if !resp.IsAvailable {
		t.Fatal("IsAvailable = false, want true")
	}
	if !resp.IsComplete {
		t.Fatal("IsComplete = false, want true")
	}
	if resp.Summary == nil || resp.Summary.TotalTokens != 25 {
		t.Fatalf("summary total = %#v, want 25", resp.Summary)
	}
	if len(resp.TopLevelTasks) != 1 {
		t.Fatalf("len(TopLevelTasks) = %d, want 1", len(resp.TopLevelTasks))
	}
	if resp.TopLevelTasks[0].TaskId != "top-level" {
		t.Fatalf("top-level task id = %q, want top-level", resp.TopLevelTasks[0].TaskId)
	}
	if len(resp.SubagentTasks) != 1 || resp.SubagentTasks[0].TaskId != "task-1" {
		t.Fatalf("subagent tasks = %#v, want task-1 bucket", resp.SubagentTasks)
	}
	if len(resp.Phases) != 1 || resp.Phases[0].Phase != "planning" || len(resp.Phases[0].Tasks) != 2 {
		t.Fatalf("phases = %#v, want planning phase with 2 tasks", resp.Phases)
	}
}

func TestUsageForEntryNormalizesCacheTokens(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		raw      string
		want     int64
	}{
		{name: "OpenAI input includes cache", provider: "openai", want: 15},
		{name: "Copilot input includes cache", provider: "copilot", want: 15},
		{name: "OpenRouter input includes cache", provider: "openrouter", want: 15},
		{name: "OpenAI-compatible label includes cache", provider: "OpenAI-compatible", want: 15},
		{name: "OpenAI model identifies missing provider", model: "openai/gpt-5.6", want: 15},
		{name: "authoritative inclusive overrides unknown provider", provider: "custom", raw: `{"input_tokens_include_cache":true,"input_tokens_include_cache_known":true}`, want: 15},
		{name: "authoritative additive overrides OpenAI provider", provider: "openai", raw: `{"input_tokens_include_cache":false,"input_tokens_include_cache_known":true}`, want: 22},
		{name: "Anthropic cache is additive", provider: "anthropic", want: 22},
		{name: "unknown provider remains additive", provider: "", want: 22},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := usageForEntry(&platform.ActivityEntry{
				LlmAttemptProvider:                 tt.provider,
				LlmAttemptModel:                    tt.model,
				InputRaw:                           tt.raw,
				LlmAttemptInputTokens:              10,
				LlmAttemptOutputTokens:             5,
				LlmAttemptCacheReadInputTokens:     4,
				LlmAttemptCacheCreationInputTokens: 3,
				LlmAttemptTokensKnown:              true,
			})
			if usage.totalTokens != tt.want {
				t.Fatalf("totalTokens = %d, want %d", usage.totalTokens, tt.want)
			}
			if usage.inputTokens != 10 || usage.outputTokens != 5 || usage.cacheReadInputTokens != 4 || usage.cacheCreationInputTokens != 3 {
				t.Fatalf("usage fields changed during normalization: %#v", usage)
			}
		})
	}
}

func TestPreserveEventUsageCacheSemantics(t *testing.T) {
	entry := &platform.ActivityEntry{Type: "llm_attempt", LlmAttemptProvider: "openai", InputRaw: `{"attempt_id":"a1"}`}
	preserveEventUsageCacheSemantics([]byte(`{"type":"llm_attempt","input_tokens_include_cache":false,"input_tokens_include_cache_known":true}`), entry)
	if entryInputIncludesCache(entry) {
		t.Fatal("authoritative additive semantics were not preserved")
	}
	if entry.InputRaw == `{"attempt_id":"a1"}` {
		t.Fatalf("semantics were not added to input_raw: %s", entry.InputRaw)
	}

	entry = &platform.ActivityEntry{Type: "llm_attempt", LlmAttemptProvider: "unknown"}
	preserveEventUsageCacheSemantics([]byte(`{"type":"llm_attempt","input_tokens_include_cache":true,"input_tokens_include_cache_known":true}`), entry)
	if !entryInputIncludesCache(entry) {
		t.Fatal("authoritative inclusive semantics were not preserved")
	}
}

func TestContentEventToActivityEntryMapsLLMAttempt(t *testing.T) {
	entry := contentEventToActivityEntry(&agent.ContentEvent{
		Timestamp:                time.Unix(10, 0),
		Type:                     "llm_attempt",
		ToolUseID:                "attempt-1",
		Model:                    "claude-sonnet",
		Provider:                 "anthropic",
		UsageAvailable:           true,
		InputTokens:              12,
		OutputTokens:             8,
		CacheReadInputTokens:     3,
		CacheCreationInputTokens: 1,
		CostUsd:                  0.0142,
		CostKnown:                true,
		Phase:                    "implementing",
		Step:                     "reviewing",
		AgentName:                "executor",
		TaskID:                   "task-9",
		AttemptStatus:            "retrying",
		AttemptLatencyMs:         1234,
		RetryAfterMs:             500,
		FailureKind:              "context_length_exceeded",
		Output:                   "context window exceeded",
		Turn:                     7,
	})
	if entry.Type != "llm_attempt" {
		t.Fatalf("Type = %q, want llm_attempt", entry.Type)
	}
	if entry.LlmAttemptId != "attempt-1" || entry.LlmAttemptModel != "claude-sonnet" {
		t.Fatalf("llm attempt fields = %#v", entry)
	}
	if entry.LlmAttemptInputTokens != 12 || entry.LlmAttemptOutputTokens != 8 {
		t.Fatalf("llm attempt tokens = in:%d out:%d, want 12/8", entry.LlmAttemptInputTokens, entry.LlmAttemptOutputTokens)
	}
	if entry.LlmAttemptProvider != "anthropic" {
		t.Fatalf("LlmAttemptProvider = %q, want anthropic", entry.LlmAttemptProvider)
	}
	if !entry.LlmAttemptTokensKnown {
		t.Fatal("LlmAttemptTokensKnown = false, want true")
	}
	if !entry.CostKnown || entry.CostUsd != 0.0142 {
		t.Fatalf("cost = %v/%f, want known 0.0142", entry.CostKnown, entry.CostUsd)
	}
	if entry.Phase != "implementing" || entry.Step != "reviewing" {
		t.Fatalf("phase/step = %q/%q, want implementing/reviewing", entry.Phase, entry.Step)
	}
	if entry.StatusCategory != "retrying" || entry.Reason != "context_length_exceeded" {
		t.Fatalf("status/reason = %q/%q, want retrying/context_length_exceeded", entry.StatusCategory, entry.Reason)
	}
	if entry.Output != "context window exceeded" {
		t.Fatalf("Output = %q, want provider error", entry.Output)
	}
	if entry.DurationMs != 1234 || entry.RetryAfterMs != 500 || entry.Turn != 7 {
		t.Fatalf("attempt metadata = duration:%d retry:%d turn:%d, want 1234/500/7", entry.DurationMs, entry.RetryAfterMs, entry.Turn)
	}
}

func TestContentEventToActivityEntryMapsCompaction(t *testing.T) {
	entry := contentEventToActivityEntry(&agent.ContentEvent{
		Timestamp:    time.Unix(20, 0),
		Type:         "compact_boundary",
		Message:      "Context compacted",
		Output:       "[COMPACTED HISTORY SUMMARY] older context",
		TokensBefore: 900000,
		TokensAfter:  500000,
		Phase:        "implementing",
	})
	if entry.Type != "compact_boundary" {
		t.Fatalf("Type = %q, want compact_boundary", entry.Type)
	}
	if entry.TokensBefore != 900000 || entry.TokensAfter != 500000 {
		t.Fatalf("tokens = %d/%d, want 900000/500000", entry.TokensBefore, entry.TokensAfter)
	}
	if entry.Output != "[COMPACTED HISTORY SUMMARY] older context" {
		t.Fatalf("Output = %q, want compaction summary", entry.Output)
	}
	if entry.Phase != "implementing" {
		t.Fatalf("Phase = %q, want implementing", entry.Phase)
	}
}

func TestContentEventToActivityEntryMapsSubagentProgressSnapshot(t *testing.T) {
	entry := contentEventToActivityEntry(&agent.ContentEvent{
		Timestamp:                 time.Unix(30, 0),
		Type:                      "subagent_status",
		TaskID:                    "task-exec",
		Status:                    "waiting",
		Message:                   "Waiting for dependencies",
		AgentName:                 "executor",
		SubagentDependsOn:         []string{"task-explore"},
		SubagentWaitingOn:         []string{"task-explore"},
		SubagentCurrentStep:       "dependency_wait",
		SubagentLastTool:          "read",
		SubagentFilesWritten:      2,
		SubagentMessagesReceived:  1,
		SubagentLastParentMessage: "Keep this scoped to adapters.",
		SubagentToolCount:         3,
		SubagentTokens:            88,
		SubagentDurationMs:        1500,
	})
	if entry.Type != "subagent_progress" {
		t.Fatalf("Type = %q, want subagent_progress", entry.Type)
	}
	if entry.SubagentType != "executor" || entry.SubagentStatus != "waiting" {
		t.Fatalf("subagent identity/status = %q/%q", entry.SubagentType, entry.SubagentStatus)
	}
	if len(entry.SubagentDependsOn) != 1 || entry.SubagentDependsOn[0] != "task-explore" {
		t.Fatalf("depends_on = %#v, want [task-explore]", entry.SubagentDependsOn)
	}
	if entry.SubagentCurrentStep != "dependency_wait" || entry.SubagentLastTool != "read" || entry.LastToolName != "read" {
		t.Fatalf("progress fields not mapped: %+v", entry)
	}
	if entry.SubagentFilesWritten != 2 || entry.SubagentMessagesReceived != 1 {
		t.Fatalf("progress counters = files:%d messages:%d", entry.SubagentFilesWritten, entry.SubagentMessagesReceived)
	}
}

func TestAggregateUsageFromEntriesMergesSameAttemptAcrossLifecycleEvents(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{
			Type:               "llm_attempt",
			TimestampUnix:      100,
			Phase:              "implementing",
			Step:               "implementing",
			AgentName:          "executor",
			LlmAttemptId:       "attempt-1",
			LlmAttemptModel:    "gpt-5.4",
			LlmAttemptProvider: "openai",
		},
		{
			Type:                               "llm_attempt",
			TimestampUnix:                      101,
			Phase:                              "implementing",
			Step:                               "implementing",
			AgentName:                          "executor",
			LlmAttemptId:                       "attempt-1",
			LlmAttemptModel:                    "gpt-5.4",
			LlmAttemptProvider:                 "openai",
			LlmAttemptInputTokens:              7,
			LlmAttemptOutputTokens:             5,
			LlmAttemptCacheReadInputTokens:     2,
			LlmAttemptCacheCreationInputTokens: 1,
			LlmAttemptTokensKnown:              true,
		},
		{Type: "result", TimestampUnix: 200},
	}

	resp := aggregateUsageFromEntries(entries)
	if resp.Summary == nil || resp.Summary.TotalTokens != 12 {
		t.Fatalf("summary = %#v, want total 12", resp.Summary)
	}
	if len(resp.TopLevelTasks) != 1 || len(resp.TopLevelTasks[0].Attempts) != 1 {
		t.Fatalf("TopLevelTasks = %#v, want one merged attempt", resp.TopLevelTasks)
	}
	attempt := resp.TopLevelTasks[0].Attempts[0]
	if attempt.AttemptId != "attempt-1" || attempt.Usage == nil || attempt.Usage.TotalTokens != 12 {
		t.Fatalf("attempt = %#v, want merged attempt-1 with total 12", attempt)
	}
	if attempt.Phase != "implementing" || attempt.Step != "implementing" {
		t.Fatalf("attempt phase/step = %q/%q, want implementing/implementing", attempt.Phase, attempt.Step)
	}
}
