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

var _ store.SecurityResearchArtifactStore = (*Store)(nil)

const securityResearchArtifactColumns = `a.id, a.revision_id, a.execution_id, a.task_name, a.kind, a.schema_version,
	a.payload, a.candidate_fingerprints, a.coverage_ids, a.blocker_ids, a.conditions, a.actor, a.idempotency_key, a.created_at`

type securityResearchArtifactScanner interface {
	Scan(...any) error
}

func scanSecurityResearchArtifact(row securityResearchArtifactScanner) (*store.SecurityResearchArtifact, error) {
	var value store.SecurityResearchArtifact
	var payload, coverageIDs, conditions []byte
	if err := row.Scan(&value.ID, &value.RevisionID, &value.ExecutionID, &value.TaskName, &value.Kind, &value.SchemaVersion,
		&payload, &value.CandidateFingerprints, &coverageIDs, &value.BlockerIDs, &conditions, &value.Actor, &value.IdempotencyKey, &value.CreatedAt); err != nil {
		return nil, err
	}
	value.Payload = payload
	if len(coverageIDs) > 0 {
		if err := json.Unmarshal(coverageIDs, &value.CoverageIDs); err != nil {
			return nil, err
		}
	}
	if len(conditions) > 0 {
		if err := json.Unmarshal(conditions, &value.Conditions); err != nil {
			return nil, err
		}
	}
	return &value, nil
}

func normalizeSecurityResearchArtifact(value *store.SecurityResearchArtifact) error {
	value.ExecutionID = strings.TrimSpace(value.ExecutionID)
	value.TaskName = strings.TrimSpace(value.TaskName)
	value.Kind = strings.TrimSpace(value.Kind)
	value.Actor = strings.TrimSpace(value.Actor)
	value.IdempotencyKey = strings.TrimSpace(value.IdempotencyKey)
	for i := range value.CandidateFingerprints {
		value.CandidateFingerprints[i] = strings.TrimSpace(value.CandidateFingerprints[i])
	}
	// Keep optional Go collections compatible with the table's NOT NULL and
	// JSON-object constraints. pgx otherwise encodes nil slices as SQL NULL,
	// while json.Marshal(nilMap) produces the JSON scalar null.
	if value.CandidateFingerprints == nil {
		value.CandidateFingerprints = []string{}
	}
	if value.CoverageIDs == nil {
		value.CoverageIDs = map[string][]uuid.UUID{}
	}
	if value.BlockerIDs == nil {
		value.BlockerIDs = []uuid.UUID{}
	}
	if value.Conditions == nil {
		value.Conditions = map[string]json.RawMessage{}
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(value.Payload, &payload); err == nil {
		value.Payload, _ = json.Marshal(payload)
	}
	return store.ValidateSecurityResearchArtifact(value)
}

func (s *Store) CreateSecurityResearchArtifact(ctx context.Context, namespace string, value *store.SecurityResearchArtifact) (*store.SecurityResearchArtifact, bool, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, false, err
	}
	if value == nil {
		return nil, false, errors.New("security research artifact is required")
	}
	if err := normalizeSecurityResearchArtifact(value); err != nil {
		return nil, false, err
	}
	coverageIDs, err := json.Marshal(value.CoverageIDs)
	if err != nil {
		return nil, false, fmt.Errorf("encoding coverage IDs: %w", err)
	}
	conditions, err := json.Marshal(value.Conditions)
	if err != nil {
		return nil, false, fmt.Errorf("encoding conditions: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("beginning security research artifact: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanSecurityResearchArtifact(tx.QueryRow(ctx, `SELECT `+securityResearchArtifactColumns+`
		FROM security_research_artifacts a
		JOIN security_research_revisions r ON r.id = a.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND a.revision_id = $2 AND a.execution_id = $3
			AND a.task_name = $4 AND a.idempotency_key = $5`, namespace, value.RevisionID, value.ExecutionID, value.TaskName, value.IdempotencyKey))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("checking security research artifact idempotency: %w", err)
	}

	var targetKey, revision, repository string
	if err := tx.QueryRow(ctx, `SELECT t.target_key, r.revision, t.locator
		FROM security_research_revisions r JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND r.id = $2`, namespace, value.RevisionID).Scan(&targetKey, &revision, &repository); errors.Is(err, pgx.ErrNoRows) {
		return nil, false, store.ErrSecurityResearchRevisionNotFound
	} else if err != nil {
		return nil, false, fmt.Errorf("checking security research artifact revision: %w", err)
	}
	if err := validateSecurityResearchArtifactReferences(ctx, tx, namespace, targetKey, repository, revision, value); err != nil {
		return nil, false, err
	}

	stored, err := scanSecurityResearchArtifact(tx.QueryRow(ctx, `INSERT INTO security_research_artifacts
		(revision_id, execution_id, task_name, kind, schema_version, payload, candidate_fingerprints,
		 coverage_ids, blocker_ids, conditions, actor, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (revision_id, execution_id, task_name, idempotency_key) DO NOTHING
		RETURNING id, revision_id, execution_id, task_name, kind, schema_version, payload,
			candidate_fingerprints, coverage_ids, blocker_ids, conditions, actor, idempotency_key, created_at`,
		value.RevisionID, value.ExecutionID, value.TaskName, value.Kind, value.SchemaVersion, value.Payload,
		value.CandidateFingerprints, coverageIDs, value.BlockerIDs, conditions, value.Actor, value.IdempotencyKey))
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		stored, err = scanSecurityResearchArtifact(tx.QueryRow(ctx, `SELECT `+securityResearchArtifactColumns+`
			FROM security_research_artifacts a
			JOIN security_research_revisions r ON r.id = a.revision_id
			JOIN security_research_targets t ON t.id = r.target_id
			WHERE t.namespace = $1 AND a.revision_id = $2 AND a.execution_id = $3
				AND a.task_name = $4 AND a.idempotency_key = $5`, namespace, value.RevisionID, value.ExecutionID, value.TaskName, value.IdempotencyKey))
	}
	if err != nil {
		return nil, false, fmt.Errorf("creating security research artifact: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("committing security research artifact: %w", err)
	}
	return stored, created, nil
}

func validateSecurityResearchArtifactReferences(ctx context.Context, tx pgx.Tx, namespace, targetKey, repository, revision string, value *store.SecurityResearchArtifact) error {
	if len(value.CandidateFingerprints) > 0 {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT f.fingerprint)
			FROM security_findings f
			WHERE f.namespace = $1 AND f.scan_name = $2 AND f.repository = $3 AND f.revision = $4
				AND (f.execution_id = $5 OR (f.execution_id = '' AND f.run_name = $5))
				AND f.fingerprint = ANY($6::text[])`, namespace, targetKey, repository, revision, value.ExecutionID, value.CandidateFingerprints).Scan(&count); err != nil {
			return fmt.Errorf("checking candidate fingerprint references: %w", err)
		}
		if count != len(value.CandidateFingerprints) {
			return store.ErrSecurityResearchArtifactReferenceNotFound
		}
	}
	for verdict, ids := range value.CoverageIDs {
		if len(ids) == 0 {
			continue
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM security_research_coverage
			WHERE revision_id = $1 AND verdict = $2 AND id = ANY($3::uuid[])`, value.RevisionID, verdict, ids).Scan(&count); err != nil {
			return fmt.Errorf("checking coverage references: %w", err)
		}
		if count != len(ids) {
			return store.ErrSecurityResearchArtifactReferenceNotFound
		}
	}
	if len(value.BlockerIDs) > 0 {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM security_research_artifacts a
			JOIN security_research_revisions r ON r.id = a.revision_id
			JOIN security_research_targets t ON t.id = r.target_id
			WHERE t.namespace = $1 AND a.revision_id = $2 AND a.execution_id = $3
				AND a.kind = 'blocker' AND a.id = ANY($4::uuid[])`, namespace, value.RevisionID, value.ExecutionID, value.BlockerIDs).Scan(&count); err != nil {
			return fmt.Errorf("checking blocker references: %w", err)
		}
		if count != len(value.BlockerIDs) {
			return store.ErrSecurityResearchArtifactReferenceNotFound
		}
	}
	return nil
}

func (s *Store) GetSecurityResearchArtifact(ctx context.Context, namespace string, revisionID uuid.UUID, executionID string, id uuid.UUID) (*store.SecurityResearchArtifact, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	if revisionID == uuid.Nil || id == uuid.Nil || strings.TrimSpace(executionID) == "" {
		return nil, store.ErrSecurityResearchArtifactNotFound
	}
	value, err := scanSecurityResearchArtifact(s.pool.QueryRow(ctx, `SELECT `+securityResearchArtifactColumns+`
		FROM security_research_artifacts a
		JOIN security_research_revisions r ON r.id = a.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND a.revision_id = $2 AND a.execution_id = $3 AND a.id = $4`, namespace, revisionID, strings.TrimSpace(executionID), id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrSecurityResearchArtifactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting security research artifact: %w", err)
	}
	return value, nil
}

func (s *Store) ListSecurityResearchArtifacts(ctx context.Context, namespace string, filter store.SecurityResearchArtifactFilter) ([]store.SecurityResearchArtifact, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	filter.ExecutionID = strings.TrimSpace(filter.ExecutionID)
	if filter.RevisionID == uuid.Nil || filter.ExecutionID == "" {
		return nil, errors.New("revision and execution are required")
	}
	if len(filter.IDs) > store.MaxSecurityResearchArtifactsPerList || len(filter.Kinds) > store.MaxSecurityResearchArtifactsPerList || len(filter.TaskNames) > store.MaxSecurityResearchArtifactsPerList {
		return nil, fmt.Errorf("artifact filters must not exceed %d values", store.MaxSecurityResearchArtifactsPerList)
	}
	for _, kind := range filter.Kinds {
		if !store.ValidSecurityResearchArtifactKind(kind) {
			return nil, fmt.Errorf("invalid security research artifact kind %q", kind)
		}
	}
	if filter.Limit <= 0 {
		filter.Limit = store.MaxSecurityResearchArtifactsPerList
	}
	if filter.Limit > store.MaxSecurityResearchArtifactsPerList {
		return nil, fmt.Errorf("limit must not exceed %d", store.MaxSecurityResearchArtifactsPerList)
	}
	rows, err := s.pool.Query(ctx, `SELECT a.id, a.revision_id, a.execution_id, a.task_name, a.kind, a.schema_version,
		CASE WHEN $7 THEN a.payload ELSE NULL END, a.candidate_fingerprints, a.coverage_ids,
		a.blocker_ids, a.conditions, a.actor, a.idempotency_key, a.created_at
		FROM security_research_artifacts a
		JOIN security_research_revisions r ON r.id = a.revision_id
		JOIN security_research_targets t ON t.id = r.target_id
		WHERE t.namespace = $1 AND a.revision_id = $2 AND a.execution_id = $3
			AND ($4::uuid[] IS NULL OR a.id = ANY($4::uuid[]))
			AND ($5::text[] IS NULL OR a.kind = ANY($5::text[]))
			AND ($6::text[] IS NULL OR a.task_name = ANY($6::text[]))
		ORDER BY a.created_at DESC, a.id LIMIT $8`, namespace, filter.RevisionID, filter.ExecutionID,
		nilIfEmptyUUIDs(filter.IDs), nilIfEmptyStrings(filter.Kinds), nilIfEmptyStrings(filter.TaskNames), filter.IncludePayload, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("listing security research artifacts: %w", err)
	}
	defer rows.Close()
	values := make([]store.SecurityResearchArtifact, 0)
	for rows.Next() {
		value, err := scanSecurityResearchArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning security research artifact: %w", err)
		}
		values = append(values, *value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing security research artifacts: %w", err)
	}
	return values, nil
}

func nilIfEmptyUUIDs(values []uuid.UUID) []uuid.UUID {
	if len(values) == 0 {
		return nil
	}
	return values
}

func nilIfEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}
