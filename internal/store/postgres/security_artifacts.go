package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func validSecurityFindingArtifactKind(kind string) bool {
	switch kind {
	case store.SecurityFindingArtifactPoCCandidate,
		store.SecurityFindingArtifactPoCValidation,
		store.SecurityFindingArtifactBountySubmission,
		store.SecurityFindingArtifactSubmissionBundle:
		return true
	}
	return false
}

const securityFindingArtifactColumns = `a.id, a.finding_id, a.execution_id, a.kind, a.content, a.s3_key, a.sha256,
	a.size_bytes, a.media_type, a.filename, a.status, a.error, a.actor_run, a.created_at, a.updated_at`

const securityFindingArtifactReturningColumns = `id, finding_id, execution_id, kind, content, s3_key, sha256,
	size_bytes, media_type, filename, status, error, actor_run, created_at, updated_at`

type securityFindingArtifactScanner interface {
	Scan(dest ...any) error
}

func scanSecurityFindingArtifact(row securityFindingArtifactScanner) (*store.SecurityFindingArtifact, error) {
	var artifact store.SecurityFindingArtifact
	var content []byte
	if err := row.Scan(&artifact.ID, &artifact.FindingID, &artifact.ExecutionID, &artifact.Kind, &content,
		&artifact.S3Key, &artifact.SHA256, &artifact.SizeBytes, &artifact.MediaType,
		&artifact.Filename, &artifact.Status, &artifact.Error, &artifact.ActorRun,
		&artifact.CreatedAt, &artifact.UpdatedAt); err != nil {
		return nil, err
	}
	artifact.Content = json.RawMessage(content)
	return &artifact, nil
}

func (s *Store) UpsertSecurityFindingArtifact(ctx context.Context, namespace string, artifact *store.SecurityFindingArtifact) (*store.SecurityFindingArtifact, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	if artifact == nil || artifact.FindingID == uuid.Nil {
		return nil, fmt.Errorf("finding artifact and finding id are required")
	}
	artifact.ExecutionID = strings.TrimSpace(artifact.ExecutionID)
	if artifact.ExecutionID == "" {
		return nil, fmt.Errorf("security finding artifact execution id is required")
	}
	artifact.Kind = strings.TrimSpace(artifact.Kind)
	if !validSecurityFindingArtifactKind(artifact.Kind) {
		return nil, fmt.Errorf("invalid security finding artifact kind %q", artifact.Kind)
	}
	content := artifact.Content
	if len(content) == 0 {
		content = json.RawMessage(`{}`)
	}
	if !json.Valid(content) {
		return nil, fmt.Errorf("security finding artifact content must be valid JSON")
	}
	if artifact.SizeBytes < 0 {
		return nil, fmt.Errorf("security finding artifact size must not be negative")
	}
	query := `INSERT INTO security_finding_artifacts
		(finding_id, execution_id, kind, content, s3_key, sha256, size_bytes, media_type, filename, status, error, actor_run)
	SELECT f.id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
	FROM security_findings f WHERE f.id = $1 AND f.namespace = $2
	ON CONFLICT (finding_id, execution_id, kind) DO UPDATE SET
		content = EXCLUDED.content, s3_key = EXCLUDED.s3_key, sha256 = EXCLUDED.sha256,
		size_bytes = EXCLUDED.size_bytes, media_type = EXCLUDED.media_type,
		filename = EXCLUDED.filename, status = EXCLUDED.status, error = EXCLUDED.error,
		actor_run = EXCLUDED.actor_run
	RETURNING ` + securityFindingArtifactReturningColumns
	stored, err := scanSecurityFindingArtifact(s.pool.QueryRow(ctx, query,
		artifact.FindingID, namespace, artifact.ExecutionID, artifact.Kind, content, artifact.S3Key,
		artifact.SHA256, artifact.SizeBytes, artifact.MediaType, artifact.Filename,
		artifact.Status, artifact.Error, artifact.ActorRun))
	if err != nil {
		return nil, fmt.Errorf("upserting security finding artifact: %w", err)
	}
	return stored, nil
}

func (s *Store) GetSecurityFindingArtifact(ctx context.Context, namespace string, findingID uuid.UUID, executionID, kind string) (*store.SecurityFindingArtifact, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	if findingID == uuid.Nil || strings.TrimSpace(executionID) == "" || !validSecurityFindingArtifactKind(strings.TrimSpace(kind)) {
		return nil, nil
	}
	query := `SELECT ` + securityFindingArtifactColumns + `
	FROM security_finding_artifacts a
	JOIN security_findings f ON f.id = a.finding_id
	WHERE f.namespace = $1 AND a.finding_id = $2 AND a.execution_id = $3 AND a.kind = $4`
	artifact, err := scanSecurityFindingArtifact(s.pool.QueryRow(ctx, query, namespace, findingID, strings.TrimSpace(executionID), strings.TrimSpace(kind)))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting security finding artifact: %w", err)
	}
	return artifact, nil
}

func (s *Store) ListSecurityFindingArtifacts(ctx context.Context, namespace string, findingID uuid.UUID, executionID string) ([]store.SecurityFindingArtifact, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	query := `SELECT ` + securityFindingArtifactColumns + `
	FROM security_finding_artifacts a
	JOIN security_findings f ON f.id = a.finding_id
	WHERE f.namespace = $1 AND a.finding_id = $2 AND a.execution_id = $3 ORDER BY a.created_at, a.kind`
	rows, err := s.pool.Query(ctx, query, namespace, findingID, strings.TrimSpace(executionID))
	if err != nil {
		return nil, fmt.Errorf("listing security finding artifacts: %w", err)
	}
	defer rows.Close()
	var out []store.SecurityFindingArtifact
	for rows.Next() {
		artifact, err := scanSecurityFindingArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning security finding artifact: %w", err)
		}
		out = append(out, *artifact)
	}
	return out, rows.Err()
}

var _ store.SecurityFindingArtifactStore = (*Store)(nil)
