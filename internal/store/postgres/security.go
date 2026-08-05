package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// securitySeverityRankSQL renders a CASE expression ranking a severity
// column/expression so "keep highest severity" and ordering can be done in
// SQL. expr must be a trusted column reference, never user input.
func securitySeverityRankSQL(expr string) string {
	return "CASE " + expr +
		" WHEN 'critical' THEN 4" +
		" WHEN 'high' THEN 3" +
		" WHEN 'medium' THEN 2" +
		" WHEN 'low' THEN 1" +
		" WHEN 'info' THEN 0" +
		" ELSE -1 END"
}

// escapeSecurityLike escapes the LIKE/ILIKE metacharacters %, _, and the
// default escape character backslash so a search term matches literally.
func escapeSecurityLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// requireSecurityNamespace rejects empty namespaces so per-finding
// operations fail closed instead of matching across all namespaces.
func requireSecurityNamespace(namespace string) error {
	if namespace == "" {
		return errors.New("namespace is required")
	}
	return nil
}

// securityFindingFilterSQL builds a parameterized WHERE clause for the
// filter. It returns an empty string when no conditions apply.
func securityFindingFilterSQL(f store.SecurityFindingFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(format string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(format, len(args)))
	}
	if f.Namespace != "" {
		add("namespace = $%d", f.Namespace)
	}
	if f.ScanName != "" {
		add("scan_name = $%d", f.ScanName)
	}
	if f.RunName != "" {
		add("run_name = $%d", f.RunName)
	}
	if f.Repository != "" {
		add("repository = $%d", f.Repository)
	}
	if f.Category != "" {
		add("category = $%d", f.Category)
	}
	if f.Severity != "" {
		add("severity = $%d", f.Severity)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Search != "" {
		args = append(args, "%"+escapeSecurityLike(f.Search)+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(title ILIKE $%d OR description ILIKE $%d OR file_path ILIKE $%d)", n, n, n))
	}
	if f.MinScore > 0 {
		add("score >= $%d", f.MinScore)
	}
	if !f.IncludeDuplicates {
		conds = append(conds, "duplicate_of IS NULL")
	}
	if len(conds) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// securityLimit clamps a caller-provided limit to (0, max], defaulting when
// unset or non-positive.
func securityLimit(limit, def, max int32) int32 {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

const securityScanColumns = `id, namespace, scan_name, run_name, session_id, repository, revision,
	status, summary, started_at, completed_at, counts, created_at, updated_at`

func scanSecurityScanRow(row pgx.Row) (*store.SecurityScanRecord, error) {
	var rec store.SecurityScanRecord
	var counts []byte
	if err := row.Scan(&rec.ID, &rec.Namespace, &rec.ScanName, &rec.RunName, &rec.SessionID,
		&rec.Repository, &rec.Revision, &rec.Status, &rec.Summary, &rec.StartedAt,
		&rec.CompletedAt, &counts, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, err
	}
	rec.Counts = map[string]int32{}
	if len(counts) > 0 {
		if err := json.Unmarshal(counts, &rec.Counts); err != nil {
			return nil, fmt.Errorf("decoding scan counts: %w", err)
		}
	}
	return &rec, nil
}

func (s *Store) UpsertSecurityScan(ctx context.Context, rec *store.SecurityScanRecord) (*store.SecurityScanRecord, error) {
	status := rec.Status
	if status == "" {
		status = "running"
	}
	counts := rec.Counts
	if counts == nil {
		counts = map[string]int32{}
	}
	countsJSON, err := json.Marshal(counts)
	if err != nil {
		return nil, fmt.Errorf("encoding scan counts: %w", err)
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO security_scans (namespace, scan_name, run_name, session_id, repository,
			revision, status, summary, started_at, completed_at, counts)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (namespace, run_name) DO UPDATE SET
			scan_name = EXCLUDED.scan_name,
			session_id = EXCLUDED.session_id,
			repository = EXCLUDED.repository,
			revision = EXCLUDED.revision,
			status = EXCLUDED.status,
			summary = EXCLUDED.summary,
			started_at = EXCLUDED.started_at,
			completed_at = EXCLUDED.completed_at,
			counts = EXCLUDED.counts,
			updated_at = now()
		RETURNING `+securityScanColumns,
		rec.Namespace, rec.ScanName, rec.RunName, rec.SessionID, rec.Repository,
		rec.Revision, status, rec.Summary, rec.StartedAt, rec.CompletedAt, countsJSON)
	out, err := scanSecurityScanRow(row)
	if err != nil {
		return nil, fmt.Errorf("upserting security scan: %w", err)
	}
	return out, nil
}

func (s *Store) GetSecurityScan(ctx context.Context, namespace, runName string) (*store.SecurityScanRecord, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+securityScanColumns+`
		FROM security_scans
		WHERE namespace = $1 AND run_name = $2`, namespace, runName)
	rec, err := scanSecurityScanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting security scan: %w", err)
	}
	return rec, nil
}

func (s *Store) ListSecurityScans(ctx context.Context, namespace, scanName string, limit int32) ([]store.SecurityScanRecord, error) {
	where := "WHERE namespace = $1"
	args := []any{namespace}
	if scanName != "" {
		args = append(args, scanName)
		where += fmt.Sprintf(" AND scan_name = $%d", len(args))
	}
	args = append(args, securityLimit(limit, 200, 1000))
	rows, err := s.pool.Query(ctx, `
		SELECT `+securityScanColumns+`
		FROM security_scans
		`+where+`
		ORDER BY created_at DESC, id
		LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("listing security scans: %w", err)
	}
	defer rows.Close()
	var out []store.SecurityScanRecord
	for rows.Next() {
		rec, err := scanSecurityScanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning security scan: %w", err)
		}
		out = append(out, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing security scans: %w", err)
	}
	return out, nil
}

const securityFindingColumns = `id, scan_id, namespace, scan_name, run_name, session_id, fingerprint,
	title, category, severity, confidence, repository, revision, file_path, start_line, end_line,
	symbol, cwe, description, impact, attack_vector, remediation, references_urls, source_agent,
	scan_step, score, status, duplicate_of, occurrences, raw, first_seen_at, last_seen_at`

func scanSecurityFindingRow(row pgx.Row, extra ...any) (*store.SecurityFindingRecord, error) {
	var rec store.SecurityFindingRecord
	dest := make([]any, 0, 32+len(extra))
	dest = append(dest, &rec.ID, &rec.ScanID, &rec.Namespace, &rec.ScanName, &rec.RunName, &rec.SessionID,
		&rec.Fingerprint, &rec.Title, &rec.Category, &rec.Severity, &rec.Confidence, &rec.Repository,
		&rec.Revision, &rec.FilePath, &rec.StartLine, &rec.EndLine, &rec.Symbol, &rec.CWE,
		&rec.Description, &rec.Impact, &rec.AttackVector, &rec.Remediation, &rec.References,
		&rec.SourceAgent, &rec.ScanStep, &rec.Score, &rec.Status, &rec.DuplicateOf,
		&rec.Occurrences, &rec.Raw, &rec.FirstSeenAt, &rec.LastSeenAt)
	dest = append(dest, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Store) UpsertSecurityFinding(ctx context.Context, rec *store.SecurityFindingRecord) (*store.SecurityFindingRecord, bool, error) {
	status := rec.Status
	if status == "" {
		status = store.SecurityFindingStatusOpen
	}
	occurrences := rec.Occurrences
	if occurrences <= 0 {
		occurrences = 1
	}
	cwe := rec.CWE
	if cwe == nil {
		cwe = []string{}
	}
	references := rec.References
	if references == nil {
		references = []string{}
	}
	raw := rec.Raw
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	rank := securitySeverityRankSQL

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning finding upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// xmax = 0 distinguishes a fresh insert from a conflict-update: updated
	// rows carry the deleting/locking transaction id in xmax. This is a
	// heuristic: a concurrent row lock (e.g. SELECT ... FOR UPDATE or a
	// speculative insert race) can set xmax on a freshly inserted row and
	// report it as a merge. The only consequence is a spurious "reobserved"
	// event; the stored finding itself is unaffected.
	var created bool
	row := tx.QueryRow(ctx, `
		INSERT INTO security_findings (scan_id, namespace, scan_name, run_name, session_id,
			fingerprint, title, category, severity, confidence, repository, revision, file_path,
			start_line, end_line, symbol, cwe, description, impact, attack_vector, remediation,
			references_urls, source_agent, scan_step, score, status, duplicate_of, occurrences, raw)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
			$19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29)
		ON CONFLICT (namespace, scan_name, repository, fingerprint) DO UPDATE SET
			scan_id = EXCLUDED.scan_id,
			scan_name = EXCLUDED.scan_name,
			run_name = EXCLUDED.run_name,
			session_id = EXCLUDED.session_id,
			title = EXCLUDED.title,
			category = EXCLUDED.category,
			severity = CASE WHEN `+rank("EXCLUDED.severity")+` > `+rank("security_findings.severity")+`
				THEN EXCLUDED.severity ELSE security_findings.severity END,
			confidence = EXCLUDED.confidence,
			revision = EXCLUDED.revision,
			file_path = EXCLUDED.file_path,
			start_line = EXCLUDED.start_line,
			end_line = EXCLUDED.end_line,
			symbol = EXCLUDED.symbol,
			cwe = EXCLUDED.cwe,
			description = EXCLUDED.description,
			impact = EXCLUDED.impact,
			attack_vector = EXCLUDED.attack_vector,
			remediation = EXCLUDED.remediation,
			references_urls = EXCLUDED.references_urls,
			source_agent = EXCLUDED.source_agent,
			scan_step = EXCLUDED.scan_step,
			score = GREATEST(security_findings.score, EXCLUDED.score),
			raw = EXCLUDED.raw,
			occurrences = security_findings.occurrences + 1,
			last_seen_at = now()
		RETURNING `+securityFindingColumns+`, (xmax = 0)`,
		rec.ScanID, rec.Namespace, rec.ScanName, rec.RunName, rec.SessionID,
		rec.Fingerprint, rec.Title, rec.Category, rec.Severity, rec.Confidence,
		rec.Repository, rec.Revision, rec.FilePath, rec.StartLine, rec.EndLine,
		rec.Symbol, cwe, rec.Description, rec.Impact, rec.AttackVector, rec.Remediation,
		references, rec.SourceAgent, rec.ScanStep, rec.Score, status, rec.DuplicateOf,
		occurrences, raw)
	out, err := scanSecurityFindingRow(row, &created)
	if err != nil {
		return nil, false, fmt.Errorf("upserting security finding: %w", err)
	}

	if !created {
		detail, err := json.Marshal(map[string]string{"scan_name": rec.ScanName, "run_name": rec.RunName})
		if err != nil {
			return nil, false, fmt.Errorf("encoding reobserved detail: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO security_finding_events (finding_id, event_type, actor, detail)
			VALUES ($1, 'reobserved', $2, $3)`, out.ID, rec.SourceAgent, detail); err != nil {
			return nil, false, fmt.Errorf("recording reobserved event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing finding upsert: %w", err)
	}
	return out, created, nil
}

func (s *Store) ListSecurityFindings(ctx context.Context, f store.SecurityFindingFilter) ([]store.SecurityFindingRecord, error) {
	where, args := securityFindingFilterSQL(f)
	args = append(args, securityLimit(f.Limit, 200, 1000))
	limitPos := len(args)
	offset := max(f.Offset, 0)
	args = append(args, offset)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM security_findings
		%s
		ORDER BY score DESC, %s DESC, last_seen_at DESC, id
		LIMIT $%d OFFSET $%d`,
		securityFindingColumns, where, securitySeverityRankSQL("severity"), limitPos, limitPos+1),
		args...)
	if err != nil {
		return nil, fmt.Errorf("listing security findings: %w", err)
	}
	defer rows.Close()
	var out []store.SecurityFindingRecord
	for rows.Next() {
		rec, err := scanSecurityFindingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning security finding: %w", err)
		}
		out = append(out, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing security findings: %w", err)
	}
	return out, nil
}

const getSecurityFindingSQL = `
	SELECT ` + securityFindingColumns + `
	FROM security_findings
	WHERE namespace = $1 AND id = $2`

func (s *Store) GetSecurityFinding(ctx context.Context, namespace string, id uuid.UUID) (*store.SecurityFindingRecord, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, getSecurityFindingSQL, namespace, id)
	rec, err := scanSecurityFindingRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting security finding: %w", err)
	}
	return rec, nil
}

const setSecurityFindingStatusSQL = `
	UPDATE security_findings f SET status = $3
	FROM security_findings prev
	WHERE f.namespace = $1 AND f.id = $2 AND prev.id = f.id
	RETURNING prev.status`

func (s *Store) SetSecurityFindingStatus(ctx context.Context, namespace string, id uuid.UUID, status, actor, note string) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	if !store.ValidSecurityFindingStatus(status) {
		return fmt.Errorf("invalid security finding status %q", status)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning status update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var previous string
	err = tx.QueryRow(ctx, setSecurityFindingStatusSQL, namespace, id, status).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrSecurityFindingNotFound
	}
	if err != nil {
		return fmt.Errorf("updating security finding status: %w", err)
	}

	detail, err := json.Marshal(map[string]string{"from": previous, "to": status})
	if err != nil {
		return fmt.Errorf("encoding status detail: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO security_finding_events (finding_id, event_type, actor, note, detail)
		VALUES ($1, 'status_changed', $2, $3, $4)`, id, actor, note, detail); err != nil {
		return fmt.Errorf("recording status event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing status update: %w", err)
	}
	return nil
}

const listSecurityFindingEventsSQL = `
	SELECT e.id, e.finding_id, e.event_type, e.actor, e.note, e.detail, e.created_at
	FROM security_finding_events e
	JOIN security_findings f ON f.id = e.finding_id
	WHERE f.namespace = $1 AND e.finding_id = $2
	ORDER BY e.created_at DESC, e.id DESC
	LIMIT $3`

func (s *Store) ListSecurityFindingEvents(ctx context.Context, namespace string, id uuid.UUID, limit int32) ([]store.SecurityFindingEvent, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, listSecurityFindingEventsSQL, namespace, id, securityLimit(limit, 200, 1000))
	if err != nil {
		return nil, fmt.Errorf("listing security finding events: %w", err)
	}
	defer rows.Close()
	var out []store.SecurityFindingEvent
	for rows.Next() {
		var ev store.SecurityFindingEvent
		if err := rows.Scan(&ev.ID, &ev.FindingID, &ev.EventType, &ev.Actor, &ev.Note, &ev.Detail, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning security finding event: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing security finding events: %w", err)
	}
	return out, nil
}

// addSecurityFindingCommentSQL inserts the comment only when the finding
// exists in the caller's namespace, so a comment can never be attached to a
// foreign finding by guessing its UUID.
const addSecurityFindingCommentSQL = `
	INSERT INTO security_finding_events (finding_id, event_type, actor, note)
	SELECT f.id, 'comment', $3, $4
	FROM security_findings f
	WHERE f.namespace = $1 AND f.id = $2
	RETURNING id, finding_id, event_type, actor, note, detail, created_at`

func (s *Store) AddSecurityFindingComment(ctx context.Context, namespace string, id uuid.UUID, actor, body string) (*store.SecurityFindingEvent, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	var ev store.SecurityFindingEvent
	err := s.pool.QueryRow(ctx, addSecurityFindingCommentSQL, namespace, id, actor, body).
		Scan(&ev.ID, &ev.FindingID, &ev.EventType, &ev.Actor, &ev.Note, &ev.Detail, &ev.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrSecurityFindingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("adding security finding comment: %w", err)
	}
	return &ev, nil
}

// newSecurityFindingSummary returns a summary map pre-seeded with the fixed
// keys so callers can gate on them even when no findings match.
func newSecurityFindingSummary() map[string]int32 {
	return map[string]int32{
		"total": 0, "open": 0,
		"open_critical": 0, "open_high": 0, "open_medium": 0, "open_low": 0, "open_info": 0,
	}
}

// addSecurityFindingSummaryCount folds one (severity, status, count) group
// into the summary, maintaining the per-severity, total, open, and
// open_<severity> keys.
func addSecurityFindingSummaryCount(summary map[string]int32, severity, status string, count int32) {
	summary[severity] += count
	summary["total"] += count
	if status == store.SecurityFindingStatusOpen {
		summary["open"] += count
		summary["open_"+severity] += count
	}
}

func (s *Store) SummarizeSecurityFindings(ctx context.Context, namespace, scanName, runName string) (map[string]int32, error) {
	where := "WHERE namespace = $1 AND duplicate_of IS NULL"
	args := []any{namespace}
	if scanName != "" {
		args = append(args, scanName)
		where += fmt.Sprintf(" AND scan_name = $%d", len(args))
	}
	if runName != "" {
		args = append(args, runName)
		where += fmt.Sprintf(" AND run_name = $%d", len(args))
	}
	rows, err := s.pool.Query(ctx, `
		SELECT severity, status, COUNT(*)
		FROM security_findings
		`+where+`
		GROUP BY severity, status`, args...)
	if err != nil {
		return nil, fmt.Errorf("summarizing security findings: %w", err)
	}
	defer rows.Close()
	summary := newSecurityFindingSummary()
	for rows.Next() {
		var severity, status string
		var count int64
		if err := rows.Scan(&severity, &status, &count); err != nil {
			return nil, fmt.Errorf("scanning security summary: %w", err)
		}
		addSecurityFindingSummaryCount(summary, severity, status, int32(count))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("summarizing security findings: %w", err)
	}
	return summary, nil
}

const deleteSecurityFindingsByScanSQL = `
	DELETE FROM security_findings WHERE namespace = $1 AND scan_name = $2`

const deleteSecurityScansByScanSQL = `
	DELETE FROM security_scans WHERE namespace = $1 AND scan_name = $2`

// DeleteSecurityScanData removes every scan run, finding, and event for the
// named scan. It is called when a SecurityScan resource is deleted.
func (s *Store) DeleteSecurityScanData(ctx context.Context, namespace, scanName string) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning security scan delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Finding events cascade from findings; findings are deleted explicitly
	// (rather than relying on the scan_id cascade) so rows re-attributed to
	// a later run of the same scan are removed too.
	if _, err := tx.Exec(ctx, deleteSecurityFindingsByScanSQL, namespace, scanName); err != nil {
		return fmt.Errorf("deleting security findings: %w", err)
	}
	if _, err := tx.Exec(ctx, deleteSecurityScansByScanSQL, namespace, scanName); err != nil {
		return fmt.Errorf("deleting security scans: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing security scan delete: %w", err)
	}
	return nil
}

var _ store.SecurityFindingStore = (*Store)(nil)
