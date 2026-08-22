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

// ConfirmSecurityFindingWithVariantSweep atomically confirms a finding and
// creates the pending variant sweep required by submission policy. The finding
// must already be bound to a durable research target and exact revision.
func (s *Store) ConfirmSecurityFindingWithVariantSweep(ctx context.Context, namespace string, findingID uuid.UUID, actor, note string) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	if findingID == uuid.Nil || strings.TrimSpace(actor) == "" {
		return errors.New("finding and actor are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning atomic finding confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var previous, title, fingerprint string
	var revisionID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT f.status, f.title, f.fingerprint, r.id
		FROM security_findings f
		JOIN security_research_targets t ON t.namespace = f.namespace AND t.target_key = f.scan_name
		JOIN security_research_revisions r ON r.target_id = t.id AND r.revision = f.revision
		WHERE f.namespace = $1 AND f.id = $2
		FOR UPDATE OF f`, namespace, findingID).Scan(&previous, &title, &fingerprint, &revisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrSecurityResearchRevisionNotFound
	}
	if err != nil {
		return fmt.Errorf("locking finding research revision: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE security_findings
		SET status = 'confirmed', accepted_risk_expires_at = NULL,
			triaged_at = COALESCE(triaged_at, now())
		WHERE namespace = $1 AND id = $2`, namespace, findingID); err != nil {
		return fmt.Errorf("confirming security finding: %w", err)
	}
	detail, _ := json.Marshal(map[string]string{"from": previous, "to": store.SecurityFindingStatusConfirmed})
	if _, err := tx.Exec(ctx, `INSERT INTO security_finding_events
		(finding_id, event_type, actor, note, detail)
		VALUES ($1, 'status_changed', $2, $3, $4)`, findingID, actor, note, detail); err != nil {
		return fmt.Errorf("recording confirmation event: %w", err)
	}
	scope, _ := json.Marshal(map[string]any{
		"finding_fingerprint": fingerprint,
		"required":            true,
		"minimum_result":      "non_empty_evidence",
	})
	key := "confirmed-finding:" + findingID.String() + ":" + revisionID.String()
	if _, err := tx.Exec(ctx, `INSERT INTO security_research_variant_sweeps
		(revision_id, finding_id, root_cause, scope, status, result, idempotency_key)
		VALUES ($1, $2, $3, $4, 'pending', '{}'::jsonb, $5)
		ON CONFLICT (revision_id, idempotency_key) DO NOTHING`, revisionID, findingID, title, scope, key); err != nil {
		return fmt.Errorf("creating required variant sweep: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO security_research_variant_sweep_events
		(sweep_id, event_type, actor, detail, idempotency_key)
		SELECT s.id, 'created', $3, s.scope, 'create:' || s.idempotency_key
		FROM security_research_variant_sweeps s
		WHERE s.revision_id = $1 AND s.idempotency_key = $2
		ON CONFLICT (sweep_id, idempotency_key) DO NOTHING`, revisionID, key, actor); err != nil {
		return fmt.Errorf("recording required variant sweep: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing atomic finding confirmation: %w", err)
	}
	return nil
}
