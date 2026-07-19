package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/sdk/pkg/agentsdk/tools/signal"
	"github.com/jackc/pgx/v5"
)

// RegisterPlanTools registers SDK save_plan and get_plan tools backed by the
// operator's Postgres artifact store.
func RegisterPlanTools(registry *Registry, stateStore store.StateStore, sessionID uuid.UUID) {
	var artifactStore signal.ArtifactStore
	if stateStore != nil {
		artifactStore = planArtifactStore{stateStore: stateStore}
	}
	for _, tool := range signal.PlanTools(artifactStore, sessionID) {
		registry.Register(tool)
	}
}

type planArtifactStore struct {
	stateStore store.StateStore
}

func (s planArtifactStore) UpsertArtifact(ctx context.Context, sessionID uuid.UUID, kind, content, s3URL, contentHash string, metadata json.RawMessage) (any, error) {
	return s.stateStore.UpsertArtifact(ctx, sessionID, kind, content, s3URL, contentHash, metadata)
}

func (s planArtifactStore) GetArtifact(ctx context.Context, sessionID uuid.UUID, kind string) (*signal.Artifact, error) {
	artifact, err := s.stateStore.GetArtifact(ctx, sessionID, kind)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, signal.ErrArtifactNotFound
		}
		return nil, err
	}
	if artifact == nil {
		return nil, signal.ErrArtifactNotFound
	}
	return &signal.Artifact{Content: artifact.Content}, nil
}
