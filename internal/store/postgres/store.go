package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/postgres/sqlc"
)

// Store implements store.StateStore backed by Postgres via pgx + sqlc.
type Store struct {
	pool         *pgxpool.Pool
	queries      *sqlc.Queries
	contentBlobs store.ProjectContentBlobStore
}

// SetProjectContentBlobStore routes new project-content version bodies to
// object storage. It must be called during process setup, before serving
// project-content requests.
func (s *Store) SetProjectContentBlobStore(blobs store.ProjectContentBlobStore) {
	s.contentBlobs = blobs
}

// New creates a new Postgres-backed StateStore.
// dsn is a Postgres connection string (e.g. "postgres://user:pass@host:5432/db").
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing postgres dsn: %w", err)
	}
	if v := os.Getenv("POSTGRES_MAX_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxConns = int32(n)
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	return &Store{
		pool:    pool,
		queries: sqlc.New(pool),
	}, nil
}

// NewFromPool creates a Store from an existing connection pool.
func NewFromPool(pool *pgxpool.Pool) *Store {
	return &Store{
		pool:    pool,
		queries: sqlc.New(pool),
	}
}

func (s *Store) Close() error {
	s.pool.Close()
	return nil
}

// Pool returns the underlying connection pool (e.g. for running migrations).
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// --- Session lifecycle ---

func (s *Store) CreateSession(ctx context.Context, agentRunName, agentRunNS, phase, currentStep string) (*store.Session, error) {
	row, err := s.queries.CreateSession(ctx, sqlc.CreateSessionParams{
		AgentrunName: agentRunName,
		AgentrunNs:   agentRunNS,
		Phase:        phase,
		CurrentStep:  currentStep,
	})
	if err != nil {
		if isUniqueViolation(err) {
			existing, lookupErr := s.GetSessionByRun(ctx, agentRunName, agentRunNS)
			if lookupErr == nil {
				return existing, nil
			}
			return nil, fmt.Errorf("creating session: duplicate exists but lookup failed: %w", lookupErr)
		}
		return nil, fmt.Errorf("creating session: %w", err)
	}
	return sessionFromRow(row), nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *Store) GetSession(ctx context.Context, id uuid.UUID) (*store.Session, error) {
	row, err := s.queries.GetSession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	return sessionFromRow(row), nil
}

func (s *Store) GetSessionByRun(ctx context.Context, agentRunName, agentRunNS string) (*store.Session, error) {
	row, err := s.queries.GetSessionByRun(ctx, sqlc.GetSessionByRunParams{
		AgentrunName: agentRunName,
		AgentrunNs:   agentRunNS,
	})
	if err != nil {
		return nil, fmt.Errorf("getting session by run: %w", err)
	}
	return sessionFromRow(row), nil
}

func (s *Store) ListSessionsByNamespace(ctx context.Context, namespace string) ([]store.Session, error) {
	// The namespace filter is an explicit SQL branch instead of
	// `$1 = '' OR agentrun_ns = $1`: the OR form forces a generic plan that
	// can never use the agentrun_ns index.
	query := `SELECT id, agentrun_name, agentrun_ns, phase, current_step, pending_question, metadata, created_at, updated_at, pending_actions, pending_input_type, pending_request_id
		 FROM agent_sessions`
	var args []any
	if namespace != "" {
		query += ` WHERE agentrun_ns = $1`
		args = append(args, namespace)
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing sessions by namespace: %w", err)
	}
	defer rows.Close()
	var out []store.Session
	for rows.Next() {
		var r sqlc.AgentSession
		if err := rows.Scan(&r.ID, &r.AgentrunName, &r.AgentrunNs, &r.Phase, &r.CurrentStep, &r.PendingQuestion, &r.Metadata, &r.CreatedAt, &r.UpdatedAt, &r.PendingActions, &r.PendingInputType, &r.PendingRequestID); err != nil {
			return nil, fmt.Errorf("scanning session: %w", err)
		}
		out = append(out, *sessionFromRow(r))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing sessions by namespace: %w", err)
	}
	return out, nil
}

func (s *Store) UpdatePhase(ctx context.Context, id uuid.UUID, phase, currentStep string) error {
	return s.queries.UpdateSessionPhase(ctx, sqlc.UpdateSessionPhaseParams{
		ID:          id,
		Phase:       phase,
		CurrentStep: currentStep,
	})
}

func (s *Store) SetPendingQuestion(ctx context.Context, id uuid.UUID, phase, question, inputType string) error {
	return s.queries.UpdateSessionPendingQuestion(ctx, sqlc.UpdateSessionPendingQuestionParams{
		ID:               id,
		Phase:            phase,
		PendingQuestion:  question,
		PendingInputType: inputType,
		PendingRequestID: uuid.NewString(),
	})
}

func (s *Store) ClearPendingQuestion(ctx context.Context, id uuid.UUID, phase string) error {
	return s.queries.ClearPendingQuestion(ctx, sqlc.ClearPendingQuestionParams{
		ID:    id,
		Phase: phase,
	})
}

func (s *Store) SetPendingAction(ctx context.Context, id uuid.UUID, phase, question string, actions json.RawMessage, inputType string) error {
	return s.queries.UpdateSessionPendingAction(ctx, sqlc.UpdateSessionPendingActionParams{
		ID:               id,
		Phase:            phase,
		PendingQuestion:  question,
		PendingActions:   actions,
		PendingInputType: inputType,
		PendingRequestID: uuid.NewString(),
	})
}

func (s *Store) ClearPendingAction(ctx context.Context, id uuid.UUID, phase string) error {
	return s.queries.ClearPendingAction(ctx, sqlc.ClearPendingActionParams{
		ID:    id,
		Phase: phase,
	})
}

// ReservePendingInputResponse atomically verifies the immutable pending request
// ID, appends one held response, and clears that exact request. A replay returns
// the existing delivery without touching any newer pending request.
func (s *Store) ReservePendingInputResponse(ctx context.Context, sessionID uuid.UUID, resolution store.PendingInputResolution) (*store.Message, bool, error) {
	resolution.RequestID = strings.TrimSpace(resolution.RequestID)
	resolution.Phase = strings.TrimSpace(resolution.Phase)
	resolution.Role = strings.TrimSpace(resolution.Role)
	resolution.Content = strings.TrimSpace(resolution.Content)
	if resolution.RequestID == "" || resolution.Phase == "" || resolution.Role != "user" || resolution.Content == "" {
		return nil, false, fmt.Errorf("request ID, phase, user role, and content are required")
	}
	metadata := map[string]json.RawMessage{}
	if len(resolution.Metadata) > 0 {
		if err := json.Unmarshal(resolution.Metadata, &metadata); err != nil {
			return nil, false, fmt.Errorf("decoding pending input response metadata: %w", err)
		}
	}
	var deliveryID string
	if raw := metadata["delivery_id"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &deliveryID)
	}
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return nil, false, fmt.Errorf("pending input response delivery_id is required")
	}
	metadata["overseer_held"] = json.RawMessage(`true`)
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return nil, false, fmt.Errorf("encoding pending input response metadata: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning pending input response transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentRequestID string
	if err := tx.QueryRow(ctx, `SELECT pending_request_id FROM agent_sessions WHERE id = $1 FOR UPDATE`, sessionID).Scan(&currentRequestID); err != nil {
		return nil, false, fmt.Errorf("locking pending input request: %w", err)
	}
	if currentRequestID != resolution.RequestID {
		var existing sqlc.ConversationMessage
		err := tx.QueryRow(ctx, `
			SELECT id, session_id, role, content, metadata, created_at
			FROM conversation_messages
			WHERE session_id = $1 AND metadata->>'delivery_id' = $2
			ORDER BY id DESC LIMIT 1`, sessionID, deliveryID).Scan(
			&existing.ID, &existing.SessionID, &existing.Role, &existing.Content, &existing.Metadata, &existing.CreatedAt,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if err := tx.Commit(ctx); err != nil {
					return nil, false, fmt.Errorf("committing stale pending input check: %w", err)
				}
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("checking prior pending input delivery: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("committing replayed pending input check: %w", err)
		}
		return messageFromRow(existing), true, nil
	}

	var inserted sqlc.ConversationMessage
	if err := tx.QueryRow(ctx, `
		INSERT INTO conversation_messages (session_id, role, content, metadata)
		VALUES ($1, 'user', $2, $3)
		RETURNING id, session_id, role, content, metadata, created_at`,
		sessionID, resolution.Content, encodedMetadata,
	).Scan(&inserted.ID, &inserted.SessionID, &inserted.Role, &inserted.Content, &inserted.Metadata, &inserted.CreatedAt); err != nil {
		return nil, false, fmt.Errorf("reserving pending input response message: %w", err)
	}
	result, err := tx.Exec(ctx, `
		UPDATE agent_sessions
		SET pending_question = '', pending_actions = '[]', pending_input_type = '', pending_request_id = '', phase = $3
		WHERE id = $1 AND pending_request_id = $2`, sessionID, resolution.RequestID, resolution.Phase)
	if err != nil {
		return nil, false, fmt.Errorf("consuming pending input request: %w", err)
	}
	if result.RowsAffected() != 1 {
		return nil, false, fmt.Errorf("pending input request changed while locked")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing pending input response: %w", err)
	}
	return messageFromRow(inserted), true, nil
}

// ReleasePendingInputResponse makes a previously reserved response visible to
// the agent loop after all controller-side mode or policy effects are durable.
func (s *Store) ReleasePendingInputResponse(ctx context.Context, sessionID uuid.UUID, messageID int64, deliveryID string) error {
	deliveryID = strings.TrimSpace(deliveryID)
	if messageID <= 0 || deliveryID == "" {
		return fmt.Errorf("message ID and delivery ID are required")
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE conversation_messages
		SET metadata = COALESCE(metadata, '{}'::jsonb) - 'overseer_held'
		WHERE id = $1 AND session_id = $2 AND metadata->>'delivery_id' = $3`,
		messageID, sessionID, deliveryID)
	if err != nil {
		return fmt.Errorf("releasing pending input response: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("reserved pending input response not found")
	}
	return nil
}

// CancelPendingInputResponse permanently suppresses a reserved response when
// the supervised run is cancelled before controller effects are applied.
func (s *Store) CancelPendingInputResponse(ctx context.Context, sessionID uuid.UUID, messageID int64, deliveryID string) error {
	deliveryID = strings.TrimSpace(deliveryID)
	if messageID <= 0 || deliveryID == "" {
		return fmt.Errorf("message ID and delivery ID are required")
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE conversation_messages
		SET delivery_state = 'cancelled',
		    metadata = (COALESCE(metadata, '{}'::jsonb) - 'overseer_held') ||
			jsonb_build_object('cancelled_at_unix', extract(epoch FROM now())::bigint)
		WHERE id = $1 AND session_id = $2 AND metadata->>'delivery_id' = $3`,
		messageID, sessionID, deliveryID)
	if err != nil {
		return fmt.Errorf("cancelling pending input response: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("reserved pending input response not found")
	}
	return nil
}

// ClearPendingInputIfID clears only the exact request nonce. A replacement
// request remains untouched.
func (s *Store) ClearPendingInputIfID(ctx context.Context, sessionID uuid.UUID, requestID, phase string) (bool, error) {
	requestID = strings.TrimSpace(requestID)
	phase = strings.TrimSpace(phase)
	if requestID == "" || phase == "" {
		return false, fmt.Errorf("request ID and phase are required")
	}
	result, err := s.pool.Exec(ctx, `
		UPDATE agent_sessions
		SET pending_question = '', pending_actions = '[]', pending_input_type = '', pending_request_id = '', phase = $3
		WHERE id = $1 AND pending_request_id = $2`, sessionID, requestID, phase)
	if err != nil {
		return false, fmt.Errorf("clearing exact pending input request: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

func (s *Store) UpdateMetadata(ctx context.Context, id uuid.UUID, metadata json.RawMessage) error {
	return s.queries.UpdateSessionMetadata(ctx, sqlc.UpdateSessionMetadataParams{
		ID:       id,
		Metadata: metadata,
	})
}

func (s *Store) MergeSessionMetadata(ctx context.Context, id uuid.UUID, key string, value json.RawMessage) error {
	patch, err := json.Marshal(map[string]json.RawMessage{key: value})
	if err != nil {
		return fmt.Errorf("encoding metadata patch: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE agent_sessions
		SET metadata = COALESCE(metadata, '{}'::jsonb) || $2::jsonb,
		    updated_at = now()
		WHERE id = $1`,
		id, patch)
	if err != nil {
		return fmt.Errorf("merging session metadata: %w", err)
	}
	return nil
}

func (s *Store) AppendInterrupt(ctx context.Context, sessionID uuid.UUID, requestedBy string) (int64, time.Time, error) {
	var id int64
	var requestedAt time.Time
	err := s.pool.QueryRow(ctx, `
		INSERT INTO session_interrupts (session_id, requested_by)
		VALUES ($1, $2)
		RETURNING id, requested_at`, sessionID, strings.TrimSpace(requestedBy)).Scan(&id, &requestedAt)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("appending interrupt: %w", err)
	}
	return id, requestedAt, nil
}

func (s *Store) PeekInterrupt(ctx context.Context, sessionID uuid.UUID) (int64, time.Time, string, bool, error) {
	var id int64
	var requestedAt time.Time
	var requestedBy string
	err := s.pool.QueryRow(ctx, `
		SELECT id, requested_at, requested_by
		FROM session_interrupts
		WHERE session_id = $1 AND consumed_at IS NULL
		ORDER BY id ASC LIMIT 1`, sessionID).Scan(&id, &requestedAt, &requestedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, time.Time{}, "", false, nil
	}
	if err != nil {
		return 0, time.Time{}, "", false, fmt.Errorf("peeking interrupt: %w", err)
	}
	return id, requestedAt, requestedBy, true, nil
}

func (s *Store) ConsumeInterrupt(ctx context.Context, sessionID uuid.UUID) (int64, time.Time, string, bool, error) {
	var id int64
	var requestedAt time.Time
	var requestedBy string
	err := s.pool.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM session_interrupts
			WHERE session_id = $1 AND consumed_at IS NULL
			ORDER BY id ASC FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE session_interrupts AS interrupt
		SET consumed_at = now()
		FROM next
		WHERE interrupt.id = next.id
		RETURNING interrupt.id, interrupt.requested_at, interrupt.requested_by`, sessionID).Scan(&id, &requestedAt, &requestedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, time.Time{}, "", false, nil
	}
	if err != nil {
		return 0, time.Time{}, "", false, fmt.Errorf("consuming interrupt: %w", err)
	}
	return id, requestedAt, requestedBy, true, nil
}

func (s *Store) ReserveWakeIntent(ctx context.Context, sessionID uuid.UUID, idempotencyKey, content string, targetWakeRequests int64) (*store.Message, int64, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	content = strings.TrimSpace(content)
	if idempotencyKey == "" || content == "" || targetWakeRequests <= 0 {
		return nil, 0, false, fmt.Errorf("wake idempotency key, content, and positive target are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, false, fmt.Errorf("beginning wake intent: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, sessionID.String()); err != nil {
		return nil, 0, false, fmt.Errorf("locking wake intent: %w", err)
	}

	var existingID int64
	var existingTarget int64
	err = tx.QueryRow(ctx, `SELECT message_id, target_wake_requests FROM agent_run_wake_intents WHERE session_id = $1 AND idempotency_key = $2`, sessionID, idempotencyKey).Scan(&existingID, &existingTarget)
	if err == nil {
		row, rowErr := tx.Query(ctx, `SELECT * FROM conversation_messages WHERE id = $1`, existingID)
		if rowErr != nil {
			return nil, 0, false, rowErr
		}
		defer row.Close()
		if !row.Next() {
			return nil, 0, false, fmt.Errorf("wake message %d not found", existingID)
		}
		var model sqlc.ConversationMessage
		if scanErr := row.Scan(&model.ID, &model.SessionID, &model.Role, &model.Content, &model.Metadata, &model.CreatedAt, &model.DeliveryState, &model.ClaimedAt, &model.DeliverySequence, &model.ClaimToken); scanErr != nil {
			return nil, 0, false, scanErr
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, 0, false, err
		}
		return messageFromRow(model), existingTarget, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, false, fmt.Errorf("checking wake intent: %w", err)
	}
	var durableMax int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(target_wake_requests), 0) FROM agent_run_wake_intents WHERE session_id = $1`, sessionID).Scan(&durableMax); err != nil {
		return nil, 0, false, fmt.Errorf("reading durable wake target: %w", err)
	}
	if durableMax >= targetWakeRequests {
		targetWakeRequests = durableMax + 1
	}
	metadata, _ := json.Marshal(map[string]string{"wake_idempotency_key": idempotencyKey})
	var messageID int64
	err = tx.QueryRow(ctx, `INSERT INTO conversation_messages (session_id, role, content, metadata) VALUES ($1, 'user', $2, $3) RETURNING id`, sessionID, content, metadata).Scan(&messageID)
	if err != nil {
		return nil, 0, false, fmt.Errorf("appending wake message: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_run_wake_intents (session_id, idempotency_key, message_id, target_wake_requests) VALUES ($1, $2, $3, $4)`, sessionID, idempotencyKey, messageID, targetWakeRequests)
	if err != nil {
		return nil, 0, false, fmt.Errorf("recording wake intent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, false, fmt.Errorf("committing wake intent: %w", err)
	}
	messages, err := s.GetMessages(ctx, sessionID)
	if err != nil {
		return nil, 0, false, err
	}
	for i := range messages {
		if messages[i].ID == messageID {
			return &messages[i], targetWakeRequests, true, nil
		}
	}
	return nil, 0, false, fmt.Errorf("created wake message %d not found", messageID)
}

func (s *Store) MarkWakeIntentApplied(ctx context.Context, sessionID uuid.UUID, idempotencyKey string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_run_wake_intents SET applied_at = COALESCE(applied_at, now()) WHERE session_id = $1 AND idempotency_key = $2`, sessionID, strings.TrimSpace(idempotencyKey))
	if err != nil {
		return fmt.Errorf("marking wake intent applied: %w", err)
	}
	return nil
}

func (s *Store) ListAllSessionMetrics(ctx context.Context) ([]store.SessionMetricsEntry, error) {
	rows, err := s.queries.ListAllSessionMetrics(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing session metrics: %w", err)
	}

	type metricsEnvelope struct {
		Metrics struct {
			CostUSD       float64 `json:"cost_usd"`
			InputTokens   int64   `json:"input_tokens"`
			OutputTokens  int64   `json:"output_tokens"`
			ToolCallCount int32   `json:"tool_call_count"`
		} `json:"metrics"`
	}

	var entries []store.SessionMetricsEntry
	for _, row := range rows {
		var env metricsEnvelope
		if err := json.Unmarshal(row.Metadata, &env); err != nil || env.Metrics.CostUSD == 0 && env.Metrics.InputTokens == 0 {
			continue
		}
		entries = append(entries, store.SessionMetricsEntry{
			AgentRunName:  row.AgentrunName,
			AgentRunNS:    row.AgentrunNs,
			CostUSD:       env.Metrics.CostUSD,
			InputTokens:   env.Metrics.InputTokens,
			OutputTokens:  env.Metrics.OutputTokens,
			ToolCallCount: env.Metrics.ToolCallCount,
		})
	}
	return entries, nil
}

// DeleteAgentRunData removes all Postgres state scoped to one AgentRun. The
// agent_sessions delete cascades conversation_messages, activity_events,
// agent_artifacts, and session_transcripts; the rest are non-FK tables that
// refer to the run by namespace/name.
func (s *Store) DeleteAgentRunData(ctx context.Context, agentRunName, agentRunNS, projectID string) error {
	agentRunName = strings.TrimSpace(agentRunName)
	agentRunNS = strings.TrimSpace(agentRunNS)
	projectID = strings.TrimSpace(projectID)
	if agentRunName == "" || agentRunNS == "" {
		return fmt.Errorf("agent run namespace and name are required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning AgentRun data cleanup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	statements := []struct {
		description string
		table       string
		sql         string
		args        []any
	}{
		{
			description: "agent session state",
			table:       "agent_sessions",
			sql:         `DELETE FROM agent_sessions WHERE agentrun_name = $1 AND agentrun_ns = $2`,
			args:        []any{agentRunName, agentRunNS},
		},
		{
			description: "agent run ownership",
			table:       "resource_ownership",
			sql:         `DELETE FROM resource_ownership WHERE resource_type = 'agent_run' AND resource_id = $1 AND resource_namespace = $2`,
			args:        []any{agentRunName, agentRunNS},
		},
		{
			description: "agent run shares",
			table:       "resource_shares",
			sql:         `DELETE FROM resource_shares WHERE resource_type = 'agent_run' AND resource_id = $1 AND resource_namespace = $2`,
			args:        []any{agentRunName, agentRunNS},
		},
		{
			description: "agent run notifications",
			table:       "notifications",
			sql:         `DELETE FROM notifications WHERE resource_type = 'agent_run' AND resource_id = $1 AND resource_namespace = $2`,
			args:        []any{agentRunName, agentRunNS},
		},
		{
			description: "Slack thread mapping",
			table:       "slack_threads",
			sql:         `DELETE FROM slack_threads WHERE run_name = $1 AND run_namespace = $2`,
			args:        []any{agentRunName, agentRunNS},
		},
		{
			description: "Slack run drafts",
			table:       "slack_drafts",
			sql:         `DELETE FROM slack_drafts WHERE run_name = $1 AND namespace = $2`,
			args:        []any{agentRunName, agentRunNS},
		},
		{
			description: "legacy agent memories",
			table:       "agent_memories",
			sql:         `DELETE FROM agent_memories WHERE source_run = $1 AND namespace = $2`,
			args:        []any{agentRunName, agentRunNS},
		},
		{
			description: "project state tasks",
			table:       "project_state_tasks",
			sql:         `DELETE FROM project_state_tasks WHERE source_run = $1 AND ($2 = '' OR project_id = $2)`,
			args:        []any{agentRunName, projectID},
		},
		{
			description: "project state memories",
			table:       "project_state_memories",
			sql:         `DELETE FROM project_state_memories WHERE source_run = $1 AND ($2 = '' OR project_id = $2)`,
			args:        []any{agentRunName, projectID},
		},
		{
			description: "project state session summaries",
			table:       "project_state_session_summaries",
			sql:         `DELETE FROM project_state_session_summaries WHERE run_id = $1 AND ($2 = '' OR project_id = $2)`,
			args:        []any{agentRunName, projectID},
		},
	}

	for _, stmt := range statements {
		exists, err := tableExists(ctx, tx, stmt.table)
		if err != nil {
			return fmt.Errorf("checking %s table for AgentRun %s/%s cleanup: %w", stmt.table, agentRunNS, agentRunName, err)
		}
		if !exists {
			continue
		}
		if _, err := tx.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			return fmt.Errorf("deleting %s for AgentRun %s/%s: %w", stmt.description, agentRunNS, agentRunName, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing AgentRun data cleanup: %w", err)
	}
	return nil
}

func tableExists(ctx context.Context, tx pgx.Tx, table string) (bool, error) {
	var regclass sql.NullString
	if err := tx.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&regclass); err != nil {
		return false, err
	}
	return regclass.Valid && regclass.String != "", nil
}

// --- Conversation ---

func (s *Store) AppendMessage(ctx context.Context, sessionID uuid.UUID, role, content string, metadata json.RawMessage) (*store.Message, error) {
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	row, err := s.queries.AppendMessage(ctx, sqlc.AppendMessageParams{
		SessionID: sessionID,
		Role:      role,
		Content:   content,
		Metadata:  metadata,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, store.ErrMessageAlreadyExists
		}
		return nil, fmt.Errorf("appending message: %w", err)
	}
	return messageFromRow(row), nil
}

func (s *Store) GetMessages(ctx context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	rows, err := s.queries.GetMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting messages: %w", err)
	}
	return messagesFromRows(rows), nil
}

func (s *Store) GetMessagesIncludingCancelled(ctx context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	rows, err := s.queries.GetMessagesIncludingCancelled(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting messages including cancelled: %w", err)
	}
	return messagesFromRows(rows), nil
}

func (s *Store) GetMessagesSince(ctx context.Context, sessionID uuid.UUID, afterID int64) ([]store.Message, error) {
	rows, err := s.queries.GetMessagesSince(ctx, sqlc.GetMessagesSinceParams{
		SessionID: sessionID,
		ID:        afterID,
	})
	if err != nil {
		return nil, fmt.Errorf("getting messages since: %w", err)
	}
	return messagesFromRows(rows), nil
}

func (s *Store) PollNewUserMessages(ctx context.Context, sessionID uuid.UUID, afterID int64) ([]store.Message, error) {
	rows, err := s.queries.PollNewUserMessages(ctx, sqlc.PollNewUserMessagesParams{
		SessionID: sessionID,
		AfterID:   afterID,
	})
	if err != nil {
		return nil, fmt.Errorf("polling new user messages: %w", err)
	}
	return messagesFromRows(rows), nil
}

// ClaimUserMessage is the authoritative pickup operation. Cancellation and
// claim race on the same delivery_state predicate, so exactly one can win.
func (s *Store) ClaimUserMessage(ctx context.Context, sessionID uuid.UUID, messageID int64, claimToken uuid.UUID) (*store.Message, bool, error) {
	var msg store.Message
	var claimedAt time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE conversation_messages
		SET delivery_state = 'claimed',
		    claimed_at = now(),
		    delivery_sequence = nextval('conversation_delivery_sequence'),
		    claim_token = $3,
		    metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{delivered_at_unix}',
		        to_jsonb(extract(epoch FROM now())::bigint), true)
		WHERE session_id = $1 AND id = $2 AND role = 'user'
		  AND delivery_state = 'pending'
		RETURNING id, session_id, role, content, metadata, delivery_state,
		          delivery_sequence, claimed_at, created_at`, sessionID, messageID, claimToken).Scan(
		&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &msg.Metadata,
		&msg.DeliveryState, &msg.DeliverySequence, &claimedAt, &msg.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("claiming user message: %w", err)
	}
	msg.ClaimedAt = &claimedAt
	return &msg, true, nil
}

func (s *Store) AppendAssistantAndCompleteClaims(ctx context.Context, sessionID uuid.UUID, claimToken uuid.UUID, content string) (*store.Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var row sqlc.ConversationMessage
	err = tx.QueryRow(ctx, `INSERT INTO conversation_messages (session_id, role, content) VALUES ($1, 'assistant', $2) RETURNING *`, sessionID, content).Scan(
		&row.ID, &row.SessionID, &row.Role, &row.Content, &row.Metadata, &row.CreatedAt, &row.DeliveryState, &row.ClaimedAt, &row.DeliverySequence, &row.ClaimToken)
	if err != nil {
		return nil, fmt.Errorf("appending assistant response: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE conversation_messages SET delivery_state = 'completed', claim_token = NULL WHERE session_id = $1 AND role = 'user' AND delivery_state = 'claimed' AND claim_token = $2`, sessionID, claimToken); err != nil {
		return nil, fmt.Errorf("completing claimed messages: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return messageFromRow(row), nil
}

func (s *Store) CompleteClaims(ctx context.Context, sessionID uuid.UUID, claimToken uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE conversation_messages SET delivery_state = 'completed', claim_token = NULL WHERE session_id = $1 AND role = 'user' AND delivery_state = 'claimed' AND claim_token = $2`, sessionID, claimToken)
	if err != nil {
		return fmt.Errorf("completing claims: %w", err)
	}
	return nil
}

func (s *Store) RecoverClaimedUserMessages(ctx context.Context, sessionID uuid.UUID, activeClaimToken uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE conversation_messages
		SET delivery_state = 'pending', claimed_at = NULL, claim_token = NULL,
		    delivery_sequence = NULL, metadata = COALESCE(metadata, '{}'::jsonb) - 'delivered_at_unix'
		WHERE session_id = $1 AND role = 'user' AND delivery_state = 'claimed'
		  AND claim_token IS DISTINCT FROM $2`, sessionID, activeClaimToken)
	if err != nil {
		return fmt.Errorf("recovering claimed user messages: %w", err)
	}
	return nil
}

func (s *Store) MarkMessagesDelivered(ctx context.Context, sessionID uuid.UUID, messageIDs []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	// Cancelled messages are excluded: a message withdrawn by the user must
	// never surface as delivered, even if the agent loop raced the
	// cancellation and still tries to stamp it.
	_, err := s.pool.Exec(ctx, `
		UPDATE conversation_messages
		SET delivery_state = CASE WHEN delivery_state = 'pending' THEN 'claimed' ELSE delivery_state END,
		    claimed_at = COALESCE(claimed_at, now()),
		    delivery_sequence = COALESCE(delivery_sequence, nextval('conversation_delivery_sequence')),
		    metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{delivered_at_unix}',
			to_jsonb(extract(epoch FROM now())::bigint), true)
		WHERE session_id = $1 AND id = ANY($2) AND role = 'user'
			AND delivery_state IN ('pending', 'claimed', 'completed')`,
		sessionID, messageIDs)
	if err != nil {
		return fmt.Errorf("marking messages delivered: %w", err)
	}
	return nil
}

// CancelUndeliveredUserMessage stamps cancelled_at_unix into a queued or
// steering user message's metadata, atomically guarding against the agent
// loop having already consumed it. Cancelled messages disappear from delivery
// polls (PollNewUserMessages) and from the rendered conversation.
func (s *Store) CancelUndeliveredUserMessage(ctx context.Context, sessionID uuid.UUID, messageID int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE conversation_messages
		SET delivery_state = 'cancelled',
		    metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{cancelled_at_unix}',
			to_jsonb(extract(epoch FROM now())::bigint), true)
		WHERE session_id = $1 AND id = $2 AND role = 'user'
			AND delivery_state = 'pending'`,
		sessionID, messageID)
	if err != nil {
		return fmt.Errorf("cancelling user message: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// Nothing updated — figure out why so the caller can report it.
	var delivered bool
	err = s.pool.QueryRow(ctx, `
		SELECT delivery_state IN ('claimed', 'completed')
		FROM conversation_messages
		WHERE session_id = $1 AND id = $2 AND role = 'user'
			AND delivery_state <> 'cancelled'`,
		sessionID, messageID).Scan(&delivered)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrMessageNotFound
	}
	if err != nil {
		return fmt.Errorf("checking user message state: %w", err)
	}
	if delivered {
		return store.ErrMessageDelivered
	}
	return store.ErrMessageNotFound
}

// --- Transcript snapshot ---

func (s *Store) UpsertSessionTranscript(ctx context.Context, sessionID uuid.UUID, data []byte, itemCount int32) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO session_transcripts (session_id, data, item_count, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (session_id)
		DO UPDATE SET data = EXCLUDED.data, item_count = EXCLUDED.item_count, updated_at = now()`,
		sessionID, data, itemCount)
	if err != nil {
		return fmt.Errorf("upserting session transcript: %w", err)
	}
	return nil
}

func (s *Store) GetSessionTranscript(ctx context.Context, sessionID uuid.UUID) ([]byte, error) {
	var data []byte
	err := s.pool.QueryRow(ctx,
		`SELECT data FROM session_transcripts WHERE session_id = $1`, sessionID).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading session transcript: %w", err)
	}
	return data, nil
}

func (s *Store) DeleteSessionTranscript(ctx context.Context, sessionID uuid.UUID) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM session_transcripts WHERE session_id = $1`, sessionID); err != nil {
		return fmt.Errorf("deleting session transcript: %w", err)
	}
	return nil
}

// --- Activity ---

func (s *Store) WriteActivityEvent(ctx context.Context, sessionID uuid.UUID, eventType, summary string, detail json.RawMessage) (*store.ActivityEvent, error) {
	if detail == nil {
		detail = json.RawMessage(`{}`)
	}
	row, err := s.queries.WriteActivityEvent(ctx, sqlc.WriteActivityEventParams{
		SessionID: sessionID,
		EventType: eventType,
		Summary:   summary,
		Detail:    detail,
	})
	if err != nil {
		return nil, fmt.Errorf("writing activity event: %w", err)
	}
	return activityEventFromRow(row), nil
}

// GetLatestActivityBySessions returns the newest event for each requested
// session in one query. List surfaces use this to show fleet activity without
// issuing one GetRecentActivity query per AgentRun.
//
// Each session resolves with a single backward descent of the
// (session_id, id) index instead of a DISTINCT ON sort over every event of
// every listed session. Events are append-only, so max id == newest, matching
// the id-based cursors used by summary versions and delta streams.
func (s *Store) GetLatestActivityBySessions(ctx context.Context, sessionIDs []uuid.UUID) (map[uuid.UUID]store.ActivityEvent, error) {
	out := make(map[uuid.UUID]store.ActivityEvent)
	if len(sessionIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
SELECT e.id, e.session_id, e.event_type, e.summary, e.detail, e.created_at
FROM unnest($1::uuid[]) AS sid(id)
CROSS JOIN LATERAL (
    SELECT id, session_id, event_type, summary, detail, created_at
    FROM activity_events
    WHERE session_id = sid.id
    ORDER BY id DESC
    LIMIT 1
) e`, sessionIDs)
	if err != nil {
		return nil, fmt.Errorf("getting latest activity by sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var event store.ActivityEvent
		if err := rows.Scan(
			&event.ID,
			&event.SessionID,
			&event.EventType,
			&event.Summary,
			&event.Detail,
			&event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning latest activity by sessions: %w", err)
		}
		out[event.SessionID] = event
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading latest activity by sessions: %w", err)
	}
	return out, nil
}

// GetLatestActivityEventID returns the newest activity event ID for a
// session, or 0 when the session has no events. Watch probes call this
// several times per second; with the (session_id, id) index it is a single
// index-only descent that never fetches event payloads, unlike loading the
// newest full event row whose detail JSONB can be large.
func (s *Store) GetLatestActivityEventID(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM activity_events WHERE session_id = $1`,
		sessionID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("getting latest activity event id: %w", err)
	}
	return id, nil
}

func (s *Store) GetRecentActivity(ctx context.Context, sessionID uuid.UUID, limit int32) ([]store.ActivityEvent, error) {
	rows, err := s.queries.GetRecentActivityEvents(ctx, sqlc.GetRecentActivityEventsParams{
		SessionID: sessionID,
		Limit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("getting recent activity: %w", err)
	}
	out := make([]store.ActivityEvent, len(rows))
	for i, r := range rows {
		out[i] = store.ActivityEvent{
			ID:        r.ID,
			SessionID: r.SessionID,
			EventType: r.EventType,
			Summary:   r.Summary,
			Detail:    r.Detail,
			CreatedAt: r.CreatedAt,
		}
	}
	return out, nil
}

// GetRecentErrorActivity returns the newest durable error events without first
// loading unrelated activity. The JSON predicates mirror the dashboard's
// ContentEvent error classification and keep recovered failures queryable even
// after a long successful run.
func (s *Store) GetRecentErrorActivity(ctx context.Context, sessionID uuid.UUID, limit int32) ([]store.ActivityEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, session_id, event_type, summary, detail, created_at
		FROM activity_events
		WHERE session_id = $1
		  AND (
			detail @> '{"is_error": true}'::jsonb
			OR COALESCE(detail->>'failure_kind', '') <> ''
			OR LOWER(COALESCE(detail->>'attempt_status', detail->>'status', '')) IN ('error', 'failed', 'failure', 'fatal')
			OR LOWER(event_type) IN ('error', 'failed', 'failure', 'fatal', 'runtime_error')
		  )
		ORDER BY created_at DESC, id DESC
		LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("getting recent error activity: %w", err)
	}
	defer rows.Close()

	out := make([]store.ActivityEvent, 0, limit)
	for rows.Next() {
		var event store.ActivityEvent
		if err := rows.Scan(&event.ID, &event.SessionID, &event.EventType, &event.Summary, &event.Detail, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning recent error activity: %w", err)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading recent error activity: %w", err)
	}
	return out, nil
}

func (s *Store) GetAllActivity(ctx context.Context, sessionID uuid.UUID) ([]store.ActivityEvent, error) {
	rows, err := s.queries.GetActivityEvents(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting all activity: %w", err)
	}
	out := make([]store.ActivityEvent, len(rows))
	for i, r := range rows {
		out[i] = store.ActivityEvent{
			ID:        r.ID,
			SessionID: r.SessionID,
			EventType: r.EventType,
			Summary:   r.Summary,
			Detail:    r.Detail,
			CreatedAt: r.CreatedAt,
		}
	}
	return out, nil
}

func (s *Store) GetActivityEventsSince(ctx context.Context, sessionID uuid.UUID, afterID int64) ([]store.ActivityEvent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, session_id, event_type, summary, detail, created_at
		 FROM activity_events
		 WHERE session_id = $1 AND id > $2
		 ORDER BY id ASC`,
		sessionID, afterID)
	if err != nil {
		return nil, fmt.Errorf("getting activity events since %d: %w", afterID, err)
	}
	defer rows.Close()
	var out []store.ActivityEvent
	for rows.Next() {
		var ev store.ActivityEvent
		if err := rows.Scan(&ev.ID, &ev.SessionID, &ev.EventType, &ev.Summary, &ev.Detail, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning activity event: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("getting activity events since %d: %w", afterID, err)
	}
	return out, nil
}

// GetAgentRunSummaryVersions returns a cheap version for every session in a
// namespace. Fleet watches combine it with the AgentRun resourceVersion so
// pending-input, usage-metadata, and latest-activity changes can update list
// rows even when the Kubernetes object itself did not change.
//
// This runs every watch tick, so it must stay index-only: the MAX(id)
// subquery descends the (session_id, id) index once per session, and the
// namespace filter is an explicit SQL branch because the
// `$1 = ” OR agentrun_ns = $1` form would force a plan that can never use
// the agentrun_ns index.
func (s *Store) GetAgentRunSummaryVersions(ctx context.Context, namespace string) (map[string]string, error) {
	query := `
SELECT s.agentrun_ns,
       s.agentrun_name,
       s.updated_at,
       (SELECT COALESCE(MAX(id), 0) FROM activity_events WHERE session_id = s.id)
FROM agent_sessions s`
	var args []any
	if namespace != "" {
		query += ` WHERE s.agentrun_ns = $1`
		args = append(args, namespace)
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting AgentRun summary versions: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var runNamespace, runName string
		var updatedAt time.Time
		var lastEventID int64
		if err := rows.Scan(&runNamespace, &runName, &updatedAt, &lastEventID); err != nil {
			return nil, fmt.Errorf("scanning AgentRun summary version: %w", err)
		}
		out[runNamespace+"/"+runName] = fmt.Sprintf("%d|%d", updatedAt.UnixNano(), lastEventID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading AgentRun summary versions: %w", err)
	}
	return out, nil
}

func (s *Store) GetSessionFingerprint(ctx context.Context, sessionID uuid.UUID) (string, error) {
	var (
		lastMessageID     int64
		deliveredCount    int64
		lastDeliveredAt   int64
		cancelledCount    int64
		lastEventID       int64
		planUpdatedAt     time.Time
		sessionUpdatedAt  time.Time
		pendingInputType  string
		pendingActionsLen int32
	)
	// The conversation component tracks MAX(id) plus the delivered-message
	// metadata: MarkMessagesDelivered mutates conversation_messages.metadata
	// in place (no new row, no agent_sessions touch), and watchers must still
	// observe queued messages flipping to delivered. Cancellations likewise
	// only touch metadata, so the cancelled count is part of the fingerprint.
	//
	// This runs every watch tick (sub-second), so the message aggregates are
	// computed in one pass over the session's messages (FILTER clauses)
	// instead of four separate correlated subqueries, and the activity MAX(id)
	// descends the (session_id, id) index.
	err := s.pool.QueryRow(ctx,
		`SELECT
			m.last_id,
			m.delivered_count,
			m.last_delivered_at,
			m.cancelled_count,
			(SELECT COALESCE(MAX(id), 0) FROM activity_events WHERE session_id = s.id),
			(SELECT COALESCE(MAX(updated_at), 'epoch'::timestamptz) FROM agent_artifacts WHERE session_id = s.id AND kind = 'plan'),
			s.updated_at,
			s.pending_input_type,
			LENGTH(s.pending_actions::text)
		 FROM agent_sessions s
		 CROSS JOIN LATERAL (
			SELECT
				COALESCE(MAX(id), 0) AS last_id,
				COUNT(*) FILTER (WHERE metadata ? 'delivered_at_unix') AS delivered_count,
				COALESCE(MAX((metadata->>'delivered_at_unix')::bigint) FILTER (WHERE metadata ? 'delivered_at_unix'), 0) AS last_delivered_at,
				COUNT(*) FILTER (WHERE metadata ? 'cancelled_at_unix') AS cancelled_count
			FROM conversation_messages
			WHERE session_id = s.id
		 ) m
		 WHERE s.id = $1`,
		sessionID).Scan(&lastMessageID, &deliveredCount, &lastDeliveredAt, &cancelledCount, &lastEventID, &planUpdatedAt, &sessionUpdatedAt, &pendingInputType, &pendingActionsLen)
	if err != nil {
		return "", fmt.Errorf("getting session fingerprint: %w", err)
	}
	return fmt.Sprintf("%d|%d|%d|%d|%d|%d|%d|%s|%d",
		lastMessageID, deliveredCount, lastDeliveredAt, cancelledCount, lastEventID, planUpdatedAt.UnixNano(), sessionUpdatedAt.UnixNano(), pendingInputType, pendingActionsLen), nil
}

// --- Artifacts ---

func (s *Store) UpsertArtifact(ctx context.Context, sessionID uuid.UUID, kind, content, s3URL, contentHash string, metadata json.RawMessage) (*store.Artifact, error) {
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	row, err := s.queries.UpsertArtifact(ctx, sqlc.UpsertArtifactParams{
		SessionID:   sessionID,
		Kind:        kind,
		Content:     content,
		S3Url:       s3URL,
		ContentHash: contentHash,
		Metadata:    metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("upserting artifact: %w", err)
	}
	return artifactFromRow(row), nil
}

func (s *Store) GetArtifact(ctx context.Context, sessionID uuid.UUID, kind string) (*store.Artifact, error) {
	row, err := s.queries.GetArtifact(ctx, sqlc.GetArtifactParams{
		SessionID: sessionID,
		Kind:      kind,
	})
	if err != nil {
		return nil, fmt.Errorf("getting artifact: %w", err)
	}
	return artifactFromRow(row), nil
}

func (s *Store) GetArtifacts(ctx context.Context, sessionID uuid.UUID) ([]store.Artifact, error) {
	rows, err := s.queries.GetArtifacts(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting artifacts: %w", err)
	}
	out := make([]store.Artifact, len(rows))
	for i, r := range rows {
		out[i] = *artifactFromRow(r)
	}
	return out, nil
}

// ListSlackDrafts returns reply drafts for a SlackAgent within a namespace,
// newest first. An empty status returns all statuses; a non-positive limit
// defaults to 50.
func (s *Store) ListSlackDrafts(ctx context.Context, namespace, slackAgent, status string, limit int32) ([]store.SlackDraft, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.queries.ListSlackDraftsByAgent(ctx, sqlc.ListSlackDraftsByAgentParams{
		Namespace:  namespace,
		SlackAgent: slackAgent,
		Status:     status,
		MaxRows:    limit,
	})
	if err != nil {
		return nil, fmt.Errorf("listing slack drafts: %w", err)
	}
	out := make([]store.SlackDraft, len(rows))
	for i, r := range rows {
		out[i] = *slackDraftFromRow(r)
	}
	return out, nil
}

// --- Row conversion helpers ---

func sessionFromRow(r sqlc.AgentSession) *store.Session {
	return &store.Session{
		ID:               r.ID,
		AgentRunName:     r.AgentrunName,
		AgentRunNS:       r.AgentrunNs,
		Phase:            r.Phase,
		CurrentStep:      r.CurrentStep,
		PendingQuestion:  r.PendingQuestion,
		PendingActions:   r.PendingActions,
		PendingInputType: r.PendingInputType,
		PendingRequestID: r.PendingRequestID,
		Metadata:         r.Metadata,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
}

func messageFromRow(r sqlc.ConversationMessage) *store.Message {
	msg := &store.Message{
		ID:            r.ID,
		SessionID:     r.SessionID,
		Role:          r.Role,
		Content:       r.Content,
		Metadata:      r.Metadata,
		DeliveryState: r.DeliveryState,
		CreatedAt:     r.CreatedAt,
	}
	if r.DeliverySequence.Valid {
		msg.DeliverySequence = r.DeliverySequence.Int64
	}
	if r.ClaimedAt.Valid {
		claimedAt := r.ClaimedAt.Time
		msg.ClaimedAt = &claimedAt
	}
	return msg
}

func messagesFromRows(rows []sqlc.ConversationMessage) []store.Message {
	out := make([]store.Message, len(rows))
	for i, r := range rows {
		out[i] = *messageFromRow(r)
	}
	return out
}

func activityEventFromRow(r sqlc.ActivityEvent) *store.ActivityEvent {
	return &store.ActivityEvent{
		ID:        r.ID,
		SessionID: r.SessionID,
		EventType: r.EventType,
		Summary:   r.Summary,
		Detail:    r.Detail,
		CreatedAt: r.CreatedAt,
	}
}

func artifactFromRow(r sqlc.AgentArtifact) *store.Artifact {
	return &store.Artifact{
		ID:          r.ID,
		SessionID:   r.SessionID,
		Kind:        r.Kind,
		Content:     r.Content,
		S3URL:       r.S3Url,
		ContentHash: r.ContentHash,
		Metadata:    r.Metadata,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

func slackDraftFromRow(r sqlc.SlackDraft) *store.SlackDraft {
	d := &store.SlackDraft{
		ID:           r.ID,
		SlackAgent:   r.SlackAgent,
		Namespace:    r.Namespace,
		Kind:         r.Kind,
		ChannelID:    r.ChannelID,
		ThreadTS:     r.ThreadTs,
		TargetUser:   r.TargetUser,
		IncomingText: r.IncomingText,
		DraftText:    r.DraftText,
		Status:       r.Status,
		CreatedAt:    r.CreatedAt,
	}
	if r.DecidedAt.Valid {
		t := r.DecidedAt.Time
		d.DecidedAt = &t
	}
	return d
}
