package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// ReopenBlockedSecurityResearchHypotheses moves every blocked hypothesis of
// the revision back to investigating so a new execution re-examines leads
// that an earlier environment failure could not test. Each change is audited
// as a "reopened" hypothesis event with an idempotency key derived from the
// version it superseded, so replaying the same reopen is a no-op.
func (s *Store) ReopenBlockedSecurityResearchHypotheses(ctx context.Context, namespace string, revisionID uuid.UUID) (int, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return 0, err
	}
	if revisionID == uuid.Nil {
		return 0, fmt.Errorf("revision id is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning blocked hypothesis reopen: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `UPDATE security_research_hypotheses h
		SET status = $3, result = $4, version = h.version + 1
		FROM security_research_revisions r
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE h.revision_id = r.id AND t.namespace = $1 AND r.id = $2 AND h.status = $5
		RETURNING h.id, h.version`,
		namespace, revisionID, store.SecurityHypothesisInvestigating, store.SecurityHypothesisResultPending, store.SecurityHypothesisBlocked)
	if err != nil {
		return 0, fmt.Errorf("reopening blocked hypotheses: %w", err)
	}
	type reopened struct {
		id      uuid.UUID
		version int32
	}
	var changed []reopened
	for rows.Next() {
		var item reopened
		if err := rows.Scan(&item.id, &item.version); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning reopened hypothesis: %w", err)
		}
		changed = append(changed, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("reading reopened hypotheses: %w", err)
	}
	for _, item := range changed {
		if _, err := tx.Exec(ctx, `INSERT INTO security_research_hypothesis_events
			(hypothesis_id, event_type, from_status, to_status, result, actor, rationale, detail, hypothesis_version, idempotency_key)
			VALUES ($1, 'reopened', $2, $3, $4, $5, $6, '{}', $7, $8)`,
			item.id, store.SecurityHypothesisBlocked, store.SecurityHypothesisInvestigating, store.SecurityHypothesisResultPending,
			"securityscan-controller", "a new execution started for this revision; blocked hypotheses are re-examined",
			item.version, fmt.Sprintf("reopen-blocked:%d", item.version-1)); err != nil {
			return 0, fmt.Errorf("recording hypothesis reopen: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing blocked hypothesis reopen: %w", err)
	}
	return len(changed), nil
}
