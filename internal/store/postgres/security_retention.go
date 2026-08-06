package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// defaultSecurityRetentionBatchLimit bounds one purge statement when the
// caller passes batchLimit <= 0.
const defaultSecurityRetentionBatchLimit = 200

// PurgeExpiredSecurityData applies the retention policy to one namespace's
// security data, one bounded batch per call. Each class runs its own
// namespace-scoped, LIMIT-bounded statement with a deterministic ORDER BY id,
// so a single call stays cheap and repeated calls resume where the previous
// batch stopped. Deletion classes remove rows; evidence and PoC classes
// redact the expired content in place (raw JSON key / attack_vector column)
// and append an audit event, so the finding row, its identity, and its audit
// history are preserved. Every statement's predicate becomes false for rows
// it already processed, which makes the whole sweep idempotent.
//
// Order within one batch: finding deletion first (rows past findingDays never
// need redaction), then evidence/PoC redaction, then report artifacts, then
// scan-run rows, then audit events. Report artifacts are purged BEFORE scan
// rows, and while a report purge is configured (reportDays > 0) a scan row is
// never deleted as long as its session still has report artifacts — exactly
// like findings pin their scan row. Together these guarantee a batch-limited,
// resumable sweep can never delete a scan row while its reports are still
// pending purge, so reports never become unreachable. As a second line of
// defense the report purge does not depend on the scan row at all: artifacts
// are namespace-scoped through their agent session and fall back to the
// artifact's own created_at when the scan row is already gone, so
// pre-existing orphans remain purgeable.
func (s *Store) PurgeExpiredSecurityData(
	ctx context.Context, namespace string, policy store.SecurityRetentionPolicy, batchLimit int,
) (store.SecurityRetentionCounts, bool, error) {
	var counts store.SecurityRetentionCounts
	if err := requireSecurityNamespace(namespace); err != nil {
		return counts, false, err
	}
	if policy.IsZero() {
		return counts, false, nil
	}
	if batchLimit <= 0 {
		batchLimit = defaultSecurityRetentionBatchLimit
	}
	now := time.Now().UTC()
	cutoff := func(days int32) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }
	moreWork := false
	run := func(days int32, what, sql string, out *int32, extra ...any) error {
		if days <= 0 {
			return nil
		}
		args := append([]any{namespace, cutoff(days), batchLimit}, extra...)
		tag, err := s.pool.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("purging expired security %s: %w", what, err)
		}
		n := int32(tag.RowsAffected()) //nolint:gosec // bounded by batchLimit
		if out != nil {
			*out += n
		}
		if int(n) >= batchLimit {
			moreWork = true
		}
		return nil
	}

	if err := run(policy.FindingDays, "findings", `
		WITH victims AS (
			SELECT id FROM security_findings
			WHERE namespace = $1 AND last_seen_at < $2
			ORDER BY id
			LIMIT $3
		)
		DELETE FROM security_findings f USING victims v WHERE f.id = v.id`,
		&counts.FindingsDeleted); err != nil {
		return counts, true, err
	}

	if err := run(policy.EvidenceDays, "finding evidence", `
		WITH victims AS (
			SELECT id FROM security_findings
			WHERE namespace = $1 AND last_seen_at < $2 AND raw ? 'evidence'
			ORDER BY id
			LIMIT $3
		), redacted AS (
			UPDATE security_findings f SET raw = f.raw - 'evidence'
			FROM victims v WHERE f.id = v.id
			RETURNING f.id
		)
		INSERT INTO security_finding_events (finding_id, event_type, actor, note)
		SELECT id, 'evidence_purged', 'retention-policy', 'evidence redacted in place by the retention policy'
		FROM redacted`,
		&counts.EvidenceRedacted); err != nil {
		return counts, true, err
	}

	if err := run(policy.PoCDays, "finding PoC content", `
		WITH victims AS (
			SELECT id FROM security_findings
			WHERE namespace = $1 AND last_seen_at < $2
				AND (attack_vector <> '' OR raw ? 'attack_vector')
			ORDER BY id
			LIMIT $3
		), redacted AS (
			UPDATE security_findings f SET attack_vector = '', raw = f.raw - 'attack_vector'
			FROM victims v WHERE f.id = v.id
			RETURNING f.id
		)
		INSERT INTO security_finding_events (finding_id, event_type, actor, note)
		SELECT id, 'poc_purged', 'retention-policy', 'proof-of-concept / attack-vector content redacted in place by the retention policy'
		FROM redacted`,
		&counts.PoCsRedacted); err != nil {
		return counts, true, err
	}

	if err := run(policy.ReportDays, "report artifacts", `
		WITH victims AS (
			SELECT DISTINCT a.id FROM agent_artifacts a
			JOIN agent_sessions sess ON sess.id = a.session_id
			LEFT JOIN security_scans s ON s.session_id = a.session_id
			WHERE sess.agentrun_ns = $1
				AND a.kind IN ('security_report', 'security_sarif')
				AND COALESCE(s.completed_at, s.created_at, a.created_at) < $2
			ORDER BY a.id
			LIMIT $3
		)
		DELETE FROM agent_artifacts a USING victims v WHERE a.id = v.id`,
		&counts.ReportsDeleted); err != nil {
		return counts, true, err
	}

	if err := run(policy.ScanDays, "scan runs", `
		WITH victims AS (
			SELECT s.id FROM security_scans s
			WHERE s.namespace = $1 AND COALESCE(s.completed_at, s.created_at) < $2
				AND NOT EXISTS (SELECT 1 FROM security_findings f WHERE f.scan_id = s.id)
				AND NOT ($4 AND EXISTS (
					SELECT 1 FROM agent_artifacts a
					WHERE a.session_id = s.session_id
						AND a.kind IN ('security_report', 'security_sarif')))
			ORDER BY s.id
			LIMIT $3
		)
		DELETE FROM security_scans s USING victims v WHERE s.id = v.id`,
		&counts.ScansDeleted, policy.ReportDays > 0); err != nil {
		return counts, true, err
	}

	if err := run(policy.ScanDays, "scan observations", `
		WITH victims AS (
			SELECT id FROM security_finding_observations
			WHERE namespace = $1 AND observed_at < $2
			ORDER BY id
			LIMIT $3
		)
		DELETE FROM security_finding_observations o USING victims v WHERE o.id = v.id`,
		&counts.ScansDeleted); err != nil {
		return counts, true, err
	}

	if err := run(policy.AuditEventDays, "audit events", `
		WITH victims AS (
			SELECT e.id FROM security_finding_events e
			JOIN security_findings f ON f.id = e.finding_id
			WHERE f.namespace = $1 AND e.created_at < $2
			ORDER BY e.id
			LIMIT $3
		)
		DELETE FROM security_finding_events e USING victims v WHERE e.id = v.id`,
		&counts.AuditEventsDeleted); err != nil {
		return counts, true, err
	}

	return counts, moreWork, nil
}
