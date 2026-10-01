// Package projectstate provides a PostgreSQL-backed implementation of the
// agent SDK's projectstate.Store interface. The SDK owns the durable
// project-state model (tasks, typed memories, briefing rendering, ranking and
// validation helpers) and its tool surface; the operator only supplies this
// persistence layer and must reuse the SDK's exported helpers rather than
// re-implementing their semantics.
package projectstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store implements sdkprojectstate.Store on PostgreSQL. All rows are scoped
// by projectID so multiple projects share the same tables. The pool is owned
// by the caller and is not closed by Close.
type Store struct {
	pool      dbConn
	embedder  sdkprojectstate.Embedder
	projectID string
	actor     string
	runID     string
	workDir   string
}

// dbConn is the subset of pgx used by the store. *pgxpool.Pool satisfies it in
// production; integration tests pass a pgx.Tx so every write rolls back.
type dbConn interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Options configures a Postgres project state store.
type Options struct {
	Pool *pgxpool.Pool
	// Embedder is optional; when set, recall fuses pgvector cosine similarity
	// with full-text ranking. Vectors must match the vector(1536) column.
	Embedder  sdkprojectstate.Embedder
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
	return newStore(opts.Pool, opts)
}

func newStore(conn dbConn, opts Options) (*Store, error) {
	projectID := strings.TrimSpace(opts.ProjectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	return &Store{
		pool:      conn,
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
		Type:        sdkprojectstate.NormalizeTaskType(in.Type),
		Status:      sdkprojectstate.TaskStatusOpen,
		Priority:    sdkprojectstate.NormalizePriority(in.Priority),
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
		sdkprojectstate.ApplyTaskPatch(task, patch, now)
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
	tasks, _, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	return sdkprojectstate.ReadyFromTasks(tasks, filter), nil
}

func (s *Store) ListTasks(ctx context.Context) ([]sdkprojectstate.Task, error) {
	tasks, _, err := s.loadTasks(ctx)
	if err != nil {
		return nil, err
	}
	sdkprojectstate.SortTasks(tasks)
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
// DependsOn with the SDK's RecomputeBlocks.
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

	sdkprojectstate.RecomputeBlocks(byID)
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

// --- ReleaseClaims ---

// ReleaseClaims reopens every in_progress task assigned to actor, clears the
// assignee, and records note as a comment.
func (s *Store) ReleaseClaims(ctx context.Context, actor, note string) ([]sdkprojectstate.Task, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return nil, fmt.Errorf("actor is required")
	}
	note = strings.TrimSpace(note)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning claim release transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `SELECT `+taskColumns+` FROM project_state_tasks
		WHERE project_id = $1 AND assignee = $2 AND status = $3 FOR UPDATE`,
		s.projectID, actor, sdkprojectstate.TaskStatusInProgress)
	if err != nil {
		return nil, fmt.Errorf("loading claimed tasks: %w", err)
	}
	var claimed []sdkprojectstate.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning claimed task: %w", err)
		}
		claimed = append(claimed, task)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating claimed tasks: %w", err)
	}
	if len(claimed) == 0 {
		return nil, nil
	}

	now := time.Now().UTC()
	for i := range claimed {
		task := &claimed[i]
		task.Status = sdkprojectstate.TaskStatusOpen
		task.Assignee = ""
		task.ClosedAt = nil
		task.UpdatedAt = now
		if note != "" {
			task.Comments = append(task.Comments, sdkprojectstate.TaskComment{ID: newID("comment"), Actor: actor, Body: note, CreatedAt: now})
		}
		comments, err := marshalComments(task.Comments)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE project_state_tasks
			SET status = $3, assignee = '', comments = $4, updated_at = $5, closed_at = NULL
			WHERE project_id = $1 AND id = $2`,
			s.projectID, task.ID, task.Status, comments, now,
		); err != nil {
			return nil, fmt.Errorf("releasing task %q: %w", task.ID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing claim release: %w", err)
	}
	sdkprojectstate.SortTasks(claimed)
	return claimed, nil
}

// --- MemoryStore ---

const memoryColumns = `id, kind, title, body, citations, commit_sha, source_run, created_at, updated_at, verified_at, use_count, last_used_at`

const (
	// defaultMemorySearchLimit mirrors the SDK engine's default.
	defaultMemorySearchLimit = 8
	// memorySearchCandidates bounds how many rows each recall signal
	// (full-text, vector) contributes before Go-side fusion and ranking.
	memorySearchCandidates = 50
	// memoryVectorWeight is the share of the fused score taken by cosine
	// similarity when an embedder is configured.
	memoryVectorWeight = 0.4
	// memoryVectorFloor drops weak vector similarity so semantic recall
	// cannot surface arbitrary memories.
	memoryVectorFloor = 0.3
	// memoryEmbedTimeout bounds best-effort embedding calls.
	memoryEmbedTimeout = 5 * time.Second
	// memoryEmbeddingDims is the project_state_memories.embedding width.
	memoryEmbeddingDims = 1536
	// memoryEmbeddingBackfillBatch bounds lazy embedding backfill per search.
	memoryEmbeddingBackfillBatch = 16
)

func (s *Store) SaveMemory(ctx context.Context, in sdkprojectstate.SaveMemoryInput) (*sdkprojectstate.Memory, error) {
	if err := sdkprojectstate.ValidateMemoryInput(&in); err != nil {
		return nil, err
	}
	citations, err := marshalCitations(in.Citations)
	if err != nil {
		return nil, err
	}
	embedding := s.embedMemoryText(ctx, in.Title, in.Body)
	now := time.Now().UTC()

	// Updates keep source_run: it records the creator, and AgentRun data
	// deletion removes memories by source_run, so later editors (including
	// the consolidator) must not take ownership of shared memories.
	if in.ID != "" {
		row := s.pool.QueryRow(ctx, `
			UPDATE project_state_memories
			SET kind = $3, title = $4, body = $5, citations = $6, commit_sha = $7,
			    embedding = $8::vector, updated_at = $9, verified_at = $9
			WHERE project_id = $1 AND id = $2
			RETURNING `+memoryColumns,
			s.projectID, in.ID, in.Kind, in.Title, in.Body, citations, in.CommitSHA, embedding, now,
		)
		mem, err := scanMemory(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("memory %q not found; omit id to create a new memory", in.ID)
		}
		if err != nil {
			return nil, fmt.Errorf("updating memory: %w", err)
		}
		return &mem, nil
	}

	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM project_state_memories WHERE project_id = $1`, s.projectID).Scan(&count); err != nil {
		return nil, fmt.Errorf("counting memories: %w", err)
	}
	if count >= sdkprojectstate.DefaultMemoryCap {
		return nil, fmt.Errorf("project memory is full (%d memories): consolidate by updating an existing memory (memory_save with its id) or remove obsolete ones with memory_delete", count)
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO project_state_memories
			(project_id, id, kind, title, body, citations, commit_sha, source_run, embedding, created_at, updated_at, verified_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::vector, $10, $10, $10)
		RETURNING `+memoryColumns,
		s.projectID, newID("mem"), in.Kind, in.Title, in.Body, citations, in.CommitSHA,
		firstNonEmpty(in.SourceRun, s.runID), embedding, now,
	)
	mem, err := scanMemory(row)
	if err != nil {
		return nil, fmt.Errorf("inserting memory: %w", err)
	}
	return &mem, nil
}

func (s *Store) GetMemory(ctx context.Context, id string) (*sdkprojectstate.Memory, error) {
	id = strings.TrimSpace(id)
	row := s.pool.QueryRow(ctx, `SELECT `+memoryColumns+` FROM project_state_memories WHERE project_id = $1 AND id = $2`, s.projectID, id)
	mem, err := scanMemory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("memory %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("loading memory %q: %w", id, err)
	}
	return &mem, nil
}

// SearchMemories fuses Postgres full-text ranking (weighted title/body
// tsvector, OR semantics, english stemming) with the SDK's LexicalScore and,
// when an embedder is configured, pgvector cosine similarity.
func (s *Store) SearchMemories(ctx context.Context, q sdkprojectstate.MemoryQuery) ([]sdkprojectstate.MemoryHit, error) {
	query := strings.TrimSpace(q.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultMemorySearchLimit
	}
	kinds := normalizedKinds(q.Kinds)

	candidates := map[string]*memoryCandidate{}
	if tsQuery := fullTextQuery(query); tsQuery != "" {
		rows, err := s.pool.Query(ctx, `
			SELECT `+memoryColumns+`, ts_rank_cd(search_tsv, to_tsquery('english', $2), 32)
			FROM project_state_memories
			WHERE project_id = $1 AND search_tsv @@ to_tsquery('english', $2)
			  AND (cardinality($3::text[]) = 0 OR kind = ANY($3))
			ORDER BY 13 DESC
			LIMIT $4`,
			s.projectID, tsQuery, kinds, memorySearchCandidates)
		if err != nil {
			return nil, fmt.Errorf("searching memories: %w", err)
		}
		if err := collectCandidates(rows, candidates, func(c *memoryCandidate, v float64) { c.text = v }); err != nil {
			return nil, err
		}
	}
	vectorUsed := false
	if vec := s.embedText(ctx, query); vec != "" {
		s.backfillEmbeddings(ctx)
		rows, err := s.pool.Query(ctx, `
			SELECT `+memoryColumns+`, 1 - (embedding <=> $2::vector)
			FROM project_state_memories
			WHERE project_id = $1 AND embedding IS NOT NULL
			  AND (cardinality($3::text[]) = 0 OR kind = ANY($3))
			ORDER BY embedding <=> $2::vector
			LIMIT $4`,
			s.projectID, vec, kinds, memorySearchCandidates)
		if err == nil {
			vectorUsed = true
			if err := collectCandidates(rows, candidates, func(c *memoryCandidate, v float64) { c.vector = v }); err != nil {
				return nil, err
			}
		}
	}

	hits := make([]sdkprojectstate.MemoryHit, 0, len(candidates))
	for _, c := range candidates {
		// Text signal: the stemmed full-text rank (bounded to [0,1) by
		// normalization 32) blended with the SDK's exact-token coverage.
		text := 0.5*c.text + 0.5*sdkprojectstate.LexicalScore(query, c.mem)
		score := text
		if vectorUsed {
			vector := c.vector
			if vector < memoryVectorFloor {
				vector = 0
			}
			score = (1-memoryVectorWeight)*text + memoryVectorWeight*vector
		}
		if score <= 0 {
			continue
		}
		hits = append(hits, sdkprojectstate.MemoryHit{Memory: c.mem, Score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

type memoryCandidate struct {
	mem    sdkprojectstate.Memory
	text   float64
	vector float64
}

func collectCandidates(rows pgx.Rows, into map[string]*memoryCandidate, set func(*memoryCandidate, float64)) error {
	defer rows.Close()
	for rows.Next() {
		var mem sdkprojectstate.Memory
		var citations []byte
		var signal float64
		if err := rows.Scan(&mem.ID, &mem.Kind, &mem.Title, &mem.Body, &citations, &mem.CommitSHA, &mem.SourceRun,
			&mem.CreatedAt, &mem.UpdatedAt, &mem.VerifiedAt, &mem.UseCount, &mem.LastUsedAt, &signal); err != nil {
			return fmt.Errorf("scanning memory candidate: %w", err)
		}
		if err := unmarshalCitations(citations, &mem); err != nil {
			return err
		}
		c, ok := into[mem.ID]
		if !ok {
			c = &memoryCandidate{mem: mem}
			into[mem.ID] = c
		}
		set(c, signal)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating memory candidates: %w", err)
	}
	return nil
}

// fullTextQuery turns free text into an OR tsquery over the SDK's tokens so a
// memory matching any meaningful term is a candidate; ranking does the rest.
func fullTextQuery(query string) string {
	var terms []string
	seen := map[string]bool{}
	for _, token := range sdkprojectstate.Tokenize(query) {
		// Compound tokens (memory_save, dead-code) are skipped: Tokenize also
		// emits their parts, which is how Postgres' parser indexes them. Any
		// other non-alphanumeric rune would be tsquery syntax.
		if len(token) < 2 || seen[token] || strings.IndexFunc(token, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) >= 0 {
			continue
		}
		seen[token] = true
		terms = append(terms, token)
	}
	return strings.Join(terms, " | ")
}

func normalizedKinds(kinds []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if strings.TrimSpace(kind) == "" {
			continue
		}
		kind = sdkprojectstate.NormalizeMemoryKind(kind)
		if !seen[kind] {
			seen[kind] = true
			out = append(out, kind)
		}
	}
	return out
}

// ListMemories returns memories ordered by kind priority (preference,
// decision, procedure, fact) then most recently updated.
func (s *Store) ListMemories(ctx context.Context, filter sdkprojectstate.MemoryFilter) ([]sdkprojectstate.Memory, error) {
	args := []any{s.projectID, normalizedKinds(filter.Kinds)}
	sql := `SELECT ` + memoryColumns + ` FROM project_state_memories
		WHERE project_id = $1 AND (cardinality($2::text[]) = 0 OR kind = ANY($2))
		ORDER BY CASE kind WHEN 'preference' THEN 0 WHEN 'decision' THEN 1 WHEN 'procedure' THEN 2 ELSE 3 END,
		         updated_at DESC, id`
	if filter.Limit > 0 {
		sql += ` LIMIT $3`
		args = append(args, filter.Limit)
	}
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("listing memories: %w", err)
	}
	return collectMemories(rows)
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

func (s *Store) VerifyMemory(ctx context.Context, id, commitSHA string) (*sdkprojectstate.Memory, error) {
	id = strings.TrimSpace(id)
	row := s.pool.QueryRow(ctx, `
		UPDATE project_state_memories
		SET verified_at = $3, commit_sha = CASE WHEN $4 = '' THEN commit_sha ELSE $4 END
		WHERE project_id = $1 AND id = $2
		RETURNING `+memoryColumns,
		s.projectID, id, time.Now().UTC(), strings.TrimSpace(commitSHA),
	)
	mem, err := scanMemory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("memory %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("verifying memory %q: %w", id, err)
	}
	return &mem, nil
}

func (s *Store) TouchMemories(ctx context.Context, ids []string) error {
	ids = uniqueNonEmpty(ids)
	if len(ids) == 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE project_state_memories
		SET use_count = use_count + 1, last_used_at = $3
		WHERE project_id = $1 AND id = ANY($2)`,
		s.projectID, ids, time.Now().UTC(),
	); err != nil {
		return fmt.Errorf("recording memory use: %w", err)
	}
	return nil
}

// backfillEmbeddings embeds a bounded batch of memories that have no vector
// (written while the embedder was unavailable, or before one was configured)
// so they regain the semantic recall signal. Best-effort.
func (s *Store) backfillEmbeddings(ctx context.Context) {
	rows, err := s.pool.Query(ctx, `SELECT id, title, body FROM project_state_memories
		WHERE project_id = $1 AND embedding IS NULL ORDER BY updated_at DESC LIMIT $2`,
		s.projectID, memoryEmbeddingBackfillBatch)
	if err != nil {
		return
	}
	type pending struct{ id, text string }
	var batch []pending
	for rows.Next() {
		var id, title, body string
		if rows.Scan(&id, &title, &body) == nil {
			batch = append(batch, pending{id: id, text: title + "\n" + body})
		}
	}
	rows.Close()
	if len(batch) == 0 {
		return
	}
	texts := make([]string, len(batch))
	for i, p := range batch {
		texts[i] = p.text
	}
	embedCtx, cancel := context.WithTimeout(ctx, memoryEmbedTimeout)
	defer cancel()
	vecs, err := s.embedder.Embed(embedCtx, texts)
	if err != nil || len(vecs) != len(batch) {
		return
	}
	for i, p := range batch {
		if len(vecs[i]) != memoryEmbeddingDims {
			continue
		}
		_, _ = s.pool.Exec(ctx, `UPDATE project_state_memories SET embedding = $3::vector
			WHERE project_id = $1 AND id = $2 AND embedding IS NULL`,
			s.projectID, p.id, sdkprojectstate.VectorLiteral(vecs[i]))
	}
}

// embedMemoryText returns the pgvector literal for a memory's title and body,
// or nil when no embedder is configured or embedding fails (best-effort: a
// missing vector only removes the semantic signal for that memory).
func (s *Store) embedMemoryText(ctx context.Context, title, body string) any {
	if vec := s.embedText(ctx, title+"\n"+body); vec != "" {
		return vec
	}
	return nil
}

func (s *Store) embedText(ctx context.Context, text string) string {
	if s.embedder == nil || strings.TrimSpace(text) == "" {
		return ""
	}
	embedCtx, cancel := context.WithTimeout(ctx, memoryEmbedTimeout)
	defer cancel()
	vecs, err := s.embedder.Embed(embedCtx, []string{text})
	// The column is vector(1536): a model with another size must degrade to
	// full-text recall, never fail the write.
	if err != nil || len(vecs) != 1 || len(vecs[0]) != memoryEmbeddingDims {
		return ""
	}
	return sdkprojectstate.VectorLiteral(vecs[0])
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
	var citations []byte
	if err := row.Scan(&mem.ID, &mem.Kind, &mem.Title, &mem.Body, &citations, &mem.CommitSHA, &mem.SourceRun,
		&mem.CreatedAt, &mem.UpdatedAt, &mem.VerifiedAt, &mem.UseCount, &mem.LastUsedAt); err != nil {
		return mem, err
	}
	if err := unmarshalCitations(citations, &mem); err != nil {
		return mem, err
	}
	return mem, nil
}

func marshalCitations(citations []sdkprojectstate.Citation) ([]byte, error) {
	if len(citations) == 0 {
		return []byte("[]"), nil
	}
	out, err := json.Marshal(citations)
	if err != nil {
		return nil, fmt.Errorf("encoding memory citations: %w", err)
	}
	return out, nil
}

func unmarshalCitations(raw []byte, mem *sdkprojectstate.Memory) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &mem.Citations); err != nil {
		return fmt.Errorf("decoding memory citations: %w", err)
	}
	if len(mem.Citations) == 0 {
		mem.Citations = nil
	}
	return nil
}

// --- PrimeStore ---

// PrimeContext renders the SDK briefing (RenderBriefing) from Postgres state.
func (s *Store) PrimeContext(ctx context.Context, opts sdkprojectstate.PrimeOptions) (string, error) {
	if opts.ReadyLimit <= 0 {
		opts.ReadyLimit = 8
	}
	actor := firstNonEmpty(strings.TrimSpace(opts.Actor), s.actor)
	tasks, _, err := s.loadTasks(ctx)
	if err != nil {
		return "", err
	}
	memories, err := s.ListMemories(ctx, sdkprojectstate.MemoryFilter{})
	if err != nil {
		return "", err
	}
	return sdkprojectstate.RenderBriefing(sdkprojectstate.BriefingInput{
		ProjectID: s.projectID,
		Active:    sdkprojectstate.ActiveTask(tasks, opts.ActiveTaskID, actor),
		Ready:     sdkprojectstate.ReadyFromTasks(tasks, sdkprojectstate.TaskFilter{Actor: actor, Limit: opts.ReadyLimit}),
		Blocked:   sdkprojectstate.BlockedFromTasks(tasks, 5),
		Memories:  memories,
	}), nil
}

// --- helpers ---

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
