package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestNewestObservabilityEventsBoundsAndRestoresChronology(t *testing.T) {
	events := []observabilityEvent{{id: 5}, {id: 4}, {id: 3}}
	got, truncated := newestObservabilityEvents(events, 2)
	if !truncated || len(got) != 2 || got[0].id != 4 || got[1].id != 5 {
		t.Fatalf("newestObservabilityEvents() = %+v, truncated=%v", got, truncated)
	}
}

func TestAggregateObservabilityAvoidsDoubleCounting(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sessionID := uuid.New()
	sessions := []observabilitySession{{id: sessionID, created: start.Add(time.Minute), metadata: json.RawMessage(`{"metrics":{"cost_usd":2.5,"input_tokens":100,"output_tokens":20}}`)}}
	event := func(id int64, offset time.Duration, typ, detail string) observabilityEvent {
		return observabilityEvent{id: id, sessionID: sessionID, typ: typ, created: start.Add(offset), detail: json.RawMessage(detail)}
	}
	events := []observabilityEvent{
		event(1, time.Minute, "tool_end", `{"tool":"Bash","tool_use_id":"tool-1","tool_duration_ms":10}`),
		event(2, 2*time.Minute, "tool_end", `{"tool":"Bash","tool_use_id":"tool-1","is_error":true,"tool_duration_ms":20}`),
		event(3, 3*time.Minute, "llm_attempt", `{"tool_use_id":"attempt-1","provider":"openai","resolved_model":"model-a","attempt_status":"started"}`),
		event(4, 4*time.Minute, "llm_attempt", `{"tool_use_id":"attempt-1","provider":"openai","resolved_model":"model-a","attempt_status":"completed","cost_usd":1.25,"input_tokens":40,"output_tokens":8}`),
		event(5, 5*time.Minute, "subagent_status", `{"task_id":"task-1","subagent_type":"reviewer","status":"failed"}`),
		event(6, 6*time.Minute, "subagent_status", `{"task_id":"task-1","subagent_type":"reviewer","status":"completed","subagent_duration_ms":30}`),
		event(7, 7*time.Minute, "compact_boundary", `{"tokens_before":80,"tokens_after":50}`),
		event(8, 8*time.Minute, "compact_boundary", `{"tokens_before":40,"tokens_after":60}`),
		{id: 9, sessionID: uuid.New(), typ: "assistant_text", created: start.Add(9 * time.Minute), detail: json.RawMessage(`{"type":"assistant_text"}`)},
	}
	got := aggregateObservability(store.ObservabilityQuery{Start: start, End: start.Add(time.Hour), BucketSeconds: 300}, sessions, events)
	if got.Totals.CostUSD != 2.5 || got.Totals.InputTokens != 100 || got.Totals.OutputTokens != 20 {
		t.Fatalf("run totals include attempt attribution: %+v", got.Totals)
	}
	if got.Totals.ToolCalls != 1 || got.Totals.ToolErrors != 0 {
		t.Fatalf("tool dedupe failed: %+v", got.Totals)
	}
	if got.Totals.Subagents != 1 || got.Totals.SubagentFailures != 0 {
		t.Fatalf("latest terminal subagent reduction failed: %+v", got.Totals)
	}
	if got.Totals.Compactions != 2 || got.Totals.TokensReclaimed != 30 {
		t.Fatalf("compaction aggregation failed: %+v", got.Totals)
	}
	if got.Totals.LLMAttempts != 1 || got.Totals.GenerationCostUSD != 1.25 || got.Totals.GenerationInputTokens != 40 || got.Totals.GenerationOutputTokens != 8 {
		t.Fatalf("LLM attempt reduction or generation attribution failed: %+v", got.Totals)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "openai/model-a" || got.Models[0].CostUSD != 1.25 || got.Models[0].InputTokens != 40 {
		t.Fatalf("model attribution missing: %+v", got.Models)
	}
	if got.Completeness.Sessions != 1 || got.Completeness.SessionsWithActivity != 1 || !got.Completeness.ActivityComplete {
		t.Fatalf("activity completeness mixed session populations: %+v", got.Completeness)
	}
	if len(got.Buckets) != 12 || !got.Buckets[0].Start.Equal(start) {
		t.Fatalf("half-open UTC buckets incorrect: %+v", got.Buckets)
	}
}
