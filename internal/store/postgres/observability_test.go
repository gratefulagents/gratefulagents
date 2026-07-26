package postgres

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// The metric-event predicate must stay textually equivalent to migration
// 040's partial index predicate; otherwise the planner falls back to scanning
// every chatty activity row in the range instead of streaming metric rows.
func TestObservabilityEventTypeListMatchesPartialIndex(t *testing.T) {
	list := observabilityEventTypeList()
	if list != "'tool_end', 'subagent_status', 'llm_attempt', 'compact_boundary'" {
		t.Fatalf("observabilityEventTypeList() = %q; update migration 040's partial index predicate together with this list", list)
	}
	migration, err := os.ReadFile("migrations/040_observability_metric_events_index.up.sql")
	if err != nil {
		t.Fatalf("reading migration 040: %v", err)
	}
	for _, clause := range []string{
		"event_type IN (" + list + ")",
		"detail->>'type' IN (" + list + ")",
	} {
		if !strings.Contains(string(migration), clause) {
			t.Fatalf("migration 040 predicate missing %q; it must match the query literal exactly", clause)
		}
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
		// Anthropic-style accounting: input_tokens exclude cached prompt tokens.
		event(10, 10*time.Minute, "llm_attempt", `{"tool_use_id":"attempt-2","provider":"anthropic","resolved_model":"model-b","attempt_status":"completed","cost_usd":0.75,"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":90,"cache_creation_input_tokens":30,"input_tokens_include_cache":false,"input_tokens_include_cache_known":true}`),
		// User-interrupted attempt: an attempt, but not a model failure.
		event(11, 11*time.Minute, "llm_attempt", `{"tool_use_id":"attempt-3","provider":"openai","resolved_model":"model-a","attempt_status":"interrupted"}`),
		// Retrying attempt with a failure kind: counts as a failure.
		event(12, 12*time.Minute, "llm_attempt", `{"tool_use_id":"attempt-4","provider":"openai","resolved_model":"model-a","attempt_status":"retrying","failure_kind":"rate_limited"}`),
		// Cancelled subagent: terminal, but user-initiated — not a failure.
		event(13, 13*time.Minute, "subagent_status", `{"task_id":"task-2","subagent_type":"reviewer","status":"cancelled"}`),
		// Error-status alias without a failure_kind: still a model failure.
		event(14, 14*time.Minute, "llm_attempt", `{"tool_use_id":"attempt-5","provider":"openai","resolved_model":"model-a","attempt_status":"error"}`),
	}
	got := aggregateObservability(store.ObservabilityQuery{Start: start, End: start.Add(time.Hour), BucketSeconds: 300}, sessions, events)
	if got.Totals.CostUSD != 2.5 || got.Totals.InputTokens != 100 || got.Totals.OutputTokens != 20 {
		t.Fatalf("run totals include attempt attribution: %+v", got.Totals)
	}
	if got.Totals.ToolCalls != 1 || got.Totals.ToolErrors != 0 {
		t.Fatalf("tool dedupe failed: %+v", got.Totals)
	}
	if got.Totals.Subagents != 2 || got.Totals.SubagentFailures != 0 {
		t.Fatalf("latest terminal subagent reduction failed (cancelled must not be a failure): %+v", got.Totals)
	}
	if got.Totals.Compactions != 2 || got.Totals.TokensReclaimed != 30 {
		t.Fatalf("compaction aggregation failed: %+v", got.Totals)
	}
	if got.Totals.LLMAttempts != 5 || got.Totals.LLMFailures != 2 {
		t.Fatalf("LLM attempt reduction failed (interrupted must not be a failure; retrying and error statuses must be): %+v", got.Totals)
	}
	if got.Totals.GenerationCostUSD != 2.0 || got.Totals.GenerationInputTokens != 170 || got.Totals.GenerationOutputTokens != 13 {
		t.Fatalf("generation attribution failed (cache-excluded input tokens must be added): %+v", got.Totals)
	}
	models := map[string]store.ObservabilityBreakdown{}
	for _, m := range got.Models {
		models[m.Name] = m
	}
	if m := models["openai/model-a"]; m.Count != 4 || m.Errors != 2 || m.CostUSD != 1.25 || m.InputTokens != 40 {
		t.Fatalf("model attribution missing: %+v", got.Models)
	}
	if m := models["anthropic/model-b"]; m.Count != 1 || m.InputTokens != 130 || m.OutputTokens != 5 {
		t.Fatalf("cache-aware model input tokens missing: %+v", got.Models)
	}
	if got.Completeness.Sessions != 1 || got.Completeness.SessionsWithActivity != 1 || !got.Completeness.ActivityComplete {
		t.Fatalf("activity completeness mixed session populations: %+v", got.Completeness)
	}
	if len(got.Buckets) != 12 || !got.Buckets[0].Start.Equal(start) {
		t.Fatalf("half-open UTC buckets incorrect: %+v", got.Buckets)
	}
}

// Rows stream out of Postgres without an ORDER BY, so the reduction must be
// order-independent: shuffling the same events must not change any counter.
func TestAggregateObservabilityIsOrderIndependent(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sessionID := uuid.New()
	sessions := []observabilitySession{{id: sessionID, created: start.Add(time.Minute), metadata: json.RawMessage(`{"metrics":{"cost_usd":2.5}}`)}}
	event := func(id int64, offset time.Duration, typ, detail string) observabilityEvent {
		return observabilityEvent{id: id, sessionID: sessionID, typ: typ, created: start.Add(offset), detail: json.RawMessage(detail)}
	}
	chronological := []observabilityEvent{
		event(1, time.Minute, "tool_end", `{"tool":"Bash","tool_use_id":"tool-1","tool_duration_ms":10}`),
		event(2, 2*time.Minute, "tool_end", `{"tool":"Bash","tool_use_id":"tool-1","is_error":true,"tool_duration_ms":20}`),
		event(3, 3*time.Minute, "llm_attempt", `{"tool_use_id":"attempt-1","provider":"openai","resolved_model":"model-a","attempt_status":"retrying","failure_kind":"rate_limited"}`),
		event(4, 4*time.Minute, "llm_attempt", `{"tool_use_id":"attempt-1","provider":"openai","resolved_model":"model-a","attempt_status":"completed","cost_usd":1.25,"input_tokens":40,"output_tokens":8}`),
		event(5, 5*time.Minute, "subagent_status", `{"task_id":"task-1","subagent_type":"reviewer","status":"failed"}`),
		event(6, 6*time.Minute, "subagent_status", `{"task_id":"task-1","subagent_type":"reviewer","status":"completed","subagent_duration_ms":30}`),
	}
	q := store.ObservabilityQuery{Start: start, End: start.Add(time.Hour), BucketSeconds: 300}
	want := aggregateObservability(q, sessions, chronological)

	shuffled := []observabilityEvent{chronological[3], chronological[5], chronological[0], chronological[4], chronological[2], chronological[1]}
	got := aggregateObservability(q, sessions, shuffled)
	if got.Totals != want.Totals {
		t.Fatalf("totals depend on row order:\n got %+v\nwant %+v", got.Totals, want.Totals)
	}
	// The tool call keeps its earliest terminal row (no error) and the attempt
	// and subagent task keep their latest (completed, not failed).
	if want.Totals.ToolCalls != 1 || want.Totals.ToolErrors != 0 || want.Totals.LLMAttempts != 1 || want.Totals.LLMFailures != 0 || want.Totals.SubagentFailures != 0 {
		t.Fatalf("lifecycle reduction changed: %+v", want.Totals)
	}
}

// Every metric event in the requested range must be aggregated. Capping the
// row set silently reports only the newest slice of the window as if it were
// the whole range, which understated generation spend by ~60%%.
func TestAggregateObservabilityCoversEveryEventInRange(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sessionID := uuid.New()
	sessions := []observabilitySession{{id: sessionID, created: start, metadata: json.RawMessage(`{}`)}}
	const attempts = 60_000
	events := make([]observabilityEvent, 0, attempts)
	for i := 0; i < attempts; i++ {
		events = append(events, observabilityEvent{
			id:        int64(i + 1),
			sessionID: sessionID,
			typ:       "llm_attempt",
			created:   start.Add(time.Duration(i) * time.Second),
			detail:    json.RawMessage(`{"tool_use_id":"attempt-` + strconv.Itoa(i) + `","provider":"openai","resolved_model":"model-a","attempt_status":"completed","cost_usd":0.01}`),
		})
	}
	got := aggregateObservability(store.ObservabilityQuery{Start: start, End: start.Add(24 * time.Hour), BucketSeconds: 3600}, sessions, events)
	if got.Totals.LLMAttempts != attempts {
		t.Fatalf("LLMAttempts = %d, want %d (no event cap)", got.Totals.LLMAttempts, attempts)
	}
	if math.Abs(got.Totals.GenerationCostUSD-float64(attempts)*0.01) > 0.01 {
		t.Fatalf("GenerationCostUSD = %v, want %v", got.Totals.GenerationCostUSD, float64(attempts)*0.01)
	}
}

// Events recorded before the runner stamped cache-accounting flags must still
// have Anthropic-style cache tokens added; assuming OpenAI semantics erases
// nearly the entire input of a warm cached run.
func TestObservabilityInputTokensIncludeCacheFallsBackToProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		detail   string
		provider string
		model    string
		want     bool
	}{
		{name: "explicit flag wins", detail: `{"input_tokens_include_cache":true,"input_tokens_include_cache_known":true}`, provider: "anthropic", model: "anthropic/claude-opus-5", want: true},
		{name: "unknown anthropic is additive", detail: `{}`, provider: "anthropic", model: "anthropic/claude-opus-5", want: false},
		{name: "unknown claude via copilot is additive", detail: `{}`, provider: "copilot", model: "copilot/claude-sonnet-4-5", want: false},
		{name: "unknown openai already includes cache", detail: `{}`, provider: "openai", model: "openai/gpt-5.6", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d map[string]any
			if err := json.Unmarshal([]byte(tc.detail), &d); err != nil {
				t.Fatal(err)
			}
			if got := observabilityInputTokensIncludeCache(d, tc.provider, tc.model); got != tc.want {
				t.Fatalf("observabilityInputTokensIncludeCache() = %v, want %v", got, tc.want)
			}
		})
	}
}

// An Anthropic attempt from before the accounting flags existed must report
// the cached prompt in generation input tokens.
func TestAggregateObservabilityAddsUnflaggedAnthropicCacheTokens(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sessionID := uuid.New()
	sessions := []observabilitySession{{id: sessionID, created: start, metadata: json.RawMessage(`{}`)}}
	events := []observabilityEvent{{
		id: 1, sessionID: sessionID, typ: "llm_attempt", created: start,
		detail: json.RawMessage(`{"tool_use_id":"attempt-1","provider":"anthropic","canonical_model":"anthropic/claude-opus-5","attempt_status":"completed","input_tokens":2,"output_tokens":965,"cache_read_input_tokens":66416,"cache_creation_input_tokens":1276}`),
	}}
	got := aggregateObservability(store.ObservabilityQuery{Start: start, End: start.Add(time.Hour), BucketSeconds: 300}, sessions, events)
	if got.Totals.GenerationInputTokens != 67694 {
		t.Fatalf("GenerationInputTokens = %d, want 67694 (cached prompt included)", got.Totals.GenerationInputTokens)
	}
}
