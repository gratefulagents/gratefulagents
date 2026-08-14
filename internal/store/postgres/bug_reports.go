package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

const agentBugReportColumns = `id, namespace, run_name, session_id, category, tool_name,
	title, body, fingerprint, occurrences, status, status_note, status_actor,
	first_seen_at, last_seen_at, created_at, updated_at`

func scanAgentBugReportRow(row pgx.Row) (*store.AgentBugReportRecord, bool, error) {
	var rec store.AgentBugReportRecord
	var created bool
	if err := row.Scan(&rec.ID, &rec.Namespace, &rec.RunName, &rec.SessionID,
		&rec.Category, &rec.ToolName, &rec.Title, &rec.Body, &rec.Fingerprint,
		&rec.Occurrences, &rec.Status, &rec.StatusNote, &rec.StatusActor,
		&rec.FirstSeenAt, &rec.LastSeenAt, &rec.CreatedAt, &rec.UpdatedAt, &created); err != nil {
		return nil, false, err
	}
	return &rec, created, nil
}

// UpsertAgentBugReport inserts the report or merges a reoccurrence into the
// existing (namespace, fingerprint) row. See store.AgentBugReportStore.
func (s *Store) UpsertAgentBugReport(ctx context.Context, rec *store.AgentBugReportRecord) (*store.AgentBugReportRecord, bool, error) {
	if rec.Namespace == "" || rec.Fingerprint == "" {
		return nil, false, errors.New("namespace and fingerprint are required")
	}
	category := rec.Category
	if category == "" {
		category = store.AgentBugReportCategoryBug
	}
	if !store.ValidAgentBugReportCategory(category) {
		return nil, false, fmt.Errorf("invalid bug report category %q", category)
	}
	// (xmax = 0) distinguishes a fresh insert from a conflict-update.
	// Occurrences count distinct reporting runs, so a retry of report_bug
	// within the same run neither inflates the count nor reopens a report a
	// human just resolved: only a *different* run counts as a reoccurrence
	// and regresses a resolved report to open. Dismissed ("won't fix /
	// noise") stays dismissed either way.
	row := s.pool.QueryRow(ctx, `
		INSERT INTO agent_bug_reports (namespace, run_name, session_id, category,
			tool_name, title, body, fingerprint)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (namespace, fingerprint) DO UPDATE SET
			body = EXCLUDED.body,
			occurrences = CASE WHEN agent_bug_reports.run_name = EXCLUDED.run_name
				THEN agent_bug_reports.occurrences
				ELSE agent_bug_reports.occurrences + 1 END,
			status = CASE WHEN agent_bug_reports.status = 'resolved'
					AND agent_bug_reports.run_name <> EXCLUDED.run_name
				THEN 'open' ELSE agent_bug_reports.status END,
			run_name = EXCLUDED.run_name,
			session_id = EXCLUDED.session_id,
			last_seen_at = now(),
			updated_at = now()
		RETURNING `+agentBugReportColumns+`, (xmax = 0) AS created`,
		rec.Namespace, rec.RunName, rec.SessionID, category,
		rec.ToolName, rec.Title, rec.Body, rec.Fingerprint)
	out, created, err := scanAgentBugReportRow(row)
	if err != nil {
		return nil, false, fmt.Errorf("upserting agent bug report: %w", err)
	}
	return out, created, nil
}

// GetAgentBugReport returns one report, or (nil, nil) when it does not exist.
func (s *Store) GetAgentBugReport(ctx context.Context, namespace string, id uuid.UUID) (*store.AgentBugReportRecord, error) {
	if namespace == "" {
		return nil, errors.New("namespace is required")
	}
	row := s.pool.QueryRow(ctx, `
		SELECT `+agentBugReportColumns+`, false
		FROM agent_bug_reports
		WHERE namespace = $1 AND id = $2`, namespace, id)
	rec, _, err := scanAgentBugReportRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting agent bug report: %w", err)
	}
	return rec, nil
}

// ListAgentBugReports lists reports matching the filter, most recently seen
// first.
func (s *Store) ListAgentBugReports(ctx context.Context, f store.AgentBugReportFilter) ([]store.AgentBugReportRecord, error) {
	if f.Namespace == "" {
		return nil, errors.New("namespace is required")
	}
	query := `SELECT ` + agentBugReportColumns + `, false FROM agent_bug_reports WHERE namespace = $1`
	args := []any{f.Namespace}
	if f.Status != "" {
		args = append(args, f.Status)
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if f.Category != "" {
		args = append(args, f.Category)
		query += fmt.Sprintf(" AND category = $%d", len(args))
	}
	query += " ORDER BY last_seen_at DESC, id"
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	args = append(args, limit)
	query += fmt.Sprintf(" LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing agent bug reports: %w", err)
	}
	defer rows.Close()
	var out []store.AgentBugReportRecord
	for rows.Next() {
		rec, _, err := scanAgentBugReportRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning agent bug report: %w", err)
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// SetAgentBugReportStatus updates the triage status of one report.
func (s *Store) SetAgentBugReportStatus(ctx context.Context, namespace string, id uuid.UUID, status, actor, note string) error {
	if namespace == "" {
		return errors.New("namespace is required")
	}
	if !store.ValidAgentBugReportStatus(status) {
		return fmt.Errorf("invalid bug report status %q", status)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE agent_bug_reports
		SET status = $3, status_actor = $4, status_note = $5, updated_at = now()
		WHERE namespace = $1 AND id = $2`,
		namespace, id, status, actor, note)
	if err != nil {
		return fmt.Errorf("updating agent bug report status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrAgentBugReportNotFound
	}
	return nil
}
