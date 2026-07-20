package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

type observabilitySession struct {
	id       uuid.UUID
	name     string
	created  time.Time
	metadata json.RawMessage
}

type observabilityEvent struct {
	id        int64
	sessionID uuid.UUID
	typ       string
	created   time.Time
	detail    json.RawMessage
}

const observabilityMaxEvents = 50_000

type breakdownAccumulator struct {
	value     store.ObservabilityBreakdown
	durations []float64
}

func (s *Store) GetObservabilityOverview(ctx context.Context, q store.ObservabilityQuery) (*store.ObservabilityOverview, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, agentrun_name, created_at, metadata
FROM agent_sessions
WHERE agentrun_ns = $1 AND created_at >= $2 AND created_at < $3
  AND agentrun_name = ANY($4::text[])
ORDER BY created_at, id`, q.Namespace, q.Start, q.End, q.AgentRunNames)
	if err != nil {
		return nil, err
	}
	var sessions []observabilitySession
	for rows.Next() {
		var row observabilitySession
		if err := rows.Scan(&row.id, &row.name, &row.created, &row.metadata); err != nil {
			rows.Close()
			return nil, err
		}
		sessions = append(sessions, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	eventRows, err := s.pool.Query(ctx, `
SELECT e.id, e.session_id, e.event_type, e.created_at,
       e.detail - ARRAY['input_raw', 'output', 'message', 'subagent_prompt', 'subagent_result_text']::text[]
FROM activity_events e JOIN agent_sessions s ON s.id = e.session_id
WHERE s.agentrun_ns = $1
  AND e.created_at >= $2 AND e.created_at < $3
  AND s.agentrun_name = ANY($4::text[])
ORDER BY e.created_at DESC, e.id DESC
LIMIT $5`, q.Namespace, q.Start, q.End, q.AgentRunNames, observabilityMaxEvents+1)
	if err != nil {
		return nil, err
	}
	var events []observabilityEvent
	for eventRows.Next() {
		var event observabilityEvent
		if err := eventRows.Scan(&event.id, &event.sessionID, &event.typ, &event.created, &event.detail); err != nil {
			eventRows.Close()
			return nil, err
		}
		events = append(events, event)
	}
	if err := eventRows.Err(); err != nil {
		eventRows.Close()
		return nil, err
	}
	eventRows.Close()
	events, truncated := newestObservabilityEvents(events, observabilityMaxEvents)
	overview := aggregateObservability(q, sessions, events)
	overview.Completeness.ActivityTruncated = truncated
	if truncated {
		overview.Completeness.ActivityComplete = false
	}
	return overview, nil
}

// newestObservabilityEvents accepts rows ordered newest-first, keeps a bounded
// prefix, and returns them oldest-first for deterministic lifecycle reduction.
func newestObservabilityEvents(events []observabilityEvent, limit int) ([]observabilityEvent, bool) {
	truncated := len(events) > limit
	if truncated {
		events = events[:limit]
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return events, truncated
}

func aggregateObservability(q store.ObservabilityQuery, sessions []observabilitySession, events []observabilityEvent) *store.ObservabilityOverview {
	out := &store.ObservabilityOverview{}
	out.Completeness.Sessions = int64(len(sessions))
	snapshotSessionIDs := make(map[uuid.UUID]struct{}, len(sessions))
	for _, session := range sessions {
		snapshotSessionIDs[session.id] = struct{}{}
	}
	buckets := map[int64]*store.ObservabilityBucket{}
	bucket := func(at time.Time) *store.ObservabilityBucket {
		unix := q.Start.Unix() + (at.Unix()-q.Start.Unix())/q.BucketSeconds*q.BucketSeconds
		b := buckets[unix]
		if b == nil {
			b = &store.ObservabilityBucket{Start: time.Unix(unix, 0).UTC()}
			buckets[unix] = b
		}
		return b
	}
	for at := q.Start; at.Before(q.End); at = at.Add(time.Duration(q.BucketSeconds) * time.Second) {
		bucket(at)
	}
	for _, session := range sessions {
		out.Totals.Runs++
		bucket(session.created).Totals.Runs++
		var metadata struct {
			Metrics *struct {
				CostUSD      float64 `json:"cost_usd"`
				InputTokens  int64   `json:"input_tokens"`
				OutputTokens int64   `json:"output_tokens"`
			} `json:"metrics"`
		}
		if json.Unmarshal(session.metadata, &metadata) == nil && metadata.Metrics != nil {
			out.Completeness.SessionsWithMetrics++
			out.Totals.CostUSD += metadata.Metrics.CostUSD
			out.Totals.InputTokens += metadata.Metrics.InputTokens
			out.Totals.OutputTokens += metadata.Metrics.OutputTokens
			b := bucket(session.created)
			b.Totals.CostUSD += metadata.Metrics.CostUSD
			b.Totals.InputTokens += metadata.Metrics.InputTokens
			b.Totals.OutputTokens += metadata.Metrics.OutputTokens
		}
	}
	out.Completeness.MetricsComplete = out.Completeness.Sessions == out.Completeness.SessionsWithMetrics

	tools := map[string]*breakdownAccumulator{}
	subagents := map[string]*breakdownAccumulator{}
	models := map[string]*breakdownAccumulator{}
	activitySessions := map[uuid.UUID]bool{}
	toolIDs := map[string]bool{}
	type terminal struct {
		event  observabilityEvent
		detail map[string]any
	}
	terminalSubagents := map[string]terminal{}
	llmAttempts := map[string]terminal{}
	for _, event := range events {
		if _, ok := snapshotSessionIDs[event.sessionID]; ok {
			activitySessions[event.sessionID] = true
		}
		var d map[string]any
		if json.Unmarshal(event.detail, &d) != nil {
			d = map[string]any{}
		}
		typ := event.typ
		if v := stringValue(d["type"]); v != "" {
			typ = v
		}
		switch typ {
		case "tool_end":
			toolID := stringValue(d["tool_use_id"])
			if toolID != "" {
				key := event.sessionID.String() + "/" + toolID
				if toolIDs[key] {
					continue
				}
				toolIDs[key] = true
			}
			name := stringValue(d["tool"])
			if name == "" {
				name = "unknown"
			}
			a := ensureBreakdown(tools, name)
			a.value.Count++
			out.Totals.ToolCalls++
			bucket(event.created).Totals.ToolCalls++
			if boolValue(d["is_error"]) {
				a.value.Errors++
				out.Totals.ToolErrors++
				bucket(event.created).Totals.ToolErrors++
			}
			if duration := numberValue(d["tool_duration_ms"]); duration > 0 {
				a.durations = append(a.durations, duration)
			}
		case "subagent_status":
			task := stringValue(d["task_id"])
			status := strings.ToLower(stringValue(d["status"]))
			if task != "" && (status == "completed" || status == "failed" || status == "cancelled" || status == "stopped") {
				terminalSubagents[event.sessionID.String()+"/"+task] = terminal{event, d}
			}
		case "llm_attempt":
			attemptID := stringValue(d["tool_use_id"])
			if attemptID == "" {
				attemptID = fmt.Sprintf("event-%d", event.id)
			}
			llmAttempts[event.sessionID.String()+"/"+attemptID] = terminal{event, d}
		case "compact_boundary":
			out.Totals.Compactions++
			bucket(event.created).Totals.Compactions++
			reclaimed := int64(numberValue(d["tokens_before"]) - numberValue(d["tokens_after"]))
			if reclaimed < 0 {
				reclaimed = 0
			}
			out.Totals.TokensReclaimed += reclaimed
			bucket(event.created).Totals.TokensReclaimed += reclaimed
		}
	}
	for _, attempt := range llmAttempts {
		d := attempt.detail
		b := bucket(attempt.event.created)
		out.Totals.LLMAttempts++
		b.Totals.LLMAttempts++
		status := stringValue(d["attempt_status"])
		if status == "" {
			status = stringValue(d["status"])
		}
		status = strings.ToLower(status)
		// A model failure is a provider/model error (failure_kind is set on
		// retrying/fallback/failed attempts) or an explicit failed status.
		// User-initiated interruptions ("interrupted") are not failures.
		failed := stringValue(d["failure_kind"]) != "" || status == "failed" || status == "retrying" || status == "fallback"
		if failed {
			out.Totals.LLMFailures++
			b.Totals.LLMFailures++
		}
		name := stringValue(d["canonical_model"])
		if name == "" {
			name = stringValue(d["resolved_model"])
		}
		if name == "" {
			name = stringValue(d["model"])
		}
		if name == "" {
			name = "unknown"
		}
		if provider := stringValue(d["provider"]); provider != "" && !strings.Contains(name, "/") {
			name = provider + "/" + name
		}
		cost := numberValue(d["cost_usd"])
		inputTokens := int64(numberValue(firstValue(d, "input_tokens", "prompt_tokens")))
		// Providers differ on whether input_tokens already include cached
		// prompt tokens (OpenAI-style: yes, Anthropic-style: no). When the
		// event explicitly says they are excluded, add them so generation
		// input tokens reflect the full processed prompt instead of
		// undercounting. Events without the accounting flags are left as-is
		// to avoid double counting.
		if boolValue(d["input_tokens_include_cache_known"]) && !boolValue(d["input_tokens_include_cache"]) {
			inputTokens += int64(numberValue(d["cache_read_input_tokens"])) + int64(numberValue(d["cache_creation_input_tokens"]))
		}
		outputTokens := int64(numberValue(firstValue(d, "output_tokens", "completion_tokens")))
		out.Totals.GenerationCostUSD += cost
		out.Totals.GenerationInputTokens += inputTokens
		out.Totals.GenerationOutputTokens += outputTokens
		b.Totals.GenerationCostUSD += cost
		b.Totals.GenerationInputTokens += inputTokens
		b.Totals.GenerationOutputTokens += outputTokens
		a := ensureBreakdown(models, name)
		a.value.Count++
		if failed {
			a.value.Errors++
		}
		a.value.CostUSD += cost
		a.value.InputTokens += inputTokens
		a.value.OutputTokens += outputTokens
		if duration := numberValue(d["attempt_latency_ms"]); duration > 0 {
			a.durations = append(a.durations, duration)
		}
	}
	for _, terminal := range terminalSubagents {
		d := terminal.detail
		name := stringValue(d["subagent_type"])
		if name == "" {
			name = "unknown"
		}
		a := ensureBreakdown(subagents, name)
		a.value.Count++
		out.Totals.Subagents++
		bucket(terminal.event.created).Totals.Subagents++
		// Only genuine failures count against reliability; cancelled/stopped
		// tasks are user-initiated terminations, not subagent errors.
		if status := strings.ToLower(stringValue(d["status"])); status == "failed" {
			a.value.Errors++
			out.Totals.SubagentFailures++
			bucket(terminal.event.created).Totals.SubagentFailures++
		}
		a.value.CostUSD += numberValue(d["subagent_cost_usd"])
		a.value.InputTokens += int64(numberValue(d["subagent_input_tokens"]))
		a.value.OutputTokens += int64(numberValue(d["subagent_output_tokens"]))
		if duration := numberValue(d["subagent_duration_ms"]); duration > 0 {
			a.durations = append(a.durations, duration)
		}
	}
	out.Completeness.SessionsWithActivity = int64(len(activitySessions))
	out.Completeness.ActivityComplete = out.Completeness.Sessions == out.Completeness.SessionsWithActivity
	for _, b := range buckets {
		out.Buckets = append(out.Buckets, *b)
	}
	sort.Slice(out.Buckets, func(i, j int) bool { return out.Buckets[i].Start.Before(out.Buckets[j].Start) })
	out.Tools = finishBreakdowns(tools)
	out.Subagents = finishBreakdowns(subagents)
	out.Models = finishBreakdowns(models)
	return out
}

func ensureBreakdown(values map[string]*breakdownAccumulator, name string) *breakdownAccumulator {
	if values[name] == nil {
		values[name] = &breakdownAccumulator{value: store.ObservabilityBreakdown{Name: name}}
	}
	return values[name]
}

func finishBreakdowns(values map[string]*breakdownAccumulator) []store.ObservabilityBreakdown {
	out := make([]store.ObservabilityBreakdown, 0, len(values))
	for _, a := range values {
		sort.Float64s(a.durations)
		if len(a.durations) > 0 {
			var sum float64
			for _, d := range a.durations {
				sum += d
			}
			a.value.AverageDurationMS = sum / float64(len(a.durations))
			a.value.P95DurationMS = a.durations[int(math.Ceil(.95*float64(len(a.durations))))-1]
		}
		out = append(out, a.value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func stringValue(v any) string  { s, _ := v.(string); return strings.TrimSpace(s) }
func boolValue(v any) bool      { b, _ := v.(bool); return b }
func numberValue(v any) float64 { n, _ := v.(float64); return n }
func firstValue(d map[string]any, keys ...string) any {
	for _, k := range keys {
		if _, ok := d[k]; ok {
			return d[k]
		}
	}
	return nil
}
