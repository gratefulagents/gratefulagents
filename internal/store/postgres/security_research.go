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

var _ store.SecurityResearchStore = (*Store)(nil)

type securityResearchScanner interface {
	Scan(...any) error
}

func researchJSON(value json.RawMessage, fallback string) (json.RawMessage, error) {
	if len(value) == 0 {
		value = json.RawMessage(fallback)
	}
	if !json.Valid(value) {
		return nil, errors.New("security research value must be valid JSON")
	}
	return value, nil
}

func scanSecurityResearchTarget(row securityResearchScanner) (*store.SecurityResearchTarget, error) {
	var value store.SecurityResearchTarget
	var metadata []byte
	if err := row.Scan(&value.ID, &value.Namespace, &value.TargetKey, &value.Kind, &value.Locator, &metadata, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	value.Metadata = metadata
	return &value, nil
}

func scanSecurityResearchRevision(row securityResearchScanner) (*store.SecurityResearchRevision, error) {
	var value store.SecurityResearchRevision
	var metadata []byte
	if err := row.Scan(&value.ID, &value.TargetID, &value.Revision, &value.SourceURI, &metadata, &value.CreatedAt); err != nil {
		return nil, err
	}
	value.Metadata = metadata
	return &value, nil
}

func (s *Store) UpsertSecurityResearchTarget(ctx context.Context, value *store.SecurityResearchTarget) (*store.SecurityResearchTarget, error) {
	if value == nil {
		return nil, errors.New("security research target is required")
	}
	if err := requireSecurityNamespace(value.Namespace); err != nil {
		return nil, err
	}
	value.TargetKey = strings.TrimSpace(value.TargetKey)
	value.Kind = strings.TrimSpace(value.Kind)
	value.Locator = strings.TrimSpace(value.Locator)
	if value.TargetKey == "" || value.Kind == "" || value.Locator == "" {
		return nil, errors.New("target key, kind, and locator are required")
	}
	metadata, err := researchJSON(value.Metadata, `{}`)
	if err != nil {
		return nil, err
	}
	stored, err := scanSecurityResearchTarget(s.pool.QueryRow(ctx, `
		INSERT INTO security_research_targets (namespace, target_key, kind, locator, metadata)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (namespace, target_key) DO UPDATE SET
			metadata = EXCLUDED.metadata
		WHERE security_research_targets.kind = EXCLUDED.kind
			AND security_research_targets.locator = EXCLUDED.locator
		RETURNING id, namespace, target_key, kind, locator, metadata, created_at, updated_at`,
		value.Namespace, value.TargetKey, value.Kind, value.Locator, metadata))
	if err != nil {
		return nil, fmt.Errorf("upserting security research target: %w", err)
	}
	return stored, nil
}

func (s *Store) BindSecurityResearchRevision(ctx context.Context, namespace string, value *store.SecurityResearchRevision) (*store.SecurityResearchRevision, bool, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, false, err
	}
	if value == nil || value.TargetID == uuid.Nil || strings.TrimSpace(value.Revision) == "" {
		return nil, false, errors.New("target id and exact revision are required")
	}
	value.Revision = strings.TrimSpace(value.Revision)
	metadata, err := researchJSON(value.Metadata, `{}`)
	if err != nil {
		return nil, false, err
	}
	command, err := s.pool.Exec(ctx, `
		INSERT INTO security_research_revisions (target_id, revision, source_uri, metadata)
		SELECT id, $3, $4, $5 FROM security_research_targets
		WHERE id = $1 AND namespace = $2
		ON CONFLICT (target_id, revision) DO NOTHING`, value.TargetID, namespace, value.Revision, value.SourceURI, metadata)
	if err != nil {
		return nil, false, fmt.Errorf("binding security research revision: %w", err)
	}
	if command.RowsAffected() == 0 {
		var targetExists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM security_research_targets WHERE id = $1 AND namespace = $2)`, value.TargetID, namespace).Scan(&targetExists); err != nil {
			return nil, false, fmt.Errorf("checking security research target: %w", err)
		}
		if !targetExists {
			return nil, false, store.ErrSecurityResearchTargetNotFound
		}
	}
	stored, err := scanSecurityResearchRevision(s.pool.QueryRow(ctx, `
		SELECT r.id, r.target_id, r.revision, r.source_uri, r.metadata, r.created_at
		FROM security_research_revisions r
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND r.target_id = $2 AND r.revision = $3`, namespace, value.TargetID, value.Revision))
	if err != nil {
		return nil, false, fmt.Errorf("reading security research revision binding: %w", err)
	}
	if strings.TrimSpace(stored.SourceURI) != strings.TrimSpace(value.SourceURI) {
		return nil, false, fmt.Errorf("exact revision is already bound to a different source URI")
	}
	// Reconcile findings confirmed before durable research was enabled. New
	// confirmations use ConfirmSecurityFindingWithVariantSweep atomically.
	if _, err := s.pool.Exec(ctx, `INSERT INTO security_research_variant_sweeps
		(revision_id, finding_id, root_cause, scope, status, result, idempotency_key)
		SELECT $1::uuid, f.id, f.title,
			jsonb_build_object('finding_fingerprint', f.fingerprint, 'required', true, 'reconciled', true),
			'pending', '{}'::jsonb, 'confirmed-finding:' || f.id::text || ':' || $1::uuid::text
		FROM security_findings f
		JOIN security_research_targets t ON t.id = $2
		WHERE f.namespace = $3 AND f.scan_name = t.target_key AND f.revision = $4 AND f.status = 'confirmed'
		ON CONFLICT (revision_id, idempotency_key) DO NOTHING`, stored.ID, stored.TargetID, namespace, stored.Revision); err != nil {
		return nil, false, fmt.Errorf("reconciling confirmed finding sweeps: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO security_research_variant_sweep_events
		(sweep_id, event_type, actor, detail, idempotency_key)
		SELECT s.id, 'created', 'system-reconciliation', s.scope, 'create:' || s.idempotency_key
		FROM security_research_variant_sweeps s
		WHERE s.revision_id = $1
		ON CONFLICT (sweep_id, idempotency_key) DO NOTHING`, stored.ID); err != nil {
		return nil, false, fmt.Errorf("recording reconciled finding sweeps: %w", err)
	}
	return stored, command.RowsAffected() == 1, nil
}

func (s *Store) GetSecurityResearchRevision(ctx context.Context, namespace, targetKey, revision string) (*store.SecurityResearchRevision, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	value, err := scanSecurityResearchRevision(s.pool.QueryRow(ctx, `
		SELECT r.id, r.target_id, r.revision, r.source_uri, r.metadata, r.created_at
		FROM security_research_revisions r
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND t.target_key = $2 AND r.revision = $3`, namespace, strings.TrimSpace(targetKey), strings.TrimSpace(revision)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting exact security research revision: %w", err)
	}
	return value, nil
}

func scanSecurityResearchDossier(row securityResearchScanner) (*store.SecurityResearchDossier, error) {
	var value store.SecurityResearchDossier
	var content []byte
	if err := row.Scan(&value.ID, &value.RevisionID, &value.Version, &value.ParentID, &content, &value.ChangeSummary, &value.Actor, &value.IdempotencyKey, &value.CreatedAt); err != nil {
		return nil, err
	}
	value.Content = content
	return &value, nil
}

const securityResearchDossierColumns = `d.id, d.revision_id, d.version, d.parent_id, d.content, d.change_summary, d.actor, d.idempotency_key, d.created_at`

func (s *Store) AmendSecurityResearchDossier(ctx context.Context, namespace string, value *store.SecurityResearchDossier) (*store.SecurityResearchDossier, bool, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, false, err
	}
	if value == nil || value.RevisionID == uuid.Nil || strings.TrimSpace(value.IdempotencyKey) == "" {
		return nil, false, errors.New("revision id and dossier idempotency key are required")
	}
	content, err := researchJSON(value.Content, `{}`)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning dossier amendment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanSecurityResearchDossier(tx.QueryRow(ctx, `SELECT `+securityResearchDossierColumns+`
		FROM security_research_dossiers d
		JOIN security_research_revisions r ON r.id = d.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND d.revision_id = $2 AND d.idempotency_key = $3`, namespace, value.RevisionID, strings.TrimSpace(value.IdempotencyKey)))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("checking dossier idempotency: %w", err)
	}

	var lockedRevision uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT r.id
		FROM security_research_revisions r JOIN security_research_targets t ON t.id = r.target_id
		WHERE r.id = $1 AND t.namespace = $2 FOR UPDATE`, value.RevisionID, namespace).Scan(&lockedRevision); errors.Is(err, pgx.ErrNoRows) {
		return nil, false, store.ErrSecurityResearchRevisionNotFound
	} else if err != nil {
		return nil, false, fmt.Errorf("locking dossier revision: %w", err)
	}
	existing, err = scanSecurityResearchDossier(tx.QueryRow(ctx, `SELECT `+securityResearchDossierColumns+`
		FROM security_research_dossiers d
		WHERE d.revision_id = $1 AND d.idempotency_key = $2`, value.RevisionID, strings.TrimSpace(value.IdempotencyKey)))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("rechecking dossier idempotency: %w", err)
	}
	var latestID uuid.UUID
	var latestVersion int32
	err = tx.QueryRow(ctx, `SELECT id, version FROM security_research_dossiers WHERE revision_id = $1 ORDER BY version DESC LIMIT 1`, value.RevisionID).Scan(&latestID, &latestVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		latestID = uuid.Nil
		latestVersion = 0
	} else if err != nil {
		return nil, false, fmt.Errorf("reading latest dossier: %w", err)
	}
	if value.ParentID != nil && (*value.ParentID != latestID || latestID == uuid.Nil) {
		return nil, false, store.ErrSecurityResearchVersionConflict
	}
	nextVersion := latestVersion + 1
	// Version is the caller's optimistic expected version, not the version to
	// assign to the new row. A caller that just read v1 sends 1 while this
	// transaction creates v2.
	if value.Version != 0 && value.Version != latestVersion {
		return nil, false, store.ErrSecurityResearchVersionConflict
	}
	var parentID *uuid.UUID
	if latestID != uuid.Nil {
		parentID = &latestID
	}
	stored, err := scanSecurityResearchDossier(tx.QueryRow(ctx, `
		INSERT INTO security_research_dossiers
			(revision_id, version, parent_id, content, change_summary, actor, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, revision_id, version, parent_id, content, change_summary, actor, idempotency_key, created_at`,
		value.RevisionID, nextVersion, parentID, content, value.ChangeSummary, value.Actor, strings.TrimSpace(value.IdempotencyKey)))
	if err != nil {
		return nil, false, fmt.Errorf("amending security research dossier: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing dossier amendment: %w", err)
	}
	return stored, true, nil
}

func (s *Store) GetLatestSecurityResearchDossier(ctx context.Context, namespace string, revisionID uuid.UUID) (*store.SecurityResearchDossier, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	value, err := scanSecurityResearchDossier(s.pool.QueryRow(ctx, `SELECT `+securityResearchDossierColumns+`
		FROM security_research_dossiers d
		JOIN security_research_revisions r ON r.id = d.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND d.revision_id = $2 ORDER BY d.version DESC LIMIT 1`, namespace, revisionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting latest security research dossier: %w", err)
	}
	return value, nil
}

func scanSecurityResearchHypothesis(row securityResearchScanner) (*store.SecurityResearchHypothesis, error) {
	var value store.SecurityResearchHypothesis
	var detail []byte
	if err := row.Scan(&value.ID, &value.RevisionID, &value.HypothesisKey, &value.Title, &value.Invariant, &value.Status, &value.Result, &detail, &value.Version, &value.IdempotencyKey, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	value.Detail = detail
	return &value, nil
}

const securityResearchHypothesisColumns = `h.id, h.revision_id, h.hypothesis_key, h.title, h.invariant, h.status, h.result, h.detail, h.version, h.idempotency_key, h.created_at, h.updated_at`

func (s *Store) CreateSecurityResearchHypothesis(ctx context.Context, namespace string, value *store.SecurityResearchHypothesis) (*store.SecurityResearchHypothesis, bool, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, false, err
	}
	if value == nil || value.RevisionID == uuid.Nil || strings.TrimSpace(value.HypothesisKey) == "" || strings.TrimSpace(value.Title) == "" || strings.TrimSpace(value.Invariant) == "" || strings.TrimSpace(value.Actor) == "" || strings.TrimSpace(value.IdempotencyKey) == "" {
		return nil, false, errors.New("revision, key, title, invariant, actor, and idempotency key are required")
	}
	if value.Status == "" {
		value.Status = store.SecurityHypothesisProposed
	}
	if value.Result == "" {
		value.Result = store.SecurityHypothesisResultPending
	}
	if value.Status != store.SecurityHypothesisProposed || value.Result != store.SecurityHypothesisResultPending {
		return nil, false, errors.New("a new hypothesis must be proposed with a pending result")
	}
	detail, err := researchJSON(value.Detail, `{}`)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning hypothesis creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	stored, err := scanSecurityResearchHypothesis(tx.QueryRow(ctx, `
		INSERT INTO security_research_hypotheses
			(revision_id, hypothesis_key, title, invariant, status, result, detail, idempotency_key)
		SELECT r.id, $3, $4, $5, $6, $7, $8, $9
		FROM security_research_revisions r JOIN security_research_targets t ON t.id = r.target_id
		WHERE r.id = $1 AND t.namespace = $2
		ON CONFLICT (revision_id, idempotency_key) DO NOTHING
		RETURNING id, revision_id, hypothesis_key, title, invariant, status, result, detail, version, idempotency_key, created_at, updated_at`,
		value.RevisionID, namespace, strings.TrimSpace(value.HypothesisKey), strings.TrimSpace(value.Title), strings.TrimSpace(value.Invariant), value.Status, value.Result, detail, strings.TrimSpace(value.IdempotencyKey)))
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		stored, err = scanSecurityResearchHypothesis(tx.QueryRow(ctx, `SELECT `+securityResearchHypothesisColumns+`
			FROM security_research_hypotheses h
			JOIN security_research_revisions r ON r.id = h.revision_id
			JOIN security_research_targets t ON t.id = r.target_id
			WHERE t.namespace = $1 AND h.revision_id = $2 AND h.idempotency_key = $3`, namespace, value.RevisionID, strings.TrimSpace(value.IdempotencyKey)))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, store.ErrSecurityResearchRevisionNotFound
		}
	}
	if err != nil {
		return nil, false, fmt.Errorf("creating security research hypothesis: %w", err)
	}
	if created {
		if _, err := tx.Exec(ctx, `INSERT INTO security_research_hypothesis_events
			(hypothesis_id, event_type, to_status, result, actor, detail, hypothesis_version, idempotency_key)
			VALUES ($1, 'created', $2, $3, $4, $5, 1, $6)`, stored.ID, stored.Status, stored.Result, strings.TrimSpace(value.Actor), detail, "create:"+stored.IdempotencyKey); err != nil {
			return nil, false, fmt.Errorf("recording hypothesis creation: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing hypothesis creation: %w", err)
	}
	return stored, created, nil
}

func validHypothesisResult(status, result string) bool {
	switch status {
	case store.SecurityHypothesisProposed, store.SecurityHypothesisInvestigating:
		return result == store.SecurityHypothesisResultPending
	case store.SecurityHypothesisSupported, store.SecurityHypothesisPromoted:
		return result == store.SecurityHypothesisResultPositive
	case store.SecurityHypothesisFalsified:
		return result == store.SecurityHypothesisResultNegative
	case store.SecurityHypothesisWeakened:
		return result == store.SecurityHypothesisResultPositive || result == store.SecurityHypothesisResultNegative || result == store.SecurityHypothesisResultInconclusive
	case store.SecurityHypothesisBlocked:
		return result == store.SecurityHypothesisResultFailed || result == store.SecurityHypothesisResultTimedOut || result == store.SecurityHypothesisResultInconclusive || result == store.SecurityHypothesisResultAbandoned
	case store.SecurityHypothesisSuperseded:
		return result == store.SecurityHypothesisResultAbandoned
	}
	return false
}

func validHypothesisTransition(from, to string) bool {
	if from == store.SecurityHypothesisProposed {
		return to == store.SecurityHypothesisInvestigating
	}
	if from == store.SecurityHypothesisInvestigating {
		switch to {
		case store.SecurityHypothesisSupported, store.SecurityHypothesisWeakened, store.SecurityHypothesisFalsified, store.SecurityHypothesisBlocked, store.SecurityHypothesisSuperseded:
			return true
		}
	}
	return from == store.SecurityHypothesisSupported && to == store.SecurityHypothesisPromoted
}

func (s *Store) TransitionSecurityResearchHypothesis(ctx context.Context, namespace string, id uuid.UUID, transition store.SecurityHypothesisTransition) (*store.SecurityResearchHypothesis, error) {
	return s.changeSecurityResearchHypothesis(ctx, namespace, id, transition, false)
}

func (s *Store) ReopenSecurityResearchHypothesis(ctx context.Context, namespace string, id uuid.UUID, transition store.SecurityHypothesisTransition) (*store.SecurityResearchHypothesis, error) {
	transition.ToStatus = store.SecurityHypothesisInvestigating
	transition.Result = store.SecurityHypothesisResultPending
	return s.changeSecurityResearchHypothesis(ctx, namespace, id, transition, true)
}

func (s *Store) changeSecurityResearchHypothesis(ctx context.Context, namespace string, id uuid.UUID, transition store.SecurityHypothesisTransition, reopen bool) (*store.SecurityResearchHypothesis, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	transition.IdempotencyKey = strings.TrimSpace(transition.IdempotencyKey)
	if id == uuid.Nil || transition.ExpectedVersion <= 0 || transition.IdempotencyKey == "" || strings.TrimSpace(transition.Rationale) == "" {
		return nil, errors.New("hypothesis, expected version, rationale, and idempotency key are required")
	}
	if !validHypothesisResult(transition.ToStatus, transition.Result) {
		return nil, store.ErrSecurityResearchInvalidTransition
	}
	detail, err := researchJSON(transition.Detail, `{}`)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning hypothesis transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var replay bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM security_research_hypothesis_events e
		JOIN security_research_hypotheses h ON h.id = e.hypothesis_id
		JOIN security_research_revisions r ON r.id = h.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND h.id = $2 AND e.idempotency_key = $3)`, namespace, id, transition.IdempotencyKey).Scan(&replay); err != nil {
		return nil, fmt.Errorf("checking hypothesis transition idempotency: %w", err)
	}
	if replay {
		return scanSecurityResearchHypothesis(tx.QueryRow(ctx, `SELECT `+securityResearchHypothesisColumns+`
			FROM security_research_hypotheses h JOIN security_research_revisions r ON r.id = h.revision_id
			JOIN security_research_targets t ON t.id = r.target_id WHERE t.namespace = $1 AND h.id = $2`, namespace, id))
	}
	current, err := scanSecurityResearchHypothesis(tx.QueryRow(ctx, `SELECT `+securityResearchHypothesisColumns+`
		FROM security_research_hypotheses h JOIN security_research_revisions r ON r.id = h.revision_id
		JOIN security_research_targets t ON t.id = r.target_id WHERE t.namespace = $1 AND h.id = $2 FOR UPDATE`, namespace, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrSecurityResearchHypothesisNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("locking security research hypothesis: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM security_research_hypothesis_events
		WHERE hypothesis_id = $1 AND idempotency_key = $2)`, id, transition.IdempotencyKey).Scan(&replay); err != nil {
		return nil, fmt.Errorf("rechecking hypothesis transition idempotency: %w", err)
	}
	if replay {
		return current, nil
	}
	if current.Version != transition.ExpectedVersion {
		return nil, store.ErrSecurityResearchVersionConflict
	}
	if reopen {
		switch current.Status {
		case store.SecurityHypothesisSupported, store.SecurityHypothesisWeakened, store.SecurityHypothesisFalsified, store.SecurityHypothesisBlocked, store.SecurityHypothesisSuperseded:
		default:
			return nil, store.ErrSecurityResearchInvalidTransition
		}
	} else if !validHypothesisTransition(current.Status, transition.ToStatus) {
		return nil, store.ErrSecurityResearchInvalidTransition
	}
	stored, err := scanSecurityResearchHypothesis(tx.QueryRow(ctx, `UPDATE security_research_hypotheses
		SET status = $3, result = $4, detail = $5, version = version + 1
		WHERE id = $1 AND version = $2
		RETURNING id, revision_id, hypothesis_key, title, invariant, status, result, detail, version, idempotency_key, created_at, updated_at`,
		id, transition.ExpectedVersion, transition.ToStatus, transition.Result, detail))
	if err != nil {
		return nil, fmt.Errorf("transitioning security research hypothesis: %w", err)
	}
	eventType := "transitioned"
	if reopen {
		eventType = "reopened"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO security_research_hypothesis_events
		(hypothesis_id, event_type, from_status, to_status, result, actor, rationale, detail, hypothesis_version, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, id, eventType, current.Status, stored.Status, stored.Result, transition.Actor, transition.Rationale, detail, stored.Version, transition.IdempotencyKey); err != nil {
		return nil, fmt.Errorf("recording hypothesis transition: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing hypothesis transition: %w", err)
	}
	return stored, nil
}

func (s *Store) AddSecurityResearchHypothesisLineage(ctx context.Context, namespace string, lineage store.SecurityHypothesisLineage, idempotencyKey string) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if lineage.ChildID == uuid.Nil || lineage.ParentID == uuid.Nil || lineage.ChildID == lineage.ParentID || idempotencyKey == "" {
		return errors.New("distinct child, parent, and idempotency key are required")
	}
	switch lineage.Relation {
	case "split_from", "merged_from", "derived_from":
	default:
		return errors.New("invalid hypothesis lineage relation")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning hypothesis lineage: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var replay bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM security_research_hypothesis_events e
		JOIN security_research_hypotheses h ON h.id = e.hypothesis_id
		JOIN security_research_revisions r ON r.id = h.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND h.id = $2 AND e.idempotency_key = $3)`, namespace, lineage.ChildID, idempotencyKey).Scan(&replay); err != nil {
		return fmt.Errorf("checking hypothesis lineage idempotency: %w", err)
	}
	if replay {
		return nil
	}
	var childRevision, parentRevision uuid.UUID
	var childStatus, childResult string
	var childVersion int32
	if err := tx.QueryRow(ctx, `SELECT h.revision_id, h.status, h.result, h.version
		FROM security_research_hypotheses h JOIN security_research_revisions r ON r.id = h.revision_id
		JOIN security_research_targets t ON t.id = r.target_id WHERE t.namespace = $1 AND h.id = $2`, namespace, lineage.ChildID).Scan(&childRevision, &childStatus, &childResult, &childVersion); errors.Is(err, pgx.ErrNoRows) {
		return store.ErrSecurityResearchHypothesisNotFound
	} else if err != nil {
		return fmt.Errorf("reading lineage child: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT h.revision_id FROM security_research_hypotheses h
		JOIN security_research_revisions r ON r.id = h.revision_id JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND h.id = $2`, namespace, lineage.ParentID).Scan(&parentRevision); errors.Is(err, pgx.ErrNoRows) {
		return store.ErrSecurityResearchHypothesisNotFound
	} else if err != nil {
		return fmt.Errorf("reading lineage parent: %w", err)
	}
	if childRevision != parentRevision {
		return errors.New("hypothesis lineage must stay within one exact revision")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 557))`, childRevision.String()); err != nil {
		return fmt.Errorf("locking hypothesis lineage graph: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM security_research_hypothesis_events
		WHERE hypothesis_id = $1 AND idempotency_key = $2)`, lineage.ChildID, idempotencyKey).Scan(&replay); err != nil {
		return fmt.Errorf("rechecking hypothesis lineage idempotency: %w", err)
	}
	if replay {
		return nil
	}
	var cycle bool
	if err := tx.QueryRow(ctx, `WITH RECURSIVE ancestors(id) AS (
		SELECT $1::uuid
		UNION
		SELECT l.parent_id FROM security_research_hypothesis_lineage l
		JOIN ancestors a ON a.id = l.child_id
	)
	SELECT EXISTS(SELECT 1 FROM ancestors WHERE id = $2)`, lineage.ParentID, lineage.ChildID).Scan(&cycle); err != nil {
		return fmt.Errorf("checking hypothesis lineage cycle: %w", err)
	}
	if cycle {
		return store.ErrSecurityResearchLineageCycle
	}
	if _, err := tx.Exec(ctx, `INSERT INTO security_research_hypothesis_lineage (child_id, parent_id, relation)
		VALUES ($1, $2, $3) ON CONFLICT (child_id, parent_id) DO NOTHING`, lineage.ChildID, lineage.ParentID, lineage.Relation); err != nil {
		return fmt.Errorf("adding hypothesis lineage: %w", err)
	}
	detail, _ := json.Marshal(map[string]string{"parent_id": lineage.ParentID.String(), "relation": lineage.Relation})
	if _, err := tx.Exec(ctx, `INSERT INTO security_research_hypothesis_events
		(hypothesis_id, event_type, from_status, to_status, result, detail, hypothesis_version, idempotency_key)
		VALUES ($1, 'lineage_added', $2, $2, $3, $4, $5, $6)
		ON CONFLICT (hypothesis_id, idempotency_key) DO NOTHING`, lineage.ChildID, childStatus, childResult, detail, childVersion, idempotencyKey); err != nil {
		return fmt.Errorf("recording hypothesis lineage: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing hypothesis lineage: %w", err)
	}
	return nil
}

func (s *Store) ListSecurityResearchHypotheses(ctx context.Context, namespace string, revisionID uuid.UUID) ([]store.SecurityResearchHypothesis, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+securityResearchHypothesisColumns+`
		FROM security_research_hypotheses h JOIN security_research_revisions r ON r.id = h.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND h.revision_id = $2 ORDER BY h.created_at, h.id`, namespace, revisionID)
	if err != nil {
		return nil, fmt.Errorf("listing security research hypotheses: %w", err)
	}
	defer rows.Close()
	var values []store.SecurityResearchHypothesis
	for rows.Next() {
		value, err := scanSecurityResearchHypothesis(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning security research hypothesis: %w", err)
		}
		values = append(values, *value)
	}
	return values, rows.Err()
}

func scanSecurityResearchHypothesisEvent(row securityResearchScanner) (*store.SecurityResearchHypothesisEvent, error) {
	var value store.SecurityResearchHypothesisEvent
	var detail []byte
	if err := row.Scan(&value.ID, &value.HypothesisID, &value.EventType, &value.FromStatus, &value.ToStatus, &value.Result, &value.Actor, &value.Rationale, &detail, &value.HypothesisVersion, &value.IdempotencyKey, &value.CreatedAt); err != nil {
		return nil, err
	}
	value.Detail = detail
	return &value, nil
}

func (s *Store) ListSecurityResearchHypothesisEvents(ctx context.Context, namespace string, hypothesisID uuid.UUID) ([]store.SecurityResearchHypothesisEvent, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT e.id, e.hypothesis_id, e.event_type, e.from_status, e.to_status, e.result, e.actor, e.rationale, e.detail, e.hypothesis_version, e.idempotency_key, e.created_at
		FROM security_research_hypothesis_events e JOIN security_research_hypotheses h ON h.id = e.hypothesis_id
		JOIN security_research_revisions r ON r.id = h.revision_id JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND h.id = $2 ORDER BY e.id`, namespace, hypothesisID)
	if err != nil {
		return nil, fmt.Errorf("listing hypothesis events: %w", err)
	}
	defer rows.Close()
	var values []store.SecurityResearchHypothesisEvent
	for rows.Next() {
		value, err := scanSecurityResearchHypothesisEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning hypothesis event: %w", err)
		}
		values = append(values, *value)
	}
	return values, rows.Err()
}

func scanSecurityResearchCoverage(row securityResearchScanner) (*store.SecurityResearchCoverage, error) {
	var value store.SecurityResearchCoverage
	var bounds, evidence []byte
	if err := row.Scan(&value.ID, &value.RevisionID, &value.HypothesisID, &value.Dimension, &value.SubjectKey, &value.Verdict, &bounds, &evidence, &value.Actor, &value.IdempotencyKey, &value.CreatedAt); err != nil {
		return nil, err
	}
	value.Bounds = bounds
	value.Evidence = evidence
	return &value, nil
}

func (s *Store) RecordSecurityResearchCoverage(ctx context.Context, namespace string, value *store.SecurityResearchCoverage) (*store.SecurityResearchCoverage, bool, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, false, err
	}
	if value == nil || value.RevisionID == uuid.Nil || strings.TrimSpace(value.SubjectKey) == "" || strings.TrimSpace(value.IdempotencyKey) == "" {
		return nil, false, errors.New("revision, coverage subject, and idempotency key are required")
	}
	if !store.ValidSecurityCoverageDimension(value.Dimension) {
		return nil, false, errors.New("invalid coverage dimension")
	}
	if !store.ValidSecurityCoverageVerdict(value.Verdict) {
		return nil, false, errors.New("invalid coverage verdict")
	}
	bounds, err := researchJSON(value.Bounds, `{}`)
	if err != nil {
		return nil, false, err
	}
	evidence, err := researchJSON(value.Evidence, `[]`)
	if err != nil {
		return nil, false, err
	}
	stored, err := scanSecurityResearchCoverage(s.pool.QueryRow(ctx, `INSERT INTO security_research_coverage
		(revision_id, hypothesis_id, dimension, subject_key, verdict, bounds, evidence, actor, idempotency_key)
		SELECT r.id, $3, $4, $5, $6, $7, $8, $9, $10
		FROM security_research_revisions r JOIN security_research_targets t ON t.id = r.target_id
		WHERE r.id = $1 AND t.namespace = $2
		ON CONFLICT (revision_id, idempotency_key) DO NOTHING
		RETURNING id, revision_id, hypothesis_id, dimension, subject_key, verdict, bounds, evidence, actor, idempotency_key, created_at`,
		value.RevisionID, namespace, value.HypothesisID, value.Dimension, strings.TrimSpace(value.SubjectKey), value.Verdict, bounds, evidence, value.Actor, strings.TrimSpace(value.IdempotencyKey)))
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		stored, err = scanSecurityResearchCoverage(s.pool.QueryRow(ctx, `SELECT c.id, c.revision_id, c.hypothesis_id, c.dimension, c.subject_key, c.verdict, c.bounds, c.evidence, c.actor, c.idempotency_key, c.created_at
			FROM security_research_coverage c JOIN security_research_revisions r ON r.id = c.revision_id
			JOIN security_research_targets t ON t.id = r.target_id
			WHERE t.namespace = $1 AND c.revision_id = $2 AND c.idempotency_key = $3`, namespace, value.RevisionID, strings.TrimSpace(value.IdempotencyKey)))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, store.ErrSecurityResearchRevisionNotFound
		}
	}
	if err != nil {
		return nil, false, fmt.Errorf("recording security research coverage: %w", err)
	}
	return stored, created, nil
}

func (s *Store) ListSecurityResearchCoverage(ctx context.Context, namespace string, revisionID uuid.UUID) ([]store.SecurityResearchCoverage, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.revision_id, c.hypothesis_id, c.dimension, c.subject_key, c.verdict, c.bounds, c.evidence, c.actor, c.idempotency_key, c.created_at
		FROM security_research_coverage c JOIN security_research_revisions r ON r.id = c.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND c.revision_id = $2 ORDER BY c.created_at, c.id`, namespace, revisionID)
	if err != nil {
		return nil, fmt.Errorf("listing security research coverage: %w", err)
	}
	defer rows.Close()
	var values []store.SecurityResearchCoverage
	for rows.Next() {
		value, err := scanSecurityResearchCoverage(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning security research coverage: %w", err)
		}
		values = append(values, *value)
	}
	return values, rows.Err()
}

func requireNonEmptyResearchObject(raw json.RawMessage, field string) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || len(value) == 0 {
		return fmt.Errorf("%s must be a non-empty JSON object", field)
	}
	return nil
}

func scanSecurityResearchVariantSweep(row securityResearchScanner) (*store.SecurityResearchVariantSweep, error) {
	var value store.SecurityResearchVariantSweep
	var scope, result []byte
	if err := row.Scan(&value.ID, &value.RevisionID, &value.FindingID, &value.RootHypothesisID, &value.RootCause, &scope, &value.Status, &result, &value.IdempotencyKey, &value.CreatedAt, &value.CompletedAt); err != nil {
		return nil, err
	}
	value.Scope = scope
	value.Result = result
	return &value, nil
}

const securityResearchVariantSweepColumns = `s.id, s.revision_id, s.finding_id, s.root_hypothesis_id, s.root_cause, s.scope, s.status, s.result, s.idempotency_key, s.created_at, s.completed_at`

func (s *Store) CreateSecurityResearchVariantSweep(ctx context.Context, namespace string, value *store.SecurityResearchVariantSweep) (*store.SecurityResearchVariantSweep, bool, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, false, err
	}
	if value == nil || value.RevisionID == uuid.Nil || strings.TrimSpace(value.RootCause) == "" || strings.TrimSpace(value.Actor) == "" || strings.TrimSpace(value.IdempotencyKey) == "" {
		return nil, false, errors.New("revision, root cause, actor, and idempotency key are required")
	}
	if value.Status == "" {
		value.Status = store.SecurityVariantSweepPending
	}
	if value.Status != store.SecurityVariantSweepPending && value.Status != store.SecurityVariantSweepRunning {
		return nil, false, errors.New("a new variant sweep must be pending or running")
	}
	scope, err := researchJSON(value.Scope, `{}`)
	if err != nil {
		return nil, false, err
	}
	if err := requireNonEmptyResearchObject(scope, "variant sweep scope"); err != nil {
		return nil, false, err
	}
	result, err := researchJSON(value.Result, `{}`)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning variant sweep creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	stored, err := scanSecurityResearchVariantSweep(tx.QueryRow(ctx, `INSERT INTO security_research_variant_sweeps
		(revision_id, finding_id, root_hypothesis_id, root_cause, scope, status, result, idempotency_key)
		SELECT r.id, $3, $4, $5, $6, $7, $8, $9
		FROM security_research_revisions r
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE r.id = $1 AND t.namespace = $2
			AND ($3::uuid IS NULL OR EXISTS (
				SELECT 1 FROM security_findings f
				WHERE f.id = $3 AND f.namespace = $2
					AND f.scan_name = t.target_key AND f.revision = r.revision
			))
			AND ($4::uuid IS NULL OR EXISTS (
				SELECT 1 FROM security_research_hypotheses h
				WHERE h.id = $4 AND h.revision_id = r.id
			))
		ON CONFLICT (revision_id, idempotency_key) DO NOTHING
		RETURNING id, revision_id, finding_id, root_hypothesis_id, root_cause, scope, status, result, idempotency_key, created_at, completed_at`,
		value.RevisionID, namespace, value.FindingID, value.RootHypothesisID, strings.TrimSpace(value.RootCause), scope, value.Status, result, strings.TrimSpace(value.IdempotencyKey)))
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		stored, err = scanSecurityResearchVariantSweep(tx.QueryRow(ctx, `SELECT `+securityResearchVariantSweepColumns+`
			FROM security_research_variant_sweeps s
			JOIN security_research_revisions r ON r.id = s.revision_id
			JOIN security_research_targets t ON t.id = r.target_id
			WHERE t.namespace = $1 AND s.revision_id = $2 AND s.idempotency_key = $3`, namespace, value.RevisionID, strings.TrimSpace(value.IdempotencyKey)))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, store.ErrSecurityResearchRevisionNotFound
		}
	}
	if err != nil {
		return nil, false, fmt.Errorf("creating security research variant sweep: %w", err)
	}
	if created {
		if _, err := tx.Exec(ctx, `INSERT INTO security_research_variant_sweep_events
			(sweep_id, event_type, actor, detail, idempotency_key)
			VALUES ($1, 'created', $2, $3, $4)`, stored.ID, strings.TrimSpace(value.Actor), scope, "create:"+stored.IdempotencyKey); err != nil {
			return nil, false, fmt.Errorf("recording variant sweep creation: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing variant sweep creation: %w", err)
	}
	return stored, created, nil
}

func (s *Store) CompleteSecurityResearchVariantSweep(ctx context.Context, namespace string, sweepID uuid.UUID, status string, result json.RawMessage, actor, idempotencyKey string) (*store.SecurityResearchVariantSweep, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if sweepID == uuid.Nil || idempotencyKey == "" {
		return nil, errors.New("variant sweep and idempotency key are required")
	}
	if status != store.SecurityVariantSweepCompleted && status != store.SecurityVariantSweepBlocked {
		return nil, errors.New("variant sweep can only be completed or blocked")
	}
	result, err := researchJSON(result, `{}`)
	if err != nil {
		return nil, err
	}
	if err := requireNonEmptyResearchObject(result, "variant sweep result"); err != nil {
		return nil, err
	}
	if status == store.SecurityVariantSweepCompleted && !store.ValidSecurityVariantSweepCompletionEvidence(result) {
		return nil, errors.New("completed variant sweep requires non-empty searched_scope, methods, evidence, and summary")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning variant sweep completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanSecurityResearchVariantSweep(tx.QueryRow(ctx, `SELECT `+securityResearchVariantSweepColumns+`
		FROM security_research_variant_sweeps s
		JOIN security_research_revisions r ON r.id = s.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND s.id = $2 FOR UPDATE`, namespace, sweepID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrSecurityResearchSweepNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("locking variant sweep: %w", err)
	}
	var replay bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM security_research_variant_sweep_events WHERE sweep_id = $1 AND idempotency_key = $2)`, sweepID, idempotencyKey).Scan(&replay); err != nil {
		return nil, fmt.Errorf("checking variant sweep idempotency: %w", err)
	}
	if replay {
		return current, nil
	}
	if current.Status != store.SecurityVariantSweepPending && current.Status != store.SecurityVariantSweepRunning {
		return nil, errors.New("variant sweep is already final")
	}
	stored, err := scanSecurityResearchVariantSweep(tx.QueryRow(ctx, `UPDATE security_research_variant_sweeps
		SET status = $2, result = $3, completed_at = now()
		WHERE id = $1
		RETURNING id, revision_id, finding_id, root_hypothesis_id, root_cause, scope, status, result, idempotency_key, created_at, completed_at`, sweepID, status, result))
	if err != nil {
		return nil, fmt.Errorf("completing variant sweep: %w", err)
	}
	eventType := "completed"
	if status == store.SecurityVariantSweepBlocked {
		eventType = "blocked"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO security_research_variant_sweep_events
		(sweep_id, event_type, actor, detail, idempotency_key) VALUES ($1, $2, $3, $4, $5)`, sweepID, eventType, actor, result, idempotencyKey); err != nil {
		return nil, fmt.Errorf("recording variant sweep completion: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing variant sweep completion: %w", err)
	}
	return stored, nil
}

func (s *Store) ListSecurityResearchVariantSweeps(ctx context.Context, namespace string, revisionID uuid.UUID) ([]store.SecurityResearchVariantSweep, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+securityResearchVariantSweepColumns+`
		FROM security_research_variant_sweeps s
		JOIN security_research_revisions r ON r.id = s.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND s.revision_id = $2 ORDER BY s.created_at, s.id`, namespace, revisionID)
	if err != nil {
		return nil, fmt.Errorf("listing variant sweeps: %w", err)
	}
	defer rows.Close()
	var values []store.SecurityResearchVariantSweep
	for rows.Next() {
		value, err := scanSecurityResearchVariantSweep(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning variant sweep: %w", err)
		}
		values = append(values, *value)
	}
	return values, rows.Err()
}

func scanSecurityResearchVariantSweepEvent(row securityResearchScanner) (*store.SecurityResearchVariantSweepEvent, error) {
	var value store.SecurityResearchVariantSweepEvent
	var detail []byte
	if err := row.Scan(&value.ID, &value.SweepID, &value.EventType, &value.Actor, &detail, &value.IdempotencyKey, &value.CreatedAt); err != nil {
		return nil, err
	}
	value.Detail = detail
	return &value, nil
}

func (s *Store) ListSecurityResearchVariantSweepEvents(ctx context.Context, namespace string, sweepID uuid.UUID) ([]store.SecurityResearchVariantSweepEvent, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT e.id, e.sweep_id, e.event_type, e.actor, e.detail, e.idempotency_key, e.created_at
		FROM security_research_variant_sweep_events e
		JOIN security_research_variant_sweeps s ON s.id = e.sweep_id
		JOIN security_research_revisions r ON r.id = s.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND s.id = $2 ORDER BY e.id`, namespace, sweepID)
	if err != nil {
		return nil, fmt.Errorf("listing variant sweep events: %w", err)
	}
	defer rows.Close()
	var values []store.SecurityResearchVariantSweepEvent
	for rows.Next() {
		value, err := scanSecurityResearchVariantSweepEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning variant sweep event: %w", err)
		}
		values = append(values, *value)
	}
	return values, rows.Err()
}

func scanSecurityResearchSubmission(row securityResearchScanner) (*store.SecurityResearchSubmission, error) {
	var value store.SecurityResearchSubmission
	var payload []byte
	if err := row.Scan(&value.ID, &value.RevisionID, &value.TargetID, &value.FindingID, &value.Workflow, &value.CandidateKey, &value.Rank, &payload, &value.Status, &value.CreatedAt, &value.SubmittedAt); err != nil {
		return nil, err
	}
	value.Payload = payload
	return &value, nil
}

const securityResearchSubmissionColumns = `s.id, s.revision_id, s.target_id, s.finding_id, s.workflow, s.candidate_key, s.rank, s.payload, s.status, s.created_at, s.submitted_at`

func (s *Store) CreateSecurityResearchSubmission(ctx context.Context, namespace string, value *store.SecurityResearchSubmission) (*store.SecurityResearchSubmission, bool, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, false, err
	}
	if value == nil || value.RevisionID == uuid.Nil || strings.TrimSpace(value.Workflow) == "" || strings.TrimSpace(value.CandidateKey) == "" || value.Rank <= 0 {
		return nil, false, errors.New("revision, workflow, candidate key, and positive rank are required")
	}
	if value.Status == "" {
		value.Status = "candidate"
	}
	if value.Status != "candidate" {
		return nil, false, errors.New("a new submission must be a candidate")
	}
	payload, err := researchJSON(value.Payload, `{}`)
	if err != nil {
		return nil, false, err
	}
	stored, err := scanSecurityResearchSubmission(s.pool.QueryRow(ctx, `INSERT INTO security_research_submissions
		(revision_id, target_id, finding_id, workflow, candidate_key, rank, payload, status)
		SELECT r.id, r.target_id, $3, $4, $5, $6, $7, 'candidate'
		FROM security_research_revisions r
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE r.id = $1 AND t.namespace = $2
			AND ($3::uuid IS NULL OR EXISTS (SELECT 1 FROM security_findings f WHERE f.id = $3 AND f.namespace = $2))
		ON CONFLICT (revision_id, workflow, candidate_key) DO NOTHING
		RETURNING id, revision_id, target_id, finding_id, workflow, candidate_key, rank, payload, status, created_at, submitted_at`,
		value.RevisionID, namespace, value.FindingID, strings.TrimSpace(value.Workflow), strings.TrimSpace(value.CandidateKey), value.Rank, payload))
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		stored, err = scanSecurityResearchSubmission(s.pool.QueryRow(ctx, `SELECT `+securityResearchSubmissionColumns+`
			FROM security_research_submissions s
			JOIN security_research_targets t ON t.id = s.target_id
			WHERE t.namespace = $1 AND s.revision_id = $2 AND s.workflow = $3 AND s.candidate_key = $4`,
			namespace, value.RevisionID, strings.TrimSpace(value.Workflow), strings.TrimSpace(value.CandidateKey)))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, store.ErrSecurityResearchRevisionNotFound
		}
	}
	if err != nil {
		return nil, false, fmt.Errorf("creating security research submission: %w", err)
	}
	return stored, created, nil
}

func scanSecuritySubmissionReservation(row securityResearchScanner) (*store.SecuritySubmissionReservation, error) {
	var value store.SecuritySubmissionReservation
	if err := row.Scan(&value.ID, &value.SubmissionID, &value.TargetID, &value.Workflow, &value.PeriodDays, &value.BudgetLimit, &value.IdempotencyKey, &value.ReservedAt, &value.ExpiresAt, &value.VoidedAt); err != nil {
		return nil, err
	}
	return &value, nil
}

const securitySubmissionReservationColumns = `r.id, r.submission_id, r.target_id, r.workflow, r.period_days, r.budget_limit, r.idempotency_key, r.reserved_at, r.expires_at, r.voided_at`

//nolint:gocyclo // Reservation ownership, expiry, replay, and rolling-budget checks intentionally share one transaction.
func (s *Store) ReserveSecurityResearchSubmission(ctx context.Context, namespace string, request store.SecuritySubmissionReservationRequest) (*store.SecuritySubmissionReservationResult, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	request.Workflow = strings.TrimSpace(request.Workflow)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.SubmissionID == uuid.Nil || request.Workflow == "" || request.PeriodDays <= 0 || request.BudgetLimit <= 0 || request.IdempotencyKey == "" {
		return nil, errors.New("submission, workflow, positive window and limit, and idempotency key are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning submission reservation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var targetID uuid.UUID
	var status string
	if err := tx.QueryRow(ctx, `SELECT s.target_id, s.status
		FROM security_research_submissions s
		JOIN security_research_targets t ON t.id = s.target_id
		WHERE t.namespace = $1 AND s.id = $2 AND s.workflow = $3
		FOR UPDATE OF t, s`, namespace, request.SubmissionID, request.Workflow).Scan(&targetID, &status); errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrSecurityResearchSubmissionNotFound
	} else if err != nil {
		return nil, fmt.Errorf("locking submission reservation scope: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE security_research_submission_reservations r
		SET voided_at = now()
		FROM security_research_submissions s
		WHERE r.submission_id = s.id AND r.target_id = $1 AND r.workflow = $2
			AND r.voided_at IS NULL AND r.expires_at <= now() AND s.status = 'reserved'`, targetID, request.Workflow); err != nil {
		return nil, fmt.Errorf("expiring stale submission reservations: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE security_research_submissions s SET status = 'candidate'
		WHERE s.target_id = $1 AND s.workflow = $2 AND s.status = 'reserved'
			AND NOT EXISTS (SELECT 1 FROM security_research_submission_reservations r WHERE r.submission_id = s.id AND r.voided_at IS NULL)`, targetID, request.Workflow); err != nil {
		return nil, fmt.Errorf("releasing stale submission reservations: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT status FROM security_research_submissions WHERE id = $1`, request.SubmissionID).Scan(&status); err != nil {
		return nil, fmt.Errorf("refreshing submission reservation status: %w", err)
	}
	var used int32
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM security_research_submission_reservations
		WHERE target_id = $1 AND workflow = $2 AND voided_at IS NULL
			AND reserved_at > now() - make_interval(days => $3)`, targetID, request.Workflow, request.PeriodDays).Scan(&used); err != nil {
		return nil, fmt.Errorf("counting rolling submission reservations: %w", err)
	}
	existing, err := scanSecuritySubmissionReservation(tx.QueryRow(ctx, `SELECT `+securitySubmissionReservationColumns+`
		FROM security_research_submission_reservations r
		WHERE r.target_id = $1 AND r.workflow = $2 AND r.idempotency_key = $3`, targetID, request.Workflow, request.IdempotencyKey))
	if err == nil {
		if existing.SubmissionID != request.SubmissionID || existing.PeriodDays != request.PeriodDays || existing.BudgetLimit != request.BudgetLimit {
			return nil, store.ErrSecurityResearchReservationConflict
		}
		if existing.VoidedAt != nil {
			if status != "candidate" || used >= request.BudgetLimit {
				if err := tx.Commit(ctx); err != nil {
					return nil, fmt.Errorf("committing exhausted reservation retry: %w", err)
				}
				return &store.SecuritySubmissionReservationResult{Reserved: false, Used: used, Limit: request.BudgetLimit}, nil
			}
			existing, err = scanSecuritySubmissionReservation(tx.QueryRow(ctx, `UPDATE security_research_submission_reservations
				SET voided_at = NULL, reserved_at = now(), expires_at = now() + interval '24 hours'
				WHERE id = $1 AND voided_at IS NOT NULL
				RETURNING id, submission_id, target_id, workflow, period_days, budget_limit, idempotency_key, reserved_at, expires_at, voided_at`, existing.ID))
			if err != nil {
				return nil, fmt.Errorf("reactivating reservation retry: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE security_research_submissions SET status = 'reserved' WHERE id = $1 AND status = 'candidate'`, request.SubmissionID); err != nil {
				return nil, fmt.Errorf("marking retried submission reserved: %w", err)
			}
			used++
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("committing replayed submission reservation: %w", err)
		}
		return &store.SecuritySubmissionReservationResult{Reservation: existing, Reserved: true, Used: used, Limit: request.BudgetLimit}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("checking submission reservation idempotency: %w", err)
	}
	var activeReservation bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM security_research_submission_reservations
		WHERE submission_id = $1 AND voided_at IS NULL)`, request.SubmissionID).Scan(&activeReservation); err != nil {
		return nil, fmt.Errorf("checking active submission reservation: %w", err)
	}
	if activeReservation {
		// An active reservation is an owned lease. Only the exact idempotent
		// attempt above may replay it; a different attempt must not proceed.
		return nil, store.ErrSecurityResearchReservationConflict
	}
	if status != "candidate" {
		return nil, errors.New("submission is not eligible for reservation")
	}
	if used >= request.BudgetLimit {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("committing exhausted submission reservation: %w", err)
		}
		return &store.SecuritySubmissionReservationResult{Reserved: false, Used: used, Limit: request.BudgetLimit}, nil
	}
	stored, err := scanSecuritySubmissionReservation(tx.QueryRow(ctx, `INSERT INTO security_research_submission_reservations
		(submission_id, target_id, workflow, period_days, budget_limit, idempotency_key, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + interval '24 hours')
		RETURNING id, submission_id, target_id, workflow, period_days, budget_limit, idempotency_key, reserved_at, expires_at, voided_at`,
		request.SubmissionID, targetID, request.Workflow, request.PeriodDays, request.BudgetLimit, request.IdempotencyKey))
	if err != nil {
		return nil, fmt.Errorf("reserving security research submission: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE security_research_submissions SET status = 'reserved' WHERE id = $1 AND status = 'candidate'`, request.SubmissionID); err != nil {
		return nil, fmt.Errorf("marking submission reserved: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing submission reservation: %w", err)
	}
	return &store.SecuritySubmissionReservationResult{Reservation: stored, Reserved: true, Used: used + 1, Limit: request.BudgetLimit}, nil
}

func (s *Store) VoidSecurityResearchSubmissionReservation(ctx context.Context, namespace string, submissionID uuid.UUID, idempotencyKey string) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if submissionID == uuid.Nil || idempotencyKey == "" {
		return errors.New("submission and reservation idempotency key are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning reservation cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `UPDATE security_research_submission_reservations r
		SET voided_at = now()
		FROM security_research_submissions s
		JOIN security_research_targets t ON t.id = s.target_id
		WHERE r.submission_id = s.id AND t.namespace = $1 AND s.id = $2
			AND r.idempotency_key = $3 AND r.voided_at IS NULL`, namespace, submissionID, idempotencyKey)
	if err != nil {
		return fmt.Errorf("voiding reservation: %w", err)
	}
	if command.RowsAffected() == 0 {
		return store.ErrSecurityResearchReservationConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE security_research_submissions
		SET status = 'candidate' WHERE id = $1 AND status = 'reserved'`, submissionID); err != nil {
		return fmt.Errorf("releasing reserved submission: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing reservation cleanup: %w", err)
	}
	return nil
}

func (s *Store) MarkSecurityResearchSubmissionSubmitted(ctx context.Context, namespace string, submissionID uuid.UUID, submittedAt time.Time) error {
	if err := requireSecurityNamespace(namespace); err != nil {
		return err
	}
	if submissionID == uuid.Nil {
		return errors.New("submission is required")
	}
	if submittedAt.IsZero() {
		submittedAt = time.Now().UTC()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning submitted transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err := tx.QueryRow(ctx, `SELECT s.status FROM security_research_submissions s
		JOIN security_research_targets t ON t.id = s.target_id
		WHERE t.namespace = $1 AND s.id = $2 FOR UPDATE`, namespace, submissionID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return store.ErrSecurityResearchSubmissionNotFound
	} else if err != nil {
		return fmt.Errorf("locking submitted transition: %w", err)
	}
	if status == "submitted" || status == "resolved" {
		return nil
	}
	if status != "reserved" {
		return errors.New("submission must be reserved before it is submitted")
	}
	var reservationActive bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM security_research_submission_reservations
		WHERE submission_id = $1 AND voided_at IS NULL AND expires_at > now())`, submissionID).Scan(&reservationActive); err != nil {
		return fmt.Errorf("checking submission reservation expiry: %w", err)
	}
	if !reservationActive {
		if _, err := tx.Exec(ctx, `UPDATE security_research_submission_reservations SET voided_at = now()
			WHERE submission_id = $1 AND voided_at IS NULL`, submissionID); err != nil {
			return fmt.Errorf("voiding expired submission reservation: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE security_research_submissions SET status = 'candidate' WHERE id = $1`, submissionID); err != nil {
			return fmt.Errorf("releasing expired submission reservation: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing expired submission reservation: %w", err)
		}
		return errors.New("submission reservation has expired")
	}
	if _, err := tx.Exec(ctx, `UPDATE security_research_submissions SET status = 'submitted', submitted_at = $2 WHERE id = $1`, submissionID, submittedAt); err != nil {
		return fmt.Errorf("marking security research submission submitted: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing submitted transition: %w", err)
	}
	return nil
}

func validSecuritySubmissionOutcome(outcome string) bool {
	switch outcome {
	case store.SecuritySubmissionOutcomeAccepted, store.SecuritySubmissionOutcomeDuplicate, store.SecuritySubmissionOutcomeInformative, store.SecuritySubmissionOutcomeRejected, store.SecuritySubmissionOutcomeResolved:
		return true
	default:
		return false
	}
}

func scanSecuritySubmissionOutcomeEvent(row securityResearchScanner) (*store.SecuritySubmissionOutcomeEvent, error) {
	var value store.SecuritySubmissionOutcomeEvent
	if err := row.Scan(&value.ID, &value.SubmissionID, &value.Outcome, &value.ExternalReference, &value.Rationale, &value.Actor, &value.CorrectionOf, &value.IdempotencyKey, &value.CreatedAt); err != nil {
		return nil, err
	}
	return &value, nil
}

func (s *Store) RecordSecuritySubmissionOutcome(ctx context.Context, namespace string, submissionID uuid.UUID, input store.SecuritySubmissionOutcomeInput) (*store.SecuritySubmissionOutcome, bool, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, false, err
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if submissionID == uuid.Nil || input.RevisionID == uuid.Nil || input.IdempotencyKey == "" || !validSecuritySubmissionOutcome(input.Outcome) {
		return nil, false, errors.New("submission, revision, valid outcome, and idempotency key are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning submission outcome: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err := tx.QueryRow(ctx, `SELECT s.status FROM security_research_submissions s
		JOIN security_research_targets t ON t.id = s.target_id
		WHERE t.namespace = $1 AND s.id = $2 AND s.revision_id = $3 FOR UPDATE`, namespace, submissionID, input.RevisionID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return nil, false, store.ErrSecurityResearchSubmissionNotFound
	} else if err != nil {
		return nil, false, fmt.Errorf("locking submission outcome: %w", err)
	}
	if status == "reserved" {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM security_research_submission_reservations
			WHERE submission_id = $1 AND voided_at IS NULL AND expires_at > now())`, submissionID).Scan(&active); err != nil {
			return nil, false, fmt.Errorf("checking outcome reservation: %w", err)
		}
		if !active {
			return nil, false, errors.New("outcome requires an active submitted reservation")
		}
		if _, err := tx.Exec(ctx, `UPDATE security_research_submissions
			SET status = 'submitted', submitted_at = COALESCE(submitted_at, now()) WHERE id = $1`, submissionID); err != nil {
			return nil, false, fmt.Errorf("finalizing externally submitted candidate: %w", err)
		}
		status = "submitted"
	}
	if status != "submitted" && status != "resolved" {
		return nil, false, errors.New("outcome requires a reserved or submitted submission")
	}
	var replay store.SecuritySubmissionOutcome
	err = tx.QueryRow(ctx, `SELECT submission_id, id, outcome, external_reference, created_at
		FROM security_research_submission_outcome_events
		WHERE submission_id = $1 AND idempotency_key = $2`, submissionID, input.IdempotencyKey).Scan(
		&replay.SubmissionID, &replay.EventID, &replay.Outcome, &replay.ExternalReference, &replay.RecordedAt)
	if err == nil {
		return &replay, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("checking submission outcome idempotency: %w", err)
	}
	var currentEventID int64
	err = tx.QueryRow(ctx, `SELECT event_id FROM security_research_submission_outcomes WHERE submission_id = $1`, submissionID).Scan(&currentEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		if input.CorrectionOf != nil {
			return nil, false, errors.New("cannot correct a submission without a current outcome")
		}
	} else if err != nil {
		return nil, false, fmt.Errorf("reading current submission outcome: %w", err)
	} else if input.CorrectionOf == nil || *input.CorrectionOf != currentEventID {
		return nil, false, errors.New("outcome correction must reference the current outcome event")
	}
	var stored store.SecuritySubmissionOutcome
	err = tx.QueryRow(ctx, `INSERT INTO security_research_submission_outcome_events
		(submission_id, outcome, external_reference, rationale, actor, correction_of, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING submission_id, id, outcome, external_reference, created_at`, submissionID, input.Outcome, input.ExternalReference, input.Rationale, input.Actor, input.CorrectionOf, input.IdempotencyKey).Scan(
		&stored.SubmissionID, &stored.EventID, &stored.Outcome, &stored.ExternalReference, &stored.RecordedAt)
	if err != nil {
		return nil, false, fmt.Errorf("recording submission outcome event: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO security_research_submission_outcomes
		(submission_id, event_id, outcome, external_reference, recorded_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (submission_id) DO UPDATE SET
			event_id = EXCLUDED.event_id, outcome = EXCLUDED.outcome,
			external_reference = EXCLUDED.external_reference, recorded_at = EXCLUDED.recorded_at`,
		stored.SubmissionID, stored.EventID, stored.Outcome, stored.ExternalReference, stored.RecordedAt); err != nil {
		return nil, false, fmt.Errorf("projecting current submission outcome: %w", err)
	}
	nextStatus := "submitted"
	if input.Outcome == store.SecuritySubmissionOutcomeResolved {
		nextStatus = "resolved"
	}
	if _, err := tx.Exec(ctx, `UPDATE security_research_submissions SET status = $2 WHERE id = $1`, submissionID, nextStatus); err != nil {
		return nil, false, fmt.Errorf("updating submission outcome status: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing submission outcome: %w", err)
	}
	return &stored, true, nil
}

func (s *Store) ListSecuritySubmissionOutcomeEvents(ctx context.Context, namespace string, revisionID, submissionID uuid.UUID) ([]store.SecuritySubmissionOutcomeEvent, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT e.id, e.submission_id, e.outcome, e.external_reference, e.rationale, e.actor, e.correction_of, e.idempotency_key, e.created_at
		FROM security_research_submission_outcome_events e
		JOIN security_research_submissions s ON s.id = e.submission_id
		JOIN security_research_targets t ON t.id = s.target_id
		WHERE t.namespace = $1 AND s.revision_id = $2 AND s.id = $3 ORDER BY e.id`, namespace, revisionID, submissionID)
	if err != nil {
		return nil, fmt.Errorf("listing submission outcome events: %w", err)
	}
	defer rows.Close()
	var values []store.SecuritySubmissionOutcomeEvent
	for rows.Next() {
		value, err := scanSecuritySubmissionOutcomeEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning submission outcome event: %w", err)
		}
		values = append(values, *value)
	}
	return values, rows.Err()
}

func (s *Store) GetSecuritySubmissionPrecision(ctx context.Context, namespace string, targetID uuid.UUID, workflow string, since *time.Time) (*store.SecuritySubmissionPrecision, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	workflow = strings.TrimSpace(workflow)
	if targetID == uuid.Nil || workflow == "" {
		return nil, errors.New("target and workflow are required")
	}
	var targetExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM security_research_targets WHERE namespace = $1 AND id = $2)`, namespace, targetID).Scan(&targetExists); err != nil {
		return nil, fmt.Errorf("checking precision target: %w", err)
	}
	if !targetExists {
		return nil, store.ErrSecurityResearchTargetNotFound
	}
	var sinceValue any
	if since != nil {
		sinceValue = *since
	}
	var value store.SecuritySubmissionPrecision
	if err := s.pool.QueryRow(ctx, `SELECT
		count(*)::bigint,
		count(*) FILTER (WHERE o.outcome = 'accepted')::bigint,
		count(*) FILTER (WHERE o.outcome = 'duplicate')::bigint,
		count(*) FILTER (WHERE o.outcome = 'informative')::bigint,
		count(*) FILTER (WHERE o.outcome = 'rejected')::bigint,
		count(*) FILTER (WHERE o.outcome = 'resolved')::bigint
		FROM security_research_submissions s
		LEFT JOIN security_research_submission_outcomes o ON o.submission_id = s.id
		WHERE s.target_id = $1 AND s.workflow = $2 AND s.submitted_at IS NOT NULL
			AND ($3::timestamptz IS NULL OR s.submitted_at >= $3)`, targetID, workflow, sinceValue).Scan(
		&value.Submitted, &value.Accepted, &value.Duplicate, &value.Informative, &value.Rejected, &value.Resolved); err != nil {
		return nil, fmt.Errorf("calculating submission precision: %w", err)
	}
	return &value, nil
}

func scanSecurityResearchDecisionSnapshot(row securityResearchScanner) (*store.SecurityResearchDecisionSnapshot, error) {
	var value store.SecurityResearchDecisionSnapshot
	var inputs []byte
	if err := row.Scan(&value.ID, &value.RevisionID, &value.SubmissionID, &value.Workflow, &value.CandidateKey, &value.Decision, &value.Reason, &value.Rank, &inputs, &value.IdempotencyKey, &value.CreatedAt); err != nil {
		return nil, err
	}
	value.Inputs = inputs
	return &value, nil
}

const securityResearchDecisionSnapshotColumns = `d.id, d.revision_id, d.submission_id, d.workflow, d.candidate_key, d.decision, d.reason, d.rank, d.inputs, d.idempotency_key, d.created_at`

func (s *Store) CreateSecurityResearchDecisionSnapshot(ctx context.Context, namespace string, value *store.SecurityResearchDecisionSnapshot) (*store.SecurityResearchDecisionSnapshot, bool, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, false, err
	}
	if value == nil || value.RevisionID == uuid.Nil || strings.TrimSpace(value.Workflow) == "" || strings.TrimSpace(value.CandidateKey) == "" || strings.TrimSpace(value.Reason) == "" || value.Rank <= 0 || strings.TrimSpace(value.IdempotencyKey) == "" {
		return nil, false, errors.New("revision, workflow, candidate key, reason, positive rank, and idempotency key are required")
	}
	if value.Decision != "submit" && value.Decision != "retain" && value.Decision != "reject" {
		return nil, false, errors.New("invalid research decision")
	}
	inputs, err := researchJSON(value.Inputs, `{}`)
	if err != nil {
		return nil, false, err
	}
	stored, err := scanSecurityResearchDecisionSnapshot(s.pool.QueryRow(ctx, `INSERT INTO security_research_decision_snapshots
		(revision_id, submission_id, workflow, candidate_key, decision, reason, rank, inputs, idempotency_key)
		SELECT r.id, $3, $4, $5, $6, $7, $8, $9, $10
		FROM security_research_revisions r
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE r.id = $1 AND t.namespace = $2
			AND ($3::uuid IS NULL OR EXISTS (
				SELECT 1 FROM security_research_submissions s
				WHERE s.id = $3 AND s.revision_id = r.id AND s.workflow = $4))
		ON CONFLICT (revision_id, workflow, idempotency_key) DO NOTHING
		RETURNING id, revision_id, submission_id, workflow, candidate_key, decision, reason, rank, inputs, idempotency_key, created_at`,
		value.RevisionID, namespace, value.SubmissionID, strings.TrimSpace(value.Workflow), strings.TrimSpace(value.CandidateKey), value.Decision, strings.TrimSpace(value.Reason), value.Rank, inputs, strings.TrimSpace(value.IdempotencyKey)))
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		stored, err = scanSecurityResearchDecisionSnapshot(s.pool.QueryRow(ctx, `SELECT `+securityResearchDecisionSnapshotColumns+`
			FROM security_research_decision_snapshots d
			JOIN security_research_revisions r ON r.id = d.revision_id
			JOIN security_research_targets t ON t.id = r.target_id
			WHERE t.namespace = $1 AND d.revision_id = $2 AND d.workflow = $3 AND d.idempotency_key = $4`,
			namespace, value.RevisionID, strings.TrimSpace(value.Workflow), strings.TrimSpace(value.IdempotencyKey)))
		if errors.Is(err, pgx.ErrNoRows) {
			var revisionExists bool
			if checkErr := s.pool.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM security_research_revisions r JOIN security_research_targets t ON t.id = r.target_id
				WHERE r.id = $1 AND t.namespace = $2)`, value.RevisionID, namespace).Scan(&revisionExists); checkErr != nil {
				return nil, false, fmt.Errorf("checking decision revision: %w", checkErr)
			}
			if !revisionExists {
				return nil, false, store.ErrSecurityResearchRevisionNotFound
			}
			return nil, false, errors.New("decision submission must belong to the revision and workflow")
		}
	}
	if err != nil {
		return nil, false, fmt.Errorf("creating security research decision snapshot: %w", err)
	}
	return stored, created, nil
}

func (s *Store) CountSecurityResearchHypothesesByActor(ctx context.Context, namespace, actor string) (int, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return 0, err
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT e.hypothesis_id)
		FROM security_research_hypothesis_events e JOIN security_research_hypotheses h ON h.id = e.hypothesis_id
		JOIN security_research_revisions r ON r.id = h.revision_id JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND e.actor = $2`, namespace, strings.TrimSpace(actor)).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting security research hypotheses by actor: %w", err)
	}
	return count, nil
}

func (s *Store) ListSecurityResearchCoverageByActor(ctx context.Context, namespace, actor string) ([]store.SecurityResearchCoverage, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.revision_id, c.hypothesis_id, c.dimension, c.subject_key, c.verdict, c.bounds, c.evidence, c.actor, c.idempotency_key, c.created_at
		FROM security_research_coverage c JOIN security_research_revisions r ON r.id = c.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND c.actor = $2 ORDER BY c.created_at, c.id`, namespace, strings.TrimSpace(actor))
	if err != nil {
		return nil, fmt.Errorf("listing security research coverage by actor: %w", err)
	}
	defer rows.Close()
	var values []store.SecurityResearchCoverage
	for rows.Next() {
		value, err := scanSecurityResearchCoverage(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning security research coverage: %w", err)
		}
		values = append(values, *value)
	}
	return values, rows.Err()
}
