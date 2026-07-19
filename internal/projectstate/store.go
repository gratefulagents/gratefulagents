// Package projectstate provides a PostgreSQL-backed implementation of the
// agent SDK's projectstate.Store interface. The SDK owns the durable
// project-state model (tasks, memories, session summaries, context priming)
// and its tool surface; the operator only supplies this persistence layer.
package projectstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	sdkmemory "github.com/gratefulagents/sdk/pkg/agentsdk/memory"
	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store implements sdkprojectstate.Store on PostgreSQL. All rows are scoped
// by projectID so multiple projects share the same tables. The pool is owned
// by the caller and is not closed by Close.
type Store struct {
	pool      *pgxpool.Pool
	embedder  sdkmemory.Embedder
	projectID string
	actor     string
	runID     string
	workDir   string
}

// Options configures a Postgres project state store.
type Options struct {
	Pool *pgxpool.Pool
	// Embedder is optional; when nil, memory search falls back to lexical matching.
	Embedder  sdkmemory.Embedder
	ProjectID string
	Actor     string
	RunID     string
	WorkDir   string
}

var _ sdkprojectstate.Store = (*Store)(nil)

// NewStore creates a Postgres-backed project state store.
func NewStore(opts Options) (*Store, error) {
	if opts.Pool == nil {
		return nil, fmt.Errorf("postgres pool is required")
	}
	projectID := strings.TrimSpace(opts.ProjectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	return &Store{
		pool:      opts.Pool,
		embedder:  opts.Embedder,
		projectID: projectID,
		actor:     strings.TrimSpace(opts.Actor),
		runID:     strings.TrimSpace(opts.RunID),
		workDir:   strings.TrimSpace(opts.WorkDir),
	}, nil
}

// Close releases store resources. The pgx pool is owned by the caller.
func (s *Store) Close() error { return nil }

// --- TaskStore ---

const taskColumns = `id, title, description, type, status, priority, assignee, depends_on, labels, comments, source_run, metadata, created_at, updated_at, closed_at`

func (s *Store) CreateTask(ctx context.Context, in sdkprojectstate.CreateTaskInput) (*sdkprojectstate.Task, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	now := time.Now().UTC()
	task := sdkprojectstate.Task{
		ID:          newID("task"),
		Title:       title,
		Description: strings.TrimSpace(in.Description),
		Type:        normalizeTaskType(in.Type),
		Status:      sdkprojectstate.TaskStatusOpen,
		Priority:    normalizePriority(in.Priority),
		Assignee:    strings.TrimSpace(in.Assignee),
		DependsOn:   uniqueNonEmpty(in.DependsOn),
		Labels:      uniqueNonEmpty(in.Labels),
		CreatedAt:   now,
		UpdatedAt:   now,
		SourceRun:   firstNonEmpty(strings.TrimSpace(in.SourceRun), s.runID),
		Metadata:    in.Metadata,
	}
	if err := s.insertTask(ctx, task); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, task.ID)
}

func (s *Store) insertTask(ctx context.Context, task sdkprojectstate.Task) error {
	comments, err := marshalComments(task.Comments)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO project_state_tasks
			(project_id, id, title, description, type, status, priority, assignee, depends_on, labels, comments, source_run, metadata, created_at, updated_at, closed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		s.projectID, task.ID, task.Title, task.Description, task.Type, task.Status, task.Priority,
		task.Assignee, textArray(task.DependsOn), textArray(task.Labels), comments, task.SourceRun,
		nullableJSON(task.Metadata), task.CreatedAt, task.UpdatedAt, task.ClosedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting task: %w", err)
	}
	return nil
}

func (s *Store) UpdateTask(ctx context.Context, id string, patch sdkprojectstate.TaskPatch) (*sdkprojectstate.Task, error) {
	return s.mutateTask(ctx, id, func(task *sdkprojectstate.Task, now time.Time) error {
		applyPatch(task, patch, now)
		if strings.TrimSpace(task.Title) == "" {
			return fmt.Errorf("title is required")
		}
		return nil
	})
}

func (s *Store) ClaimTask(ctx context.Context, id, actor string) (*sdkprojectstate.Task, error) {
	return s.mutateTask(ctx, id, func(task *sdkprojectstate.Task, now time.Time) error {
		claimant := firstNonEmpty(strings.TrimSpace(actor), s.actor, "agent")
		task.Assignee = claimant
		task.Status = sdkprojectstate.TaskStatusInProgress
		task.UpdatedAt = now
		task.ClosedAt = nil
		return nil
	})
}

func (s *Store) CloseTask(ctx context.Context, id, reason string) (*sdkprojectstate.Task, error) {
	return s.mutateTask(ctx, id, func(task *sdkprojectstate.Task, now time.Time) error {
		task.Status = sdkprojectstate.TaskStatusClosed
		task.UpdatedAt = now
		closedAt := now
		task.ClosedAt = &closedAt
		if strings.TrimSpace(reason) != "" {
			task.Comments = append(task.Comments, sdkprojectstate.TaskComment{
				ID:        newID("comment"),
				Actor:     s.actor,
				Body:      "Closed: " + strings.TrimSpace(reason),
				CreatedAt: now,
			})
		}
		return nil
	})
}

func (s *Store) ReadyTasks(ctx context.Context, filter sdkprojectstate.TaskFilter) ([]sdkprojectstate.Task, error) {
	tasks, byID, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]sdkprojectstate.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Status != sdkprojectstate.TaskStatusOpen {
			continue
		}
		if !matchesLabels(task.Labels, filter.Labels) {
			continue
		}
		actor := firstNonEmpty(filter.Actor, filter.Assignee)
		if filter.Assignee != "" && task.Assignee != "" && task.Assignee != filter.Assignee {
			continue
		}
		if !filter.IncludeAssigned && task.Assignee != "" && task.Assignee != actor {
			continue
		}
		if hasOpenBlocker(byID, task) {
			continue
		}
		out = append(out, task)
	}
	sortTasks(out)
	return limitTasks(out, filter.Limit), nil
}

func (s *Store) ListTasks(ctx context.Context) ([]sdkprojectstate.Task, error) {
	tasks, _, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	sortTasks(tasks)
	return tasks, nil
}

func (s *Store) GetTask(ctx context.Context, id string) (*sdkprojectstate.Task, error) {
	_, byID, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	task, ok := byID[strings.TrimSpace(id)]
	if !ok {
		return nil, fmt.Errorf("task %q not found", id)
	}
	return &task, nil
}

func (s *Store) AddDependency(ctx context.Context, taskID, dependsOnID string) error {
	dependsOnID = strings.TrimSpace(dependsOnID)
	if exists, err := s.taskExists(ctx, dependsOnID); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("dependency task %q not found", dependsOnID)
	}
	if strings.TrimSpace(taskID) == dependsOnID {
		return fmt.Errorf("task cannot depend on itself")
	}
	_, err := s.mutateTask(ctx, taskID, func(task *sdkprojectstate.Task, now time.Time) error {
		task.DependsOn = appendUnique(task.DependsOn, dependsOnID)
		task.UpdatedAt = now
		return nil
	})
	return err
}

func (s *Store) RemoveDependency(ctx context.Context, taskID, dependsOnID string) error {
	_, err := s.mutateTask(ctx, taskID, func(task *sdkprojectstate.Task, now time.Time) error {
		task.DependsOn = removeString(task.DependsOn, strings.TrimSpace(dependsOnID))
		task.UpdatedAt = now
		return nil
	})
	return err
}

func (s *Store) AddComment(ctx context.Context, taskID, actor, body string) (*sdkprojectstate.TaskComment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("comment body is required")
	}
	comment := sdkprojectstate.TaskComment{
		ID:    newID("comment"),
		Actor: firstNonEmpty(strings.TrimSpace(actor), s.actor),
		Body:  body,
	}
	if _, err := s.mutateTask(ctx, taskID, func(task *sdkprojectstate.Task, now time.Time) error {
		comment.CreatedAt = now
		task.Comments = append(task.Comments, comment)
		task.UpdatedAt = now
		return nil
	}); err != nil {
		return nil, err
	}
	return &comment, nil
}

// mutateTask loads one task FOR UPDATE, applies fn, and writes it back.
func (s *Store) mutateTask(ctx context.Context, id string, fn func(task *sdkprojectstate.Task, now time.Time) error) (*sdkprojectstate.Task, error) {
	id = strings.TrimSpace(id)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning task transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM project_state_tasks WHERE project_id = $1 AND id = $2 FOR UPDATE`, s.projectID, id)
	task, err := scanTask(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("task %q not found", id)
		}
		return nil, fmt.Errorf("loading task %q: %w", id, err)
	}

	now := time.Now().UTC()
	if err := fn(&task, now); err != nil {
		return nil, err
	}

	comments, err := marshalComments(task.Comments)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE project_state_tasks
		SET title = $3, description = $4, type = $5, status = $6, priority = $7, assignee = $8,
		    depends_on = $9, labels = $10, comments = $11, metadata = $12, updated_at = $13, closed_at = $14
		WHERE project_id = $1 AND id = $2`,
		s.projectID, task.ID, task.Title, task.Description, task.Type, task.Status, task.Priority,
		task.Assignee, textArray(task.DependsOn), textArray(task.Labels), comments,
		nullableJSON(task.Metadata), task.UpdatedAt, task.ClosedAt,
	); err != nil {
		return nil, fmt.Errorf("updating task %q: %w", id, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing task update: %w", err)
	}
	return s.GetTask(ctx, task.ID)
}

func (s *Store) taskExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_state_tasks WHERE project_id = $1 AND id = $2)`, s.projectID, strings.TrimSpace(id)).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking task %q: %w", id, err)
	}
	return exists, nil
}

// loadTasks reads all tasks for the project and derives the Blocks edges from
// DependsOn (mirroring the SDK engine's recomputeBlocks).
func (s *Store) loadTasks(ctx context.Context) ([]sdkprojectstate.Task, map[string]sdkprojectstate.Task, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+taskColumns+` FROM project_state_tasks WHERE project_id = $1`, s.projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing tasks: %w", err)
	}
	defer rows.Close()

	byID := make(map[string]sdkprojectstate.Task)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, nil, fmt.Errorf("scanning task row: %w", err)
		}
		byID[task.ID] = task
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterating task rows: %w", err)
	}

	for id, task := range byID {
		for _, depID := range task.DependsOn {
			dep, ok := byID[depID]
			if !ok {
				continue
			}
			dep.Blocks = appendUnique(dep.Blocks, id)
			byID[depID] = dep
		}
	}
	tasks := make([]sdkprojectstate.Task, 0, len(byID))
	for _, task := range byID {
		tasks = append(tasks, task)
	}
	return tasks, byID, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanTask(row rowScanner) (sdkprojectstate.Task, error) {
	var task sdkprojectstate.Task
	var comments []byte
	var metadata []byte
	if err := row.Scan(&task.ID, &task.Title, &task.Description, &task.Type, &task.Status, &task.Priority,
		&task.Assignee, &task.DependsOn, &task.Labels, &comments, &task.SourceRun, &metadata,
		&task.CreatedAt, &task.UpdatedAt, &task.ClosedAt); err != nil {
		return task, err
	}
	if len(comments) > 0 {
		if err := json.Unmarshal(comments, &task.Comments); err != nil {
			return task, fmt.Errorf("decoding task comments: %w", err)
		}
	}
	if len(metadata) > 0 {
		task.Metadata = json.RawMessage(metadata)
	}
	return task, nil
}

// --- MemoryStore ---

const memoryColumns = `id, kind, scope, content, tags, task_ids, file_paths, source_run, metadata, created_at, updated_at, last_read_at`

func (s *Store) UpsertMemory(ctx context.Context, in sdkprojectstate.UpsertMemoryInput) (*sdkprojectstate.Memory, error) {
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return nil, fmt.Errorf("memory content is required")
	}
	now := time.Now().UTC()
	mem := sdkprojectstate.Memory{
		ID:        strings.TrimSpace(in.ID),
		Kind:      normalizeMemoryKind(in.Kind),
		Scope:     normalizeMemoryScope(in.Scope),
		Content:   content,
		Tags:      uniqueNonEmpty(in.Tags),
		TaskIDs:   uniqueNonEmpty(in.TaskIDs),
		FilePaths: uniqueNonEmpty(in.FilePaths),
		SourceRun: firstNonEmpty(strings.TrimSpace(in.SourceRun), s.runID),
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  in.Metadata,
	}
	if mem.ID == "" {
		mem.ID = newID("mem")
	}

	// Best-effort embedding: a missing vector degrades recall to lexical
	// matching but must never fail the write.
	var embedding *string
	if s.embedder != nil {
		if vec, err := s.embedder.Embed(ctx, content); err == nil && len(vec) > 0 {
			literal := sdkmemory.VectorLiteral(vec)
			embedding = &literal
		}
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO project_state_memories
			(project_id, id, kind, scope, content, tags, task_ids, file_paths, source_run, metadata, embedding, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::vector, $12, $13)
		ON CONFLICT (project_id, id) DO UPDATE SET
			kind = EXCLUDED.kind, scope = EXCLUDED.scope, content = EXCLUDED.content,
			tags = EXCLUDED.tags, task_ids = EXCLUDED.task_ids, file_paths = EXCLUDED.file_paths,
			source_run = EXCLUDED.source_run, metadata = EXCLUDED.metadata,
			embedding = EXCLUDED.embedding, updated_at = EXCLUDED.updated_at
		RETURNING `+memoryColumns,
		s.projectID, mem.ID, mem.Kind, mem.Scope, mem.Content, textArray(mem.Tags), textArray(mem.TaskIDs),
		textArray(mem.FilePaths), mem.SourceRun, nullableJSON(mem.Metadata), embedding, mem.CreatedAt, mem.UpdatedAt,
	)
	out, err := scanMemory(row)
	if err != nil {
		return nil, fmt.Errorf("upserting memory: %w", err)
	}
	return &out, nil
}

func (s *Store) SearchMemories(ctx context.Context, filter sdkprojectstate.MemoryFilter) ([]sdkprojectstate.Memory, error) {
	query := strings.TrimSpace(filter.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if s.embedder != nil {
		if vec, err := s.embedder.Embed(ctx, query); err == nil && len(vec) > 0 {
			return s.searchSemantic(ctx, filter, sdkmemory.VectorLiteral(vec))
		}
	}
	// Lexical fallback when no embedder is configured or embedding failed.
	return s.listMemories(ctx, filter, true)
}

// searchSemantic ranks kind/tag-filtered memories by cosine similarity over
// pgvector, backfilling with lexical matches for rows without an embedding.
func (s *Store) searchSemantic(ctx context.Context, filter sdkprojectstate.MemoryFilter, queryVec string) ([]sdkprojectstate.Memory, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	where, args := s.memoryFilterClauses(filter)
	args = append(args, queryVec)
	vecArg := len(args)
	args = append(args, limit)
	limitArg := len(args)

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM project_state_memories
		WHERE %s AND embedding IS NOT NULL
		ORDER BY embedding <=> $%d::vector
		LIMIT $%d`, memoryColumns, where, vecArg, limitArg), args...)
	if err != nil {
		return nil, fmt.Errorf("searching memories: %w", err)
	}
	out, err := collectMemories(rows)
	if err != nil {
		return nil, err
	}
	if len(out) >= limit {
		return out, nil
	}
	lexical, err := s.listMemories(ctx, filter, true)
	if err != nil {
		return out, nil //nolint:nilerr // semantic results are still valid
	}
	seen := make(map[string]struct{}, len(out))
	for _, mem := range out {
		seen[mem.ID] = struct{}{}
	}
	for _, mem := range lexical {
		if _, ok := seen[mem.ID]; ok {
			continue
		}
		out = append(out, mem)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *Store) ListMemories(ctx context.Context, filter sdkprojectstate.MemoryFilter) ([]sdkprojectstate.Memory, error) {
	return s.listMemories(ctx, filter, false)
}

func (s *Store) listMemories(ctx context.Context, filter sdkprojectstate.MemoryFilter, requireQuery bool) ([]sdkprojectstate.Memory, error) {
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	if requireQuery && query == "" {
		return nil, fmt.Errorf("query is required")
	}
	where, args := s.memoryFilterClauses(filter)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM project_state_memories
		WHERE %s
		ORDER BY (kind = 'pinned') DESC, updated_at DESC`, memoryColumns, where), args...)
	if err != nil {
		return nil, fmt.Errorf("listing memories: %w", err)
	}
	out, err := collectMemories(rows)
	if err != nil {
		return nil, err
	}
	if query != "" {
		filtered := out[:0]
		for _, mem := range out {
			if memoryMatchesQuery(mem, query) {
				filtered = append(filtered, mem)
			}
		}
		out = filtered
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *Store) memoryFilterClauses(filter sdkprojectstate.MemoryFilter) (string, []any) {
	clauses := []string{"project_id = $1"}
	args := []any{s.projectID}
	if len(filter.Kinds) > 0 {
		args = append(args, textArray(lowerTrimmed(filter.Kinds)))
		clauses = append(clauses, fmt.Sprintf("lower(kind) = ANY($%d)", len(args)))
	}
	if len(filter.Tags) > 0 {
		args = append(args, textArray(lowerTrimmed(filter.Tags)))
		clauses = append(clauses, fmt.Sprintf("(SELECT COALESCE(array_agg(lower(t)), '{}') FROM unnest(tags) AS t) @> $%d", len(args)))
	}
	return strings.Join(clauses, " AND "), args
}

func (s *Store) DeleteMemory(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM project_state_memories WHERE project_id = $1 AND id = $2`, s.projectID, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("deleting memory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("memory %q not found", id)
	}
	return nil
}

func collectMemories(rows pgx.Rows) ([]sdkprojectstate.Memory, error) {
	defer rows.Close()
	var out []sdkprojectstate.Memory
	for rows.Next() {
		mem, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning memory row: %w", err)
		}
		out = append(out, mem)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating memory rows: %w", err)
	}
	return out, nil
}

func scanMemory(row rowScanner) (sdkprojectstate.Memory, error) {
	var mem sdkprojectstate.Memory
	var metadata []byte
	if err := row.Scan(&mem.ID, &mem.Kind, &mem.Scope, &mem.Content, &mem.Tags, &mem.TaskIDs, &mem.FilePaths,
		&mem.SourceRun, &metadata, &mem.CreatedAt, &mem.UpdatedAt, &mem.LastReadAt); err != nil {
		return mem, err
	}
	if len(metadata) > 0 {
		mem.Metadata = json.RawMessage(metadata)
	}
	return mem, nil
}

// --- SessionStore ---

func (s *Store) SaveSessionSummary(ctx context.Context, summary sdkprojectstate.SessionSummary) (*sdkprojectstate.SessionSummary, error) {
	if strings.TrimSpace(summary.Summary) == "" {
		return nil, fmt.Errorf("session summary is required")
	}
	now := time.Now().UTC()
	if strings.TrimSpace(summary.ID) == "" {
		summary.ID = newID("session")
		summary.CreatedAt = now
	}
	summary.RunID = firstNonEmpty(strings.TrimSpace(summary.RunID), s.runID)
	summary.UpdatedAt = now
	summary.TaskIDs = uniqueNonEmpty(summary.TaskIDs)

	row := s.pool.QueryRow(ctx, `
		INSERT INTO project_state_session_summaries (project_id, id, run_id, summary, task_ids, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (project_id, id) DO UPDATE SET
			run_id = EXCLUDED.run_id, summary = EXCLUDED.summary,
			task_ids = EXCLUDED.task_ids, updated_at = EXCLUDED.updated_at
		RETURNING id, run_id, summary, task_ids, created_at, updated_at`,
		s.projectID, summary.ID, summary.RunID, summary.Summary, textArray(summary.TaskIDs), now, summary.UpdatedAt,
	)
	var out sdkprojectstate.SessionSummary
	if err := row.Scan(&out.ID, &out.RunID, &out.Summary, &out.TaskIDs, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return nil, fmt.Errorf("saving session summary: %w", err)
	}
	return &out, nil
}

func (s *Store) ListSessionSummaries(ctx context.Context, limit int) ([]sdkprojectstate.SessionSummary, error) {
	sql := `SELECT id, run_id, summary, task_ids, created_at, updated_at FROM project_state_session_summaries WHERE project_id = $1 ORDER BY updated_at DESC`
	args := []any{s.projectID}
	if limit > 0 {
		sql += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("listing session summaries: %w", err)
	}
	defer rows.Close()
	var out []sdkprojectstate.SessionSummary
	for rows.Next() {
		var summary sdkprojectstate.SessionSummary
		if err := rows.Scan(&summary.ID, &summary.RunID, &summary.Summary, &summary.TaskIDs, &summary.CreatedAt, &summary.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning session summary row: %w", err)
		}
		out = append(out, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating session summary rows: %w", err)
	}
	return out, nil
}

// --- PrimeStore ---

// PrimeContext renders the durable-state briefing in the same markdown shape
// as the SDK engine: active task, ready/blocked work, pinned/recent memories.
func (s *Store) PrimeContext(ctx context.Context, opts sdkprojectstate.PrimeOptions) (string, error) {
	if opts.ReadyLimit <= 0 {
		opts.ReadyLimit = 8
	}
	if opts.MemoryLimit <= 0 {
		opts.MemoryLimit = 8
	}
	actor := firstNonEmpty(strings.TrimSpace(opts.Actor), s.actor)

	tasks, byID, err := s.loadTasks(ctx)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("## Durable Project State\n")
	b.WriteString("Project: " + s.projectID + "\n")
	if s.workDir != "" {
		b.WriteString("Workspace: " + s.workDir + "\n")
	}

	if active := activeTask(tasks, byID, opts.ActiveTaskID, actor); active != nil {
		b.WriteString("\n### Active Task\n")
		writeTaskLine(&b, *active)
		if active.Description != "" {
			b.WriteString("  " + oneLine(active.Description, 220) + "\n")
		}
		if len(active.DependsOn) > 0 {
			b.WriteString("  Depends on: " + strings.Join(active.DependsOn, ", ") + "\n")
		}
	}

	ready, err := s.ReadyTasks(ctx, sdkprojectstate.TaskFilter{Actor: actor, Limit: opts.ReadyLimit})
	if err != nil {
		return "", err
	}
	if len(ready) > 0 {
		b.WriteString("\n### Ready Work\n")
		for _, task := range ready {
			writeTaskLine(&b, task)
		}
	}

	blocked := blockedTasks(tasks, byID, 5)
	if len(blocked) > 0 {
		b.WriteString("\n### Blocked Work\n")
		for _, task := range blocked {
			writeTaskLine(&b, task)
		}
	}

	memories, err := s.ListMemories(ctx, sdkprojectstate.MemoryFilter{})
	if err != nil {
		return "", err
	}
	pinned, recent := splitMemoriesForPrime(memories, opts.MemoryLimit)
	if len(pinned) > 0 {
		b.WriteString("\n### Pinned Memories\n")
		for _, mem := range pinned {
			b.WriteString("- " + oneLine(mem.Content, 220) + memorySuffix(mem) + "\n")
		}
	}
	if len(recent) > 0 {
		b.WriteString("\n### Recent Memories\n")
		for _, mem := range recent {
			b.WriteString("- " + oneLine(mem.Content, 180) + memorySuffix(mem) + "\n")
		}
	}

	out := strings.TrimSpace(b.String())
	if !strings.Contains(out, "###") {
		out += "\nNo durable tasks or memories yet."
	}
	return out, nil
}

// --- helpers (semantics mirror the SDK projectstate engine) ---

func activeTask(tasks []sdkprojectstate.Task, byID map[string]sdkprojectstate.Task, activeTaskID, actor string) *sdkprojectstate.Task {
	if activeTaskID != "" {
		if task, ok := byID[activeTaskID]; ok {
			return &task
		}
	}
	for _, task := range tasks {
		if task.Status == sdkprojectstate.TaskStatusInProgress && (actor == "" || task.Assignee == actor) {
			out := task
			return &out
		}
	}
	return nil
}

func blockedTasks(tasks []sdkprojectstate.Task, byID map[string]sdkprojectstate.Task, limit int) []sdkprojectstate.Task {
	var out []sdkprojectstate.Task
	for _, task := range tasks {
		if task.Status != sdkprojectstate.TaskStatusOpen && task.Status != sdkprojectstate.TaskStatusBlocked {
			continue
		}
		if hasOpenBlocker(byID, task) {
			out = append(out, task)
		}
	}
	sortTasks(out)
	return limitTasks(out, limit)
}

func splitMemoriesForPrime(memories []sdkprojectstate.Memory, limit int) (pinned, recent []sdkprojectstate.Memory) {
	for _, mem := range memories {
		if mem.Kind == sdkprojectstate.MemoryKindPinned {
			pinned = append(pinned, mem)
		} else {
			recent = append(recent, mem)
		}
	}
	sortMemoriesByUpdated(pinned)
	sortMemoriesByUpdated(recent)
	if limit > 0 && len(pinned) > limit {
		pinned = pinned[:limit]
	}
	remaining := limit
	if remaining > 0 {
		remaining -= len(pinned)
	}
	if remaining <= 0 {
		remaining = limit
	}
	if remaining > 0 && len(recent) > remaining {
		recent = recent[:remaining]
	}
	return pinned, recent
}

func sortMemoriesByUpdated(memories []sdkprojectstate.Memory) {
	sort.SliceStable(memories, func(i, j int) bool { return memories[i].UpdatedAt.After(memories[j].UpdatedAt) })
}

func hasOpenBlocker(byID map[string]sdkprojectstate.Task, task sdkprojectstate.Task) bool {
	for _, depID := range task.DependsOn {
		dep, ok := byID[depID]
		if !ok || dep.Status != sdkprojectstate.TaskStatusClosed {
			return true
		}
	}
	return false
}

func applyPatch(task *sdkprojectstate.Task, patch sdkprojectstate.TaskPatch, now time.Time) {
	if patch.Title != nil {
		task.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Description != nil {
		task.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.Type != nil {
		task.Type = normalizeTaskType(*patch.Type)
	}
	if patch.Status != nil {
		task.Status = normalizeTaskStatus(*patch.Status)
		if task.Status == sdkprojectstate.TaskStatusClosed {
			closedAt := now
			task.ClosedAt = &closedAt
		} else {
			task.ClosedAt = nil
		}
	}
	if patch.Priority != nil {
		task.Priority = normalizePriority(*patch.Priority)
	}
	if patch.Assignee != nil {
		task.Assignee = strings.TrimSpace(*patch.Assignee)
	}
	if patch.ReplaceLabels {
		task.Labels = uniqueNonEmpty(patch.Labels)
	}
	if patch.Metadata != nil {
		task.Metadata = *patch.Metadata
	}
	task.UpdatedAt = now
}

func normalizeTaskType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case sdkprojectstate.TaskTypeBug:
		return sdkprojectstate.TaskTypeBug
	case sdkprojectstate.TaskTypeFeature, "feat":
		return sdkprojectstate.TaskTypeFeature
	case sdkprojectstate.TaskTypeChore:
		return sdkprojectstate.TaskTypeChore
	case sdkprojectstate.TaskTypeEpic:
		return sdkprojectstate.TaskTypeEpic
	default:
		return sdkprojectstate.TaskTypeTask
	}
}

func normalizeTaskStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case sdkprojectstate.TaskStatusInProgress, "in-progress", "claimed":
		return sdkprojectstate.TaskStatusInProgress
	case sdkprojectstate.TaskStatusBlocked:
		return sdkprojectstate.TaskStatusBlocked
	case sdkprojectstate.TaskStatusClosed, "done", "completed":
		return sdkprojectstate.TaskStatusClosed
	case sdkprojectstate.TaskStatusDeferred:
		return sdkprojectstate.TaskStatusDeferred
	default:
		return sdkprojectstate.TaskStatusOpen
	}
}

func normalizePriority(value int) int {
	if value < 0 {
		return 0
	}
	if value > 4 {
		return 4
	}
	return value
}

func normalizeMemoryKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case sdkprojectstate.MemoryKindPinned:
		return sdkprojectstate.MemoryKindPinned
	case sdkprojectstate.MemoryKindEpisodic:
		return sdkprojectstate.MemoryKindEpisodic
	case sdkprojectstate.MemoryKindProcedural:
		return sdkprojectstate.MemoryKindProcedural
	default:
		return sdkprojectstate.MemoryKindSemantic
	}
}

func normalizeMemoryScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case sdkprojectstate.MemoryScopeUser:
		return sdkprojectstate.MemoryScopeUser
	case sdkprojectstate.MemoryScopeTask:
		return sdkprojectstate.MemoryScopeTask
	case sdkprojectstate.MemoryScopeFile:
		return sdkprojectstate.MemoryScopeFile
	default:
		return sdkprojectstate.MemoryScopeProject
	}
}

func memoryMatchesQuery(mem sdkprojectstate.Memory, query string) bool {
	haystack := strings.ToLower(strings.Join(append([]string{mem.Content, mem.Kind, mem.Scope}, append(mem.Tags, append(mem.TaskIDs, mem.FilePaths...)...)...), " "))
	if strings.Contains(haystack, query) {
		return true
	}
	for term := range strings.FieldsSeq(query) {
		if strings.Contains(haystack, term) {
			return true
		}
	}
	return false
}

func matchesLabels(actual, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(actual))
	for _, label := range actual {
		set[strings.ToLower(strings.TrimSpace(label))] = struct{}{}
	}
	for _, want := range wanted {
		if _, ok := set[strings.ToLower(strings.TrimSpace(want))]; !ok {
			return false
		}
	}
	return true
}

func sortTasks(tasks []sdkprojectstate.Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Status != tasks[j].Status {
			return tasks[i].Status < tasks[j].Status
		}
		if tasks[i].Priority != tasks[j].Priority {
			return tasks[i].Priority < tasks[j].Priority
		}
		if !tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
		}
		return tasks[i].ID < tasks[j].ID
	})
}

func limitTasks(tasks []sdkprojectstate.Task, limit int) []sdkprojectstate.Task {
	if limit > 0 && len(tasks) > limit {
		tasks = tasks[:limit]
	}
	return tasks
}

func writeTaskLine(b *strings.Builder, task sdkprojectstate.Task) {
	status := task.Status
	if status == "" {
		status = sdkprojectstate.TaskStatusOpen
	}
	fmt.Fprintf(b, "- %s [P%d %s] %s\n", task.ID, task.Priority, status, oneLine(task.Title, 180))
}

func memorySuffix(mem sdkprojectstate.Memory) string {
	var parts []string
	if mem.Kind != "" {
		parts = append(parts, mem.Kind)
	}
	if len(mem.Tags) > 0 {
		parts = append(parts, strings.Join(mem.Tags, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

func oneLine(value string, max int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if max > 0 && len(value) > max {
		return value[:max-3] + "..."
	}
	return value
}

func newID(prefix string) string {
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	return prefix + "_" + id[:12]
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || slices.Contains(values, value) {
		return uniqueNonEmpty(values)
	}
	return append(uniqueNonEmpty(values), value)
}

func removeString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	out := make([]string, 0, len(values))
	for _, existing := range values {
		if strings.TrimSpace(existing) != "" && existing != value {
			out = append(out, existing)
		}
	}
	return out
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func lowerTrimmed(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// textArray normalizes nil slices to empty so pgx writes '{}' not NULL.
func textArray(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func marshalComments(comments []sdkprojectstate.TaskComment) ([]byte, error) {
	if len(comments) == 0 {
		return []byte("[]"), nil
	}
	out, err := json.Marshal(comments)
	if err != nil {
		return nil, fmt.Errorf("encoding task comments: %w", err)
	}
	return out, nil
}

// SanitizeProjectID converts an arbitrary identifier (namespace/repo URL) to
// the SDK's lowercase dash-separated project id shape.
func SanitizeProjectID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// ProjectID returns a stable readable identity whose hash preserves distinctions
// that are lost when the namespace and repository are sanitized.
func ProjectID(namespace, repository string) string {
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return SanitizeProjectID(namespace + "-chat")
	}
	identity := namespace + "/" + CanonicalRepositoryIdentity(repository)
	prefix := SanitizeProjectID(identity)
	sum := sha256.Sum256([]byte(identity))
	return prefix + "-" + hex.EncodeToString(sum[:6])
}

// CanonicalRepositoryIdentity normalizes common repository URL and SCP forms.
func CanonicalRepositoryIdentity(repository string) string {
	repository = strings.TrimSpace(repository)
	if at := strings.Index(repository, "@"); at >= 0 && !strings.Contains(repository[:at], "://") {
		if colon := strings.Index(repository[at+1:], ":"); colon >= 0 {
			host := repository[at+1 : at+1+colon]
			path := repository[at+1+colon+1:]
			return canonicalRepositoryHostPath(host, path)
		}
	}

	candidate := repository
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	if parsed, err := url.Parse(candidate); err == nil && parsed.Host != "" {
		return canonicalRepositoryHostPath(parsed.Hostname(), parsed.Path)
	}
	return strings.TrimSuffix(strings.TrimSuffix(repository, "/"), ".git")
}

func canonicalRepositoryHostPath(host, path string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	path = strings.Trim(strings.TrimSpace(path), "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")
	if host == "github.com" {
		path = strings.ToLower(path)
	}
	return host + "/" + path
}
