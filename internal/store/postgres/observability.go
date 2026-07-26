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
	"github.com/gratefulagents/gratefulagents/internal/usageaccounting"
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

// observabilityEventTypes are the only event types the aggregation reads.
// Filtering in SQL keeps chatty event types (assistant text, thinking deltas,
// tool output) from consuming the bounded event budget and silently dropping
// older metric events in busy ranges.
var observabilityEventTypes = []string{"tool_end", "subagent_status", "llm_attempt", "compact_boundary"}

// observabilityEventTypeList renders the metric event types as a SQL literal
// list. The predicate must be literal (not a bind parameter) and textually
// equivalent to migration 040's partial index predicate so the planner can
// use the index instead of scanning every chatty activity row in the window.
func observabilityEventTypeList() string {
	quoted := make([]string, len(observabilityEventTypes))
	for i, t := range observabilityEventTypes {
		quoted[i] = "'" + t + "'"
	}
	return strings.Join(quoted, ", ")
}

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

	// Metric events are streamed and folded into counters as they arrive: the
	// aggregator never retains the row set, so memory scales with the number
	// of distinct tool calls / attempts / subagent tasks in the range rather
	// than with the number of rows. A row cap here would silently amputate the
	// older part of the window — the headline spend would then report only the
	// most recent slice while claiming to cover the whole range.
	//
	// No ORDER BY: the reduction is order-independent (each lifecycle key
	// keeps its own earliest/latest row by (created_at, id)), so the planner
	// can stream straight from the partial index instead of sorting the whole
	// match set.
	types := observabilityEventTypeList()
	aggregator := newObservabilityAggregator(q, sessions)
	eventRows, err := s.pool.Query(ctx, `
SELECT e.id, e.session_id, e.event_type, e.created_at,
       e.detail - ARRAY['input_raw', 'output', 'message', 'subagent_prompt', 'subagent_result_text']::text[]
FROM activity_events e JOIN agent_sessions s ON s.id = e.session_id
WHERE s.agentrun_ns = $1
  AND e.created_at >= $2 AND e.created_at < $3
  AND s.agentrun_name = ANY($4::text[])
  AND (e.event_type IN (`+types+`) OR e.detail->>'type' IN (`+types+`))`,
		q.Namespace, q.Start, q.End, q.AgentRunNames)
	if err != nil {
		return nil, err
	}
	for eventRows.Next() {
		var event observabilityEvent
		if err := eventRows.Scan(&event.id, &event.sessionID, &event.typ, &event.created, &event.detail); err != nil {
			eventRows.Close()
			return nil, err
		}
		aggregator.add(event)
	}
	if err := eventRows.Err(); err != nil {
		eventRows.Close()
		return nil, err
	}
	eventRows.Close()
	overview := aggregator.finish()
	// The event query above is filtered to metric-relevant types, so derive
	// activity coverage from any event kind: a session that only produced
	// chatty events still has activity.
	var withActivity int64
	if err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM agent_sessions s
WHERE s.agentrun_ns = $1 AND s.created_at >= $2 AND s.created_at < $3
  AND s.agentrun_name = ANY($4::text[])
  AND EXISTS (
    SELECT 1 FROM activity_events e
    WHERE e.session_id = s.id AND e.created_at >= $2 AND e.created_at < $3
  )`, q.Namespace, q.Start, q.End, q.AgentRunNames).Scan(&withActivity); err != nil {
		return nil, err
	}
	overview.Completeness.SessionsWithActivity = withActivity
	overview.Completeness.ActivityComplete = overview.Completeness.Sessions == withActivity
	return overview, nil
}

// aggregateObservability folds a materialized event slice; the store streams
// rows through the aggregator directly.
func aggregateObservability(q store.ObservabilityQuery, sessions []observabilitySession, events []observabilityEvent) *store.ObservabilityOverview {
	aggregator := newObservabilityAggregator(q, sessions)
	for _, event := range events {
		aggregator.add(event)
	}
	return aggregator.finish()
}

// observabilityAggregator folds metric events into overview counters as they
// stream out of Postgres. Only per-lifecycle-key state is retained, so memory
// scales with distinct tool calls / model attempts / subagent tasks instead of
// with the row count, and the whole selected range can be aggregated without a
// row cap that would silently drop its older half.
//
// The reduction is order-independent: each key remembers the (created_at, id)
// of the row it accepted and yields only to a strictly earlier row (tool
// calls, whose first terminal event is authoritative) or a strictly later row
// (model attempts and subagent tasks, whose latest lifecycle event wins). Rows
// may therefore arrive in any order, which lets the query skip an ORDER BY
// over the entire match set.
type observabilityAggregator struct {
	q                  store.ObservabilityQuery
	out                *store.ObservabilityOverview
	buckets            map[int64]*store.ObservabilityBucket
	snapshotSessionIDs map[uuid.UUID]struct{}
	activitySessions   map[uuid.UUID]struct{}
	tools              map[string]*observabilityToolState
	attempts           map[string]*observabilityAttemptState
	subagents          map[string]*observabilitySubagentState
}

// observabilityRowKey orders two rows of the same lifecycle key.
type observabilityRowKey struct {
	at time.Time
	id int64
}

func (k observabilityRowKey) before(other observabilityRowKey) bool {
	if !k.at.Equal(other.at) {
		return k.at.Before(other.at)
	}
	return k.id < other.id
}

type observabilityToolState struct {
	row      observabilityRowKey
	name     string
	isError  bool
	duration float64
}

type observabilityAttemptState struct {
	row     observabilityRowKey
	model   string
	failed  bool
	cost    float64
	input   int64
	output  int64
	latency float64
}

type observabilitySubagentState struct {
	row      observabilityRowKey
	name     string
	failed   bool
	cost     float64
	input    int64
	output   int64
	duration float64
}

func newObservabilityAggregator(q store.ObservabilityQuery, sessions []observabilitySession) *observabilityAggregator {
	a := &observabilityAggregator{
		q:                  q,
		out:                &store.ObservabilityOverview{},
		buckets:            map[int64]*store.ObservabilityBucket{},
		snapshotSessionIDs: make(map[uuid.UUID]struct{}, len(sessions)),
		activitySessions:   map[uuid.UUID]struct{}{},
		tools:              map[string]*observabilityToolState{},
		attempts:           map[string]*observabilityAttemptState{},
		subagents:          map[string]*observabilitySubagentState{},
	}
	a.out.Completeness.Sessions = int64(len(sessions))
	for at := q.Start; at.Before(q.End); at = at.Add(time.Duration(q.BucketSeconds) * time.Second) {
		a.bucket(at)
	}
	for _, session := range sessions {
		a.snapshotSessionIDs[session.id] = struct{}{}
		a.out.Totals.Runs++
		b := a.bucket(session.created)
		b.Totals.Runs++
		var metadata struct {
			Metrics *struct {
				CostUSD      float64 `json:"cost_usd"`
				InputTokens  int64   `json:"input_tokens"`
				OutputTokens int64   `json:"output_tokens"`
			} `json:"metrics"`
		}
		if json.Unmarshal(session.metadata, &metadata) == nil && metadata.Metrics != nil {
			a.out.Completeness.SessionsWithMetrics++
			a.out.Totals.CostUSD += metadata.Metrics.CostUSD
			a.out.Totals.InputTokens += metadata.Metrics.InputTokens
			a.out.Totals.OutputTokens += metadata.Metrics.OutputTokens
			b.Totals.CostUSD += metadata.Metrics.CostUSD
			b.Totals.InputTokens += metadata.Metrics.InputTokens
			b.Totals.OutputTokens += metadata.Metrics.OutputTokens
		}
	}
	a.out.Completeness.MetricsComplete = a.out.Completeness.Sessions == a.out.Completeness.SessionsWithMetrics
	return a
}

func (a *observabilityAggregator) bucket(at time.Time) *store.ObservabilityBucket {
	unix := a.q.Start.Unix() + (at.Unix()-a.q.Start.Unix())/a.q.BucketSeconds*a.q.BucketSeconds
	b := a.buckets[unix]
	if b == nil {
		b = &store.ObservabilityBucket{Start: time.Unix(unix, 0).UTC()}
		a.buckets[unix] = b
	}
	return b
}

func (a *observabilityAggregator) add(event observabilityEvent) {
	if _, ok := a.snapshotSessionIDs[event.sessionID]; ok {
		a.activitySessions[event.sessionID] = struct{}{}
	}
	var d map[string]any
	if json.Unmarshal(event.detail, &d) != nil {
		d = map[string]any{}
	}
	typ := event.typ
	if v := stringValue(d["type"]); v != "" {
		typ = v
	}
	row := observabilityRowKey{at: event.created, id: event.id}
	switch typ {
	case "tool_end":
		key := stringValue(d["tool_use_id"])
		if key == "" {
			// Events without a tool_use_id cannot be deduplicated; give each
			// its own key so none are collapsed into a single call.
			key = fmt.Sprintf("event-%d", event.id)
		}
		key = event.sessionID.String() + "/" + key
		name := stringValue(d["tool"])
		if name == "" {
			name = "unknown"
		}
		state := &observabilityToolState{row: row, name: name, isError: boolValue(d["is_error"]), duration: numberValue(d["tool_duration_ms"])}
		if current := a.tools[key]; current == nil || state.row.before(current.row) {
			a.tools[key] = state
		}
	case "subagent_status":
		task := stringValue(d["task_id"])
		status := strings.ToLower(stringValue(d["status"]))
		if task == "" || (status != "completed" && status != "failed" && status != "cancelled" && status != "stopped") {
			return
		}
		name := stringValue(d["subagent_type"])
		if name == "" {
			name = "unknown"
		}
		state := &observabilitySubagentState{
			row:  row,
			name: name,
			// Only genuine failures count against reliability; cancelled and
			// stopped tasks are user-initiated terminations, not errors.
			failed:   status == "failed",
			cost:     numberValue(d["subagent_cost_usd"]),
			input:    int64(numberValue(d["subagent_input_tokens"])),
			output:   int64(numberValue(d["subagent_output_tokens"])),
			duration: numberValue(d["subagent_duration_ms"]),
		}
		if current := a.subagents[event.sessionID.String()+"/"+task]; current == nil || current.row.before(state.row) {
			a.subagents[event.sessionID.String()+"/"+task] = state
		}
	case "llm_attempt":
		key := stringValue(d["tool_use_id"])
		if key == "" {
			key = fmt.Sprintf("event-%d", event.id)
		}
		key = event.sessionID.String() + "/" + key
		state := observabilityAttemptFromDetail(row, d)
		if current := a.attempts[key]; current == nil || current.row.before(state.row) {
			a.attempts[key] = state
		}
	case "compact_boundary":
		b := a.bucket(event.created)
		a.out.Totals.Compactions++
		b.Totals.Compactions++
		reclaimed := int64(numberValue(d["tokens_before"]) - numberValue(d["tokens_after"]))
		reclaimed = max(reclaimed, 0)
		a.out.Totals.TokensReclaimed += reclaimed
		b.Totals.TokensReclaimed += reclaimed
	}
}

func observabilityAttemptFromDetail(row observabilityRowKey, d map[string]any) *observabilityAttemptState {
	status := stringValue(d["attempt_status"])
	if status == "" {
		status = stringValue(d["status"])
	}
	status = strings.ToLower(status)
	// A model failure is a provider/model error (failure_kind is set on
	// retrying/fallback/failed attempts) or an explicit error status, matching
	// the aliases GetRecentErrorActivity and isErrorContentEvent recognize.
	// User-initiated interruptions ("interrupted") are not failures.
	failed := stringValue(d["failure_kind"]) != ""
	switch status {
	case "failed", "retrying", "fallback", "error", "failure", "fatal":
		failed = true
	}
	provider := stringValue(d["provider"])
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
	if provider != "" && !strings.Contains(name, "/") {
		name = provider + "/" + name
	}
	inputTokens := int64(numberValue(firstValue(d, "input_tokens", "prompt_tokens")))
	if !observabilityInputTokensIncludeCache(d, provider, name) {
		inputTokens += int64(numberValue(d["cache_read_input_tokens"])) + int64(numberValue(d["cache_creation_input_tokens"]))
	}
	return &observabilityAttemptState{
		row:     row,
		model:   name,
		failed:  failed,
		cost:    numberValue(d["cost_usd"]),
		input:   inputTokens,
		output:  int64(numberValue(firstValue(d, "output_tokens", "completion_tokens"))),
		latency: numberValue(d["attempt_latency_ms"]),
	}
}

// observabilityInputTokensIncludeCache reports whether an attempt's
// input_tokens already cover the cached prompt. The runner stamps the answer
// on the event; older events predate those flags, so fall back to the shared
// provider classification every usage reader uses (internal/usageaccounting).
// Assuming "already included" would silently erase the cached prompt — nearly
// the entire input of a warm Anthropic run.
func observabilityInputTokensIncludeCache(d map[string]any, provider, model string) bool {
	return usageaccounting.InputIncludesCache(
		boolValue(d["input_tokens_include_cache_known"]),
		boolValue(d["input_tokens_include_cache"]),
		provider, model,
	)
}

func (a *observabilityAggregator) finish() *store.ObservabilityOverview {
	tools := map[string]*breakdownAccumulator{}
	subagents := map[string]*breakdownAccumulator{}
	models := map[string]*breakdownAccumulator{}
	for _, state := range a.tools {
		b := a.bucket(state.row.at)
		acc := ensureBreakdown(tools, state.name)
		acc.value.Count++
		a.out.Totals.ToolCalls++
		b.Totals.ToolCalls++
		if state.isError {
			acc.value.Errors++
			a.out.Totals.ToolErrors++
			b.Totals.ToolErrors++
		}
		if state.duration > 0 {
			acc.durations = append(acc.durations, state.duration)
		}
	}
	for _, state := range a.attempts {
		b := a.bucket(state.row.at)
		a.out.Totals.LLMAttempts++
		b.Totals.LLMAttempts++
		if state.failed {
			a.out.Totals.LLMFailures++
			b.Totals.LLMFailures++
		}
		a.out.Totals.GenerationCostUSD += state.cost
		a.out.Totals.GenerationInputTokens += state.input
		a.out.Totals.GenerationOutputTokens += state.output
		b.Totals.GenerationCostUSD += state.cost
		b.Totals.GenerationInputTokens += state.input
		b.Totals.GenerationOutputTokens += state.output
		acc := ensureBreakdown(models, state.model)
		acc.value.Count++
		if state.failed {
			acc.value.Errors++
		}
		acc.value.CostUSD += state.cost
		acc.value.InputTokens += state.input
		acc.value.OutputTokens += state.output
		if state.latency > 0 {
			acc.durations = append(acc.durations, state.latency)
		}
	}
	for _, state := range a.subagents {
		b := a.bucket(state.row.at)
		acc := ensureBreakdown(subagents, state.name)
		acc.value.Count++
		a.out.Totals.Subagents++
		b.Totals.Subagents++
		if state.failed {
			acc.value.Errors++
			a.out.Totals.SubagentFailures++
			b.Totals.SubagentFailures++
		}
		acc.value.CostUSD += state.cost
		acc.value.InputTokens += state.input
		acc.value.OutputTokens += state.output
		if state.duration > 0 {
			acc.durations = append(acc.durations, state.duration)
		}
	}
	a.out.Completeness.SessionsWithActivity = int64(len(a.activitySessions))
	a.out.Completeness.ActivityComplete = a.out.Completeness.Sessions == a.out.Completeness.SessionsWithActivity
	for _, b := range a.buckets {
		a.out.Buckets = append(a.out.Buckets, *b)
	}
	sort.Slice(a.out.Buckets, func(i, j int) bool { return a.out.Buckets[i].Start.Before(a.out.Buckets[j].Start) })
	a.out.Tools = finishBreakdowns(tools)
	a.out.Subagents = finishBreakdowns(subagents)
	a.out.Models = finishBreakdowns(models)
	return a.out
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
