package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
	if f.ScanID != uuid.Nil {
		add("scan_id = $%d", f.ScanID)
	}
	if f.ScanName != "" {
		add("scan_name = $%d", f.ScanName)
	}
	if f.RunName != "" {
		add("run_name = $%d", f.RunName)
	}
	if f.ExecutionID != "" {
		add("execution_id = $%d", f.ExecutionID)
	}
	if f.TaskName != "" {
		add("task_name = $%d", f.TaskName)
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
		if f.Status == store.SecurityFindingStatusActionable {
			args = append(args, []string{
				store.SecurityFindingStatusOpen,
				store.SecurityFindingStatusTriaged,
				store.SecurityFindingStatusConfirmed,
			})
			conds = append(conds, fmt.Sprintf("status = ANY($%d)", len(args)))
		} else {
			add("status = $%d", f.Status)
		}
	}
	if f.Search != "" {
		args = append(args, "%"+escapeSecurityLike(f.Search)+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(title ILIKE $%d OR description ILIKE $%d OR file_path ILIKE $%d)", n, n, n))
	}
	if f.MinScore > 0 {
		add("score >= $%d", f.MinScore)
	}
	if f.BaselineState != "" {
		add("baseline_state = $%d", f.BaselineState)
	}
	if f.Assignee != "" {
		add("assignee = $%d", f.Assignee)
	}
	if len(f.ExcludedScanNames) > 0 {
		add("NOT (scan_name = ANY($%d))", f.ExcludedScanNames)
	}
	switch f.Suppressed {
	case store.SecuritySuppressedInclude:
	case store.SecuritySuppressedOnly:
		conds = append(conds, "suppressed_by IS NOT NULL")
	default:
		conds = append(conds, "suppressed_by IS NULL")
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

func (s *Store) ListSecurityScans(ctx context.Context, namespace, scanName string, limit int32, excludedScanNames []string) ([]store.SecurityScanRecord, error) {
	where := "WHERE namespace = $1"
	args := []any{namespace}
	if scanName != "" {
		args = append(args, scanName)
		where += fmt.Sprintf(" AND scan_name = $%d", len(args))
	}
	if len(excludedScanNames) > 0 {
		args = append(args, excludedScanNames)
		where += fmt.Sprintf(" AND NOT (scan_name = ANY($%d))", len(args))
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
	scan_step, score, status, duplicate_of, occurrences, raw, first_seen_at, last_seen_at,
	assignee, accepted_risk_expires_at, ticket_url, ticket_provider, COALESCE(baseline_state, ''),
	resolved_at, triaged_at, COALESCE(suppressed_by, ''), COALESCE(suppressed_reason, ''),
	COALESCE(suppressed_owner, ''), suppression_expires_at, suppressed_at,
	source_kind, tool, tool_version, rule_id, correlated_fingerprints, execution_id, task_name`

func scanSecurityFindingRow(row pgx.Row) (*store.SecurityFindingRecord, error) {
	var rec store.SecurityFindingRecord
	dest := make([]any, 0, 51)
	dest = append(dest, &rec.ID, &rec.ScanID, &rec.Namespace, &rec.ScanName, &rec.RunName, &rec.SessionID,
		&rec.Fingerprint, &rec.Title, &rec.Category, &rec.Severity, &rec.Confidence, &rec.Repository,
		&rec.Revision, &rec.FilePath, &rec.StartLine, &rec.EndLine, &rec.Symbol, &rec.CWE,
		&rec.Description, &rec.Impact, &rec.AttackVector, &rec.Remediation, &rec.References,
		&rec.SourceAgent, &rec.ScanStep, &rec.Score, &rec.Status, &rec.DuplicateOf,
		&rec.Occurrences, &rec.Raw, &rec.FirstSeenAt, &rec.LastSeenAt,
		&rec.Assignee, &rec.AcceptedRiskExpiresAt, &rec.TicketURL, &rec.TicketProvider,
		&rec.BaselineState, &rec.ResolvedAt, &rec.TriagedAt,
		&rec.SuppressedBy, &rec.SuppressedReason, &rec.SuppressedOwner,
		&rec.SuppressionExpiresAt, &rec.SuppressedAt,
		&rec.SourceKind, &rec.Tool, &rec.ToolVersion, &rec.RuleID, &rec.CorrelatedFingerprints,
		&rec.ExecutionID, &rec.TaskName)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return &rec, nil
}

// securitySeverityRank mirrors securitySeverityRankSQL in Go for merge
// decisions made outside SQL.
func securitySeverityRank(s string) int {
	switch s {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	case "info":
		return 0
	}
	return -1
}

func (s *Store) UpsertSecurityFinding(ctx context.Context, rec *store.SecurityFindingRecord) (*store.SecurityFindingRecord, bool, error) {
	status := rec.Status
	if status == "" {
		status = store.SecurityFindingStatusOpen
	}
	confidence := rec.Confidence
	if confidence == "" {
		confidence = "tentative"
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
	sourceKind := rec.SourceKind
	if sourceKind == "" {
		sourceKind = "agent"
	}
	correlated := rec.CorrelatedFingerprints
	if correlated == nil {
		correlated = []string{}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning finding upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	out, created, err := s.upsertSecurityFindingTx(ctx, tx, rec, status, confidence, occurrences, cwe, references, raw, sourceKind, correlated)
	if err != nil {
		return nil, false, err
	}

	// One observation row per report per run: the deterministic input for
	// scan-to-scan baseline comparison.
	if _, err := tx.Exec(ctx, `
		INSERT INTO security_finding_observations (namespace, scan_name, repository, fingerprint,
			run_name, revision, severity)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		rec.Namespace, rec.ScanName, rec.Repository, rec.Fingerprint,
		rec.RunName, rec.Revision, out.Severity); err != nil {
		return nil, false, fmt.Errorf("recording finding observation: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing finding upsert: %w", err)
	}
	return out, created, nil
}

const selectSecurityFindingForUpdateSQL = `
	SELECT ` + securityFindingColumns + `
	FROM security_findings
	WHERE namespace = $1 AND scan_name = $2 AND repository = $3 AND fingerprint = $4
	FOR UPDATE`

// upsertSecurityFindingTx locks the finding keyed by (namespace, scan_name,
// repository, fingerprint) and either inserts a fresh row (baseline "new")
// or merges the reobservation into it, classifying the baseline state and
// appending the matching audit event.
func (s *Store) upsertSecurityFindingTx(ctx context.Context, tx pgx.Tx, rec *store.SecurityFindingRecord,
	status, confidence string, occurrences int32, cwe, references []string, raw json.RawMessage,
	sourceKind string, correlated []string) (*store.SecurityFindingRecord, bool, error) {
	existing, err := scanSecurityFindingRow(tx.QueryRow(ctx, selectSecurityFindingForUpdateSQL,
		rec.Namespace, rec.ScanName, rec.Repository, rec.Fingerprint))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("locking security finding: %w", err)
	}

	if existing == nil {
		// ON CONFLICT DO NOTHING guards the race where a concurrent
		// transaction inserts the same key between our lock probe and this
		// insert: we then re-probe (blocking on their lock) and merge.
		row := tx.QueryRow(ctx, `
			INSERT INTO security_findings (scan_id, namespace, scan_name, run_name, session_id,
				fingerprint, title, category, severity, confidence, repository, revision, file_path,
				start_line, end_line, symbol, cwe, description, impact, attack_vector, remediation,
				references_urls, source_agent, scan_step, score, status, duplicate_of, occurrences,
				raw, baseline_state, source_kind, tool, tool_version, rule_id, correlated_fingerprints,
				execution_id, task_name)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
				$19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35,
				$36, $37)
			ON CONFLICT (namespace, scan_name, repository, fingerprint) DO NOTHING
			RETURNING `+securityFindingColumns,
			rec.ScanID, rec.Namespace, rec.ScanName, rec.RunName, rec.SessionID,
			rec.Fingerprint, rec.Title, rec.Category, rec.Severity, confidence,
			rec.Repository, rec.Revision, rec.FilePath, rec.StartLine, rec.EndLine,
			rec.Symbol, cwe, rec.Description, rec.Impact, rec.AttackVector, rec.Remediation,
			references, rec.SourceAgent, rec.ScanStep, rec.Score, status, rec.DuplicateOf,
			occurrences, raw, store.SecurityFindingBaselineNew,
			sourceKind, rec.Tool, rec.ToolVersion, rec.RuleID, correlated,
			rec.ExecutionID, rec.TaskName)
		out, err := scanSecurityFindingRow(row)
		if err == nil {
			return out, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("inserting security finding: %w", err)
		}
		existing, err = scanSecurityFindingRow(tx.QueryRow(ctx, selectSecurityFindingForUpdateSQL,
			rec.Namespace, rec.ScanName, rec.Repository, rec.Fingerprint))
		if err != nil {
			return nil, false, fmt.Errorf("re-locking security finding after insert race: %w", err)
		}
	}

	merge := classifySecurityFindingReobservation(existing, rec.Severity)

	// Provenance columns are refreshed from the reobservation (an identical
	// fingerprint implies the same source identity). execution_id and
	// task_name then behave differently, because the uniqueness key is global
	// across executions while every read path (listing, post-script matrix,
	// summaries, reports) is scoped to one execution:
	//   - execution_id is always re-stamped from the reporting run. A
	//     reobservation is a finding OF the current execution; keeping the
	//     first execution would update a row that the current execution can
	//     never see again, so every recurring finding would silently vanish
	//     from its report.
	//   - task_name keeps its first non-empty value only WITHIN the same
	//     execution: inside one execution a re-report from another task must
	//     not move the row off the task that created it. Once the row enters
	//     a new execution the old task attribution is meaningless, so it is
	//     re-stamped from the reporting run.
	// A reporter that carries no execution id cannot re-attribute anything,
	// so an empty incoming execution_id leaves both columns alone.
	// correlated_fingerprints is deliberately NOT touched: recorded
	// correlations survive reobservation and change only via
	// CorrelateSecurityFindings.
	row := tx.QueryRow(ctx, `
		UPDATE security_findings SET
			scan_id = $2,
			scan_name = $3,
			run_name = $4,
			session_id = $5,
			title = $6,
			category = $7,
			severity = $8,
			confidence = $9,
			revision = $10,
			file_path = $11,
			start_line = $12,
			end_line = $13,
			symbol = $14,
			cwe = $15,
			description = $16,
			impact = $17,
			attack_vector = $18,
			remediation = $19,
			references_urls = $20,
			source_agent = $21,
			scan_step = $22,
			score = GREATEST(score, $23),
			raw = $24,
			status = $25,
			baseline_state = $26,
			resolved_at = CASE WHEN $27 THEN NULL ELSE resolved_at END,
			accepted_risk_expires_at = CASE WHEN $28 THEN NULL ELSE accepted_risk_expires_at END,
			source_kind = $29,
			tool = $30,
			tool_version = $31,
			rule_id = $32,
			execution_id = CASE WHEN $33 <> '' THEN $33 ELSE security_findings.execution_id END,
			task_name = CASE
				WHEN $33 <> '' AND security_findings.execution_id IS DISTINCT FROM $33 THEN $34
				ELSE COALESCE(NULLIF(security_findings.task_name, ''), $34)
			END,
			occurrences = occurrences + 1,
			last_seen_at = now()
		WHERE id = $1
		RETURNING `+securityFindingColumns,
		existing.ID, rec.ScanID, rec.ScanName, rec.RunName, rec.SessionID,
		rec.Title, rec.Category, merge.severity, confidence, rec.Revision,
		rec.FilePath, rec.StartLine, rec.EndLine, rec.Symbol, cwe,
		rec.Description, rec.Impact, rec.AttackVector, rec.Remediation, references,
		rec.SourceAgent, rec.ScanStep, rec.Score, raw,
		merge.status, merge.baseline, merge.clearResolved, merge.clearExpiry,
		sourceKind, rec.Tool, rec.ToolVersion, rec.RuleID,
		rec.ExecutionID, rec.TaskName)
	out, err := scanSecurityFindingRow(row)
	if err != nil {
		return nil, false, fmt.Errorf("merging security finding: %w", err)
	}

	detail, err := json.Marshal(merge.eventDetail(rec.ScanName, rec.RunName))
	if err != nil {
		return nil, false, fmt.Errorf("encoding %s detail: %w", merge.eventType, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO security_finding_events (finding_id, event_type, actor, detail)
		VALUES ($1, $2, $3, $4)`, out.ID, merge.eventType, rec.SourceAgent, detail); err != nil {
		return nil, false, fmt.Errorf("recording %s event: %w", merge.eventType, err)
	}
	return out, false, nil
}

// securityFindingMerge is the outcome of classifying one reobservation of an
// existing finding.
type securityFindingMerge struct {
	severity      string
	status        string
	fromStatus    string
	fromSeverity  string
	baseline      string
	eventType     string
	clearResolved bool
	clearExpiry   bool
}

// classifySecurityFindingReobservation decides how a reobservation merges
// into the stored finding.
//
// "Materially changed" evidence is defined as a severity increase: the
// fingerprint pins the finding's evidence and location identity (a change in
// either produces a new fingerprint and therefore a new row), so under an
// identical fingerprint the only merge-visible material signal is severity.
//
// Rules:
//   - stored baseline resolved  -> reopened (resolved_at cleared)
//   - stored status fixed       -> regresses to open (evidence reappeared)
//   - false_positive / accepted_risk -> sticky, UNLESS the severity
//     increased, in which case the suppression regresses to open (the prior
//     decision stays in the audit history)
//   - anything else             -> recurring, status preserved
func classifySecurityFindingReobservation(existing *store.SecurityFindingRecord, incomingSeverity string) securityFindingMerge {
	m := securityFindingMerge{
		severity:     existing.Severity,
		status:       existing.Status,
		fromStatus:   existing.Status,
		fromSeverity: existing.Severity,
		baseline:     store.SecurityFindingBaselineRecurring,
		eventType:    "reobserved",
	}
	material := securitySeverityRank(incomingSeverity) > securitySeverityRank(existing.Severity)
	if material {
		m.severity = incomingSeverity
	}

	regressed := existing.Status == store.SecurityFindingStatusFixed ||
		((existing.Status == store.SecurityFindingStatusFalsePositive ||
			existing.Status == store.SecurityFindingStatusAcceptedRisk) && material)

	wasResolved := existing.BaselineState == store.SecurityFindingBaselineResolved || existing.ResolvedAt != nil
	if wasResolved {
		m.baseline = store.SecurityFindingBaselineReopened
		m.eventType = "reopened"
		m.clearResolved = true
	} else if regressed {
		m.baseline = store.SecurityFindingBaselineRegressed
		m.eventType = "regressed"
	}
	if regressed {
		m.status = store.SecurityFindingStatusOpen
		m.clearExpiry = existing.Status == store.SecurityFindingStatusAcceptedRisk
	}
	return m
}

// eventDetail builds the audit detail payload for the merge's event.
func (m securityFindingMerge) eventDetail(scanName, runName string) map[string]string {
	detail := map[string]string{"scan_name": scanName, "run_name": runName}
	if m.eventType == "reobserved" {
		return detail
	}
	detail["from_status"] = m.fromStatus
	detail["to_status"] = m.status
	if m.severity != m.fromSeverity {
		detail["severity_from"] = m.fromSeverity
		detail["severity_to"] = m.severity
		detail["reason"] = "severity_increased"
	}
	return detail
}

const selectSecurityFindingCorrelationSQL = `
	SELECT id, fingerprint, correlated_fingerprints
	FROM security_findings
	WHERE namespace = $1 AND scan_name = $2 AND repository = $3 AND fingerprint = $4
	FOR UPDATE`

// CorrelateSecurityFindings records a two-way cross-reference between the
// two findings: each side's correlated_fingerprints gains the other's
// fingerprint and a "correlated" event is appended for any side that
// changed. Neither row's content, provenance, or status is otherwise
// touched, so a correlated pair keeps both sources intact. Idempotent.
func (s *Store) CorrelateSecurityFindings(ctx context.Context, namespace, scanName, repository, fingerprintA, fingerprintB, reason, actor string) (bool, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return false, err
	}
	if fingerprintA == "" || fingerprintB == "" || fingerprintA == fingerprintB {
		return false, errors.New("two distinct fingerprints are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning finding correlation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	type side struct {
		id         uuid.UUID
		fp         string
		correlated []string
	}
	// Lock both rows in deterministic fingerprint order so concurrent
	// correlations of the same pair cannot deadlock.
	fps := []string{fingerprintA, fingerprintB}
	if fps[1] < fps[0] {
		fps[0], fps[1] = fps[1], fps[0]
	}
	sides := make([]side, 0, 2)
	for _, fp := range fps {
		var sd side
		sd.fp = fp
		err := tx.QueryRow(ctx, selectSecurityFindingCorrelationSQL,
			namespace, scanName, repository, fp).Scan(&sd.id, &sd.fp, &sd.correlated)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, store.ErrSecurityFindingNotFound
		}
		if err != nil {
			return false, fmt.Errorf("locking finding for correlation: %w", err)
		}
		sides = append(sides, sd)
	}

	changed := false
	for i, sd := range sides {
		other := sides[1-i]
		if slices.Contains(sd.correlated, other.fp) {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE security_findings
			SET correlated_fingerprints = array_append(correlated_fingerprints, $2)
			WHERE id = $1`, sd.id, other.fp); err != nil {
			return false, fmt.Errorf("recording finding correlation: %w", err)
		}
		detail, err := json.Marshal(map[string]string{
			"correlated_fingerprint": other.fp,
			"reason":                 reason,
			"scan_name":              scanName,
		})
		if err != nil {
			return false, fmt.Errorf("encoding correlation detail: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO security_finding_events (finding_id, event_type, actor, note, detail)
			VALUES ($1, 'correlated', $2, $3, $4)`, sd.id, actor, reason, detail); err != nil {
			return false, fmt.Errorf("recording correlation event: %w", err)
		}
		changed = true
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing finding correlation: %w", err)
	}
	return changed, nil
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
	UPDATE security_findings f SET
		status = $3,
		accepted_risk_expires_at = $4,
		triaged_at = CASE WHEN $3 <> 'open' THEN COALESCE(f.triaged_at, now()) ELSE f.triaged_at END
	FROM security_findings prev
	WHERE f.namespace = $1 AND f.id = $2 AND prev.id = f.id
	RETURNING prev.status`

func (s *Store) SetSecurityFindingStatus(ctx context.Context, namespace string, id uuid.UUID, status, actor, note string, acceptedRiskExpiresAt *time.Time) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	if !store.ValidSecurityFindingStatus(status) {
		return fmt.Errorf("invalid security finding status %q", status)
	}
	if acceptedRiskExpiresAt != nil && status != store.SecurityFindingStatusAcceptedRisk {
		return fmt.Errorf("accepted-risk expiry requires status %q, got %q", store.SecurityFindingStatusAcceptedRisk, status)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning status update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A non-accepted_risk status always clears a stored expiry so a stale
	// deadline can never re-open a finding that was re-triaged.
	var expiry *time.Time
	if status == store.SecurityFindingStatusAcceptedRisk {
		expiry = acceptedRiskExpiresAt
	}
	var previous string
	err = tx.QueryRow(ctx, setSecurityFindingStatusSQL, namespace, id, status, expiry).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrSecurityFindingNotFound
	}
	if err != nil {
		return fmt.Errorf("updating security finding status: %w", err)
	}

	detailMap := map[string]string{"from": previous, "to": status}
	if expiry != nil {
		detailMap["accepted_risk_expires_at"] = expiry.UTC().Format(time.RFC3339)
	}
	detail, err := json.Marshal(detailMap)
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
		"total": 0, "open": 0, "actionable": 0, "suppressed": 0,
		"open_critical": 0, "open_high": 0, "open_medium": 0, "open_low": 0, "open_info": 0,
		"actionable_critical": 0, "actionable_high": 0, "actionable_medium": 0, "actionable_low": 0, "actionable_info": 0,
		"baseline_new": 0, "baseline_recurring": 0, "baseline_regressed": 0,
		"baseline_resolved": 0, "baseline_reopened": 0, "baseline_tracked": 0,
		"source_agent": 0, "source_scanner": 0, "correlated": 0,
	}
}

// addSecurityFindingSummaryCount folds one (severity, status, baseline,
// sourceKind, correlated, suppressed, count) group into the summary.
// Suppressed findings only bump the "suppressed" key unless
// includeSuppressed is true, in which case they are folded into every count
// as well.
func addSecurityFindingSummaryCount(summary map[string]int32, severity, status, baseline, sourceKind string, correlated, suppressed bool, includeSuppressed bool, count int32) {
	if suppressed {
		summary["suppressed"] += count
		if !includeSuppressed {
			return
		}
	}
	summary[severity] += count
	summary["total"] += count
	if status == store.SecurityFindingStatusOpen {
		summary["open"] += count
		summary["open_"+severity] += count
	}
	if store.SecurityFindingIsActionable(status) {
		summary["actionable"] += count
		summary["actionable_"+severity] += count
	}
	if baseline != "" {
		summary["baseline_"+baseline] += count
		summary["baseline_tracked"] += count
	}
	if sourceKind == "scanner" {
		summary["source_scanner"] += count
	} else {
		summary["source_agent"] += count
	}
	if correlated {
		summary["correlated"] += count
	}
}

func (s *Store) SummarizeSecurityFindings(ctx context.Context, namespace, scanName, runName string, includeSuppressed bool) (map[string]int32, error) {
	return s.SummarizeSecurityFindingsScoped(ctx, store.SecurityFindingSummaryScope{
		Namespace: namespace, ScanName: scanName, RunName: runName,
		IncludeSuppressed: includeSuppressed,
	})
}

func (s *Store) SummarizeSecurityFindingsScoped(ctx context.Context, scope store.SecurityFindingSummaryScope) (map[string]int32, error) {
	where := "WHERE namespace = $1 AND duplicate_of IS NULL"
	args := []any{scope.Namespace}
	narrow := func(column, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		where += fmt.Sprintf(" AND %s = $%d", column, len(args))
	}
	if scope.ScanID != uuid.Nil {
		args = append(args, scope.ScanID)
		where += fmt.Sprintf(" AND scan_id = $%d", len(args))
	}
	narrow("scan_name", scope.ScanName)
	narrow("run_name", scope.RunName)
	narrow("execution_id", scope.ExecutionID)
	narrow("task_name", scope.TaskName)
	if len(scope.ExcludedScanNames) > 0 {
		args = append(args, scope.ExcludedScanNames)
		where += fmt.Sprintf(" AND NOT (scan_name = ANY($%d))", len(args))
	}
	rows, err := s.pool.Query(ctx, `
		SELECT severity, status, COALESCE(baseline_state, ''), source_kind,
			cardinality(correlated_fingerprints) > 0, suppressed_by IS NOT NULL, COUNT(*)
		FROM security_findings
		`+where+`
		GROUP BY severity, status, baseline_state, source_kind,
			cardinality(correlated_fingerprints) > 0, suppressed_by IS NOT NULL`, args...)
	if err != nil {
		return nil, fmt.Errorf("summarizing security findings: %w", err)
	}
	defer rows.Close()
	summary := newSecurityFindingSummary()
	for rows.Next() {
		var severity, status, baseline, sourceKind string
		var correlated, suppressed bool
		var count int64
		if err := rows.Scan(&severity, &status, &baseline, &sourceKind, &correlated, &suppressed, &count); err != nil {
			return nil, fmt.Errorf("scanning security summary: %w", err)
		}
		addSecurityFindingSummaryCount(summary, severity, status, baseline, sourceKind, correlated, suppressed, scope.IncludeSuppressed, int32(count))
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
	// Research targets are keyed by the SecurityScan name and follow the same
	// deletion lifecycle. Delete them before findings so retained dossiers can
	// never be rebound to a later scan that reuses the name.
	if _, err := tx.Exec(ctx, `DELETE FROM security_research_targets WHERE namespace = $1 AND target_key = $2`, namespace, scanName); err != nil {
		return fmt.Errorf("deleting security research: %w", err)
	}
	// Finding events cascade from findings; findings are deleted explicitly
	// (rather than relying on the scan_id cascade) so rows re-attributed to
	// a later run of the same scan are removed too.
	if _, err := tx.Exec(ctx, deleteSecurityFindingsByScanSQL, namespace, scanName); err != nil {
		return fmt.Errorf("deleting security findings: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM security_finding_observations WHERE namespace = $1 AND scan_name = $2`, namespace, scanName); err != nil {
		return fmt.Errorf("deleting security finding observations: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM security_notification_markers WHERE namespace = $1 AND scan_name = $2`, namespace, scanName); err != nil {
		return fmt.Errorf("deleting security notification markers: %w", err)
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

// ClaimSecurityNotifications inserts markers for the fingerprints and
// returns the ones newly claimed. Already-marked fingerprints are skipped, so
// a finding never notifies twice for the same rule/channel.
func (s *Store) ClaimSecurityNotifications(ctx context.Context, namespace, scanName, ruleKey string, fingerprints []string) ([]string, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	if len(fingerprints) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		INSERT INTO security_notification_markers (namespace, scan_name, rule_key, fingerprint)
		SELECT $1, $2, $3, fp FROM unnest($4::text[]) AS fp
		ON CONFLICT DO NOTHING
		RETURNING fingerprint`, namespace, scanName, ruleKey, fingerprints)
	if err != nil {
		return nil, fmt.Errorf("claiming security notification markers: %w", err)
	}
	defer rows.Close()
	var claimed []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, fmt.Errorf("scanning claimed notification marker: %w", err)
		}
		claimed = append(claimed, fp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading claimed notification markers: %w", err)
	}
	return claimed, nil
}

// ReleaseSecurityNotifications removes markers so a failed delivery can be
// retried. Idempotent.
func (s *Store) ReleaseSecurityNotifications(ctx context.Context, namespace, scanName, ruleKey string, fingerprints []string) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	if len(fingerprints) == 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM security_notification_markers
		WHERE namespace = $1 AND scan_name = $2 AND rule_key = $3 AND fingerprint = ANY($4::text[])`,
		namespace, scanName, ruleKey, fingerprints); err != nil {
		return fmt.Errorf("releasing security notification markers: %w", err)
	}
	return nil
}
