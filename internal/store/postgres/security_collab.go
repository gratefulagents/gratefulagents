package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// setSecurityFindingAssigneeSQL updates the assignee only inside the
// caller's namespace and returns the previous value for the audit event.
const setSecurityFindingAssigneeSQL = `
	UPDATE security_findings f SET assignee = $3
	FROM security_findings prev
	WHERE f.namespace = $1 AND f.id = $2 AND prev.id = f.id
	RETURNING prev.assignee`

func (s *Store) SetSecurityFindingAssignee(ctx context.Context, namespace string, id uuid.UUID, assignee, actor string) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning assignee update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var previous string
	err = tx.QueryRow(ctx, setSecurityFindingAssigneeSQL, namespace, id, assignee).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrSecurityFindingNotFound
	}
	if err != nil {
		return fmt.Errorf("updating security finding assignee: %w", err)
	}
	detail, err := json.Marshal(map[string]string{"from": previous, "to": assignee})
	if err != nil {
		return fmt.Errorf("encoding assignee detail: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO security_finding_events (finding_id, event_type, actor, detail)
		VALUES ($1, 'assignee_changed', $2, $3)`, id, actor, detail); err != nil {
		return fmt.Errorf("recording assignee event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing assignee update: %w", err)
	}
	return nil
}

const setSecurityFindingTicketSQL = `
	UPDATE security_findings f SET ticket_url = $3, ticket_provider = $4
	FROM security_findings prev
	WHERE f.namespace = $1 AND f.id = $2 AND prev.id = f.id
	RETURNING prev.ticket_url, prev.ticket_provider`

func (s *Store) SetSecurityFindingTicket(ctx context.Context, namespace string, id uuid.UUID, ticketURL, provider, actor string) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	ticketURL = strings.TrimSpace(ticketURL)
	eventType := "ticket_unlinked"
	if ticketURL != "" {
		if err := store.ValidateSecurityTicketURL(ticketURL); err != nil {
			return err
		}
		eventType = "ticket_linked"
	} else {
		provider = ""
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning ticket update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var prevURL, prevProvider string
	err = tx.QueryRow(ctx, setSecurityFindingTicketSQL, namespace, id, ticketURL, provider).Scan(&prevURL, &prevProvider)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrSecurityFindingNotFound
	}
	if err != nil {
		return fmt.Errorf("updating security finding ticket: %w", err)
	}
	detail, err := json.Marshal(map[string]string{
		"from_url": prevURL, "from_provider": prevProvider,
		"url": ticketURL, "provider": provider,
	})
	if err != nil {
		return fmt.Errorf("encoding ticket detail: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO security_finding_events (finding_id, event_type, actor, detail)
		VALUES ($1, $2, $3, $4)`, id, eventType, actor, detail); err != nil {
		return fmt.Errorf("recording ticket event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing ticket update: %w", err)
	}
	return nil
}

// ExpireAcceptedRisks flips every expired accepted_risk finding in the
// namespace back to open, so the sweep is cheap and idempotent no matter how
// often it runs.
func (s *Store) ExpireAcceptedRisks(ctx context.Context, namespace string) (int32, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning accepted-risk sweep: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		UPDATE security_findings SET status = 'open', accepted_risk_expires_at = NULL
		WHERE namespace = $1
		  AND status = 'accepted_risk'
		  AND accepted_risk_expires_at IS NOT NULL
		  AND accepted_risk_expires_at <= now()
		RETURNING id`, namespace)
	if err != nil {
		return 0, fmt.Errorf("expiring accepted risks: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning expired accepted risk: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("expiring accepted risks: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	detail, err := json.Marshal(map[string]string{"from": store.SecurityFindingStatusAcceptedRisk, "to": store.SecurityFindingStatusOpen})
	if err != nil {
		return 0, fmt.Errorf("encoding expiry detail: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `
			INSERT INTO security_finding_events (finding_id, event_type, actor, detail)
			VALUES ($1, 'accepted_risk_expired', 'system', $2)`, id, detail); err != nil {
			return 0, fmt.Errorf("recording accepted-risk expiry event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing accepted-risk sweep: %w", err)
	}
	return int32(len(ids)), nil
}

func (s *Store) BulkUpdateSecurityFindings(ctx context.Context, namespace, scanName string, ids []uuid.UUID, upd store.SecurityFindingBulkUpdate) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("no finding ids given")
	}
	if upd.Status == nil && upd.Assignee == nil {
		return errors.New("bulk update changes nothing: status or assignee required")
	}
	if upd.Status != nil {
		if !store.ValidSecurityFindingStatus(*upd.Status) {
			return fmt.Errorf("invalid security finding status %q", *upd.Status)
		}
		if upd.AcceptedRiskExpiresAt != nil && *upd.Status != store.SecurityFindingStatusAcceptedRisk {
			return fmt.Errorf("accepted-risk expiry requires status %q", store.SecurityFindingStatusAcceptedRisk)
		}
	} else if upd.AcceptedRiskExpiresAt != nil {
		return fmt.Errorf("accepted-risk expiry requires status %q", store.SecurityFindingStatusAcceptedRisk)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning bulk update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, id := range ids {
		if err := s.bulkUpdateSecurityFindingTx(ctx, tx, namespace, scanName, id, upd); err != nil {
			var bulkErr *store.BulkSecurityFindingError
			if errors.As(err, &bulkErr) {
				return err
			}
			return &store.BulkSecurityFindingError{FindingID: id, Err: err}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing bulk update: %w", err)
	}
	return nil
}

// bulkUpdateSecurityFindingTx applies the bulk update to one finding inside
// the shared transaction, appending one audit event per changed aspect.
func (s *Store) bulkUpdateSecurityFindingTx(ctx context.Context, tx pgx.Tx, namespace, scanName string, id uuid.UUID, upd store.SecurityFindingBulkUpdate) error {
	var prevStatus, prevAssignee string
	err := tx.QueryRow(ctx, `
		SELECT status, assignee FROM security_findings
		WHERE namespace = $1 AND id = $2 AND ($3 = '' OR scan_name = $3)
		FOR UPDATE`, namespace, id, scanName).Scan(&prevStatus, &prevAssignee)
	if errors.Is(err, pgx.ErrNoRows) {
		return &store.BulkSecurityFindingError{FindingID: id, Err: store.ErrSecurityFindingNotFound}
	}
	if err != nil {
		return fmt.Errorf("locking finding: %w", err)
	}

	if upd.Status != nil {
		var expiry *time.Time
		if *upd.Status == store.SecurityFindingStatusAcceptedRisk {
			expiry = upd.AcceptedRiskExpiresAt
		}
		if _, err := tx.Exec(ctx, `
			UPDATE security_findings SET
				status = $3,
				accepted_risk_expires_at = $4,
				triaged_at = CASE WHEN $3 <> 'open' THEN COALESCE(triaged_at, now()) ELSE triaged_at END
			WHERE namespace = $1 AND id = $2`, namespace, id, *upd.Status, expiry); err != nil {
			return fmt.Errorf("updating status: %w", err)
		}
		detailMap := map[string]string{"from": prevStatus, "to": *upd.Status, "bulk": "true"}
		if expiry != nil {
			detailMap["accepted_risk_expires_at"] = expiry.UTC().Format(time.RFC3339)
		}
		detail, err := json.Marshal(detailMap)
		if err != nil {
			return fmt.Errorf("encoding status detail: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO security_finding_events (finding_id, event_type, actor, note, detail)
			VALUES ($1, 'status_changed', $2, $3, $4)`, id, upd.Actor, upd.Note, detail); err != nil {
			return fmt.Errorf("recording status event: %w", err)
		}
	}

	if upd.Assignee != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE security_findings SET assignee = $3
			WHERE namespace = $1 AND id = $2`, namespace, id, *upd.Assignee); err != nil {
			return fmt.Errorf("updating assignee: %w", err)
		}
		detail, err := json.Marshal(map[string]string{"from": prevAssignee, "to": *upd.Assignee, "bulk": "true"})
		if err != nil {
			return fmt.Errorf("encoding assignee detail: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO security_finding_events (finding_id, event_type, actor, note, detail)
			VALUES ($1, 'assignee_changed', $2, $3, $4)`, id, upd.Actor, upd.Note, detail); err != nil {
			return fmt.Errorf("recording assignee event: %w", err)
		}
	}
	return nil
}

func (s *Store) FinalizeSecurityScanBaseline(ctx context.Context, namespace, runName string) (int32, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return 0, err
	}
	scan, err := s.GetSecurityScan(ctx, namespace, runName)
	if err != nil {
		return 0, err
	}
	// Only a completed run defines a baseline: an aborted or in-flight run
	// must never mark findings resolved.
	if scan == nil || scan.CompletedAt == nil {
		return 0, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning baseline finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Never resolve against a stale run: when a newer run of the same scan
	// already recorded observations, this run no longer defines the
	// baseline.
	var newerRun bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM security_finding_observations o
			JOIN security_scans s2 ON s2.namespace = o.namespace AND s2.run_name = o.run_name
			WHERE o.namespace = $1 AND o.scan_name = $2 AND o.run_name <> $3
			  AND s2.created_at > $4
		)`, namespace, scan.ScanName, runName, scan.CreatedAt).Scan(&newerRun); err != nil {
		return 0, fmt.Errorf("checking newer runs: %w", err)
	}
	if newerRun {
		return 0, nil
	}

	runStart := scan.CreatedAt
	if scan.StartedAt != nil {
		runStart = *scan.StartedAt
	}
	rows, err := tx.Query(ctx, `
		UPDATE security_findings f SET baseline_state = 'resolved', resolved_at = now()
		WHERE f.namespace = $1 AND f.scan_name = $2
		  AND f.duplicate_of IS NULL
		  AND (f.baseline_state IS NULL OR f.baseline_state <> 'resolved')
		  AND f.first_seen_at < $4
		  AND NOT EXISTS (
			SELECT 1 FROM security_finding_observations o
			WHERE o.namespace = f.namespace AND o.scan_name = f.scan_name
			  AND o.repository = f.repository AND o.fingerprint = f.fingerprint
			  AND o.run_name = $3
		  )
		RETURNING f.id`, namespace, scan.ScanName, runName, runStart)
	if err != nil {
		return 0, fmt.Errorf("resolving absent findings: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning resolved finding: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("resolving absent findings: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	detail, err := json.Marshal(map[string]string{"run_name": runName, "scan_name": scan.ScanName})
	if err != nil {
		return 0, fmt.Errorf("encoding resolved detail: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `
			INSERT INTO security_finding_events (finding_id, event_type, actor, detail)
			VALUES ($1, 'resolved', 'system', $2)`, id, detail); err != nil {
			return 0, fmt.Errorf("recording resolved event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing baseline finalization: %w", err)
	}
	return int32(len(ids)), nil
}

// requireSecuritySavedFilterScope rejects empty owner scoping so one user's
// saved filters can never leak into another's list.
func requireSecuritySavedFilterScope(namespace, owner string) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	if owner == "" {
		return errors.New("owner is required")
	}
	return nil
}

const securitySavedFilterColumns = `id, namespace, owner, name, query, created_at, updated_at`

func (s *Store) ListSecuritySavedFilters(ctx context.Context, namespace, owner string) ([]store.SecuritySavedFilter, error) {
	if err := requireSecuritySavedFilterScope(namespace, owner); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+securitySavedFilterColumns+`
		FROM security_saved_filters
		WHERE namespace = $1 AND owner = $2
		ORDER BY name`, namespace, owner)
	if err != nil {
		return nil, fmt.Errorf("listing security saved filters: %w", err)
	}
	defer rows.Close()
	var out []store.SecuritySavedFilter
	for rows.Next() {
		var rec store.SecuritySavedFilter
		if err := rows.Scan(&rec.ID, &rec.Namespace, &rec.Owner, &rec.Name, &rec.Query, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning security saved filter: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing security saved filters: %w", err)
	}
	return out, nil
}

func (s *Store) SaveSecuritySavedFilter(ctx context.Context, rec *store.SecuritySavedFilter) (*store.SecuritySavedFilter, error) {
	if err := requireSecuritySavedFilterScope(rec.Namespace, rec.Owner); err != nil {
		return nil, err
	}
	if strings.TrimSpace(rec.Name) == "" {
		return nil, errors.New("filter name is required")
	}
	query := rec.Query
	if len(query) == 0 {
		query = json.RawMessage("{}")
	}
	var out store.SecuritySavedFilter
	err := s.pool.QueryRow(ctx, `
		INSERT INTO security_saved_filters (namespace, owner, name, query)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (namespace, owner, name) DO UPDATE SET
			query = EXCLUDED.query,
			updated_at = now()
		RETURNING `+securitySavedFilterColumns,
		rec.Namespace, rec.Owner, rec.Name, query).
		Scan(&out.ID, &out.Namespace, &out.Owner, &out.Name, &out.Query, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("saving security saved filter: %w", err)
	}
	return &out, nil
}

func (s *Store) DeleteSecuritySavedFilter(ctx context.Context, namespace, owner, name string) error {
	if err := requireSecuritySavedFilterScope(namespace, owner); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM security_saved_filters
		WHERE namespace = $1 AND owner = $2 AND name = $3`, namespace, owner, name); err != nil {
		return fmt.Errorf("deleting security saved filter: %w", err)
	}
	return nil
}

func (s *Store) GetSecurityFindingTrends(ctx context.Context, namespace, scanName string, excludedScanNames []string) (*store.SecurityFindingTrends, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	where := "WHERE namespace = $1 AND duplicate_of IS NULL"
	args := []any{namespace}
	if scanName != "" {
		args = append(args, scanName)
		where += fmt.Sprintf(" AND scan_name = $%d", len(args))
	}
	if len(excludedScanNames) > 0 {
		args = append(args, excludedScanNames)
		where += fmt.Sprintf(" AND NOT (scan_name = ANY($%d))", len(args))
	}
	var trends store.SecurityFindingTrends
	err := s.pool.QueryRow(ctx, `
		SELECT
			COUNT(triaged_at),
			COUNT(resolved_at),
			COALESCE(AVG(EXTRACT(EPOCH FROM (triaged_at - first_seen_at))) FILTER (WHERE triaged_at IS NOT NULL), 0),
			COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (triaged_at - first_seen_at)))
				FILTER (WHERE triaged_at IS NOT NULL), 0),
			COALESCE(AVG(EXTRACT(EPOCH FROM (resolved_at - first_seen_at))) FILTER (WHERE resolved_at IS NOT NULL), 0),
			COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (resolved_at - first_seen_at)))
				FILTER (WHERE resolved_at IS NOT NULL), 0)
		FROM security_findings
		`+where, args...).
		Scan(&trends.TriagedCount, &trends.ResolvedCount,
			&trends.AvgTimeToTriageSeconds, &trends.MedianTimeToTriageSeconds,
			&trends.AvgTimeToResolutionSeconds, &trends.MedianTimeToResolutionSeconds)
	if err != nil {
		return nil, fmt.Errorf("aggregating security finding trends: %w", err)
	}
	return &trends, nil
}

// securityAuditExportMax caps how many events one export can return.
const securityAuditExportMax = int32(10000)

func (s *Store) ExportSecurityFindingEvents(ctx context.Context, namespace, scanName string, limit int32) ([]store.SecurityFindingAuditRecord, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	if scanName == "" {
		return nil, errors.New("scan name is required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.finding_id, e.event_type, e.actor, e.note, e.detail, e.created_at,
			f.fingerprint, f.title, f.severity, f.status
		FROM security_finding_events e
		JOIN security_findings f ON f.id = e.finding_id
		WHERE f.namespace = $1 AND f.scan_name = $2
		ORDER BY e.created_at, e.id
		LIMIT $3`, namespace, scanName, securityLimit(limit, securityAuditExportMax, securityAuditExportMax))
	if err != nil {
		return nil, fmt.Errorf("exporting security finding events: %w", err)
	}
	defer rows.Close()
	var out []store.SecurityFindingAuditRecord
	for rows.Next() {
		var rec store.SecurityFindingAuditRecord
		if err := rows.Scan(&rec.Event.ID, &rec.Event.FindingID, &rec.Event.EventType, &rec.Event.Actor,
			&rec.Event.Note, &rec.Event.Detail, &rec.Event.CreatedAt,
			&rec.Fingerprint, &rec.Title, &rec.Severity, &rec.Status); err != nil {
			return nil, fmt.Errorf("scanning exported event: %w", err)
		}
		rec.FindingID = rec.Event.FindingID
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("exporting security finding events: %w", err)
	}
	return out, nil
}
