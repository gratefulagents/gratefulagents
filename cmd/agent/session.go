package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/store/contentblob"
	pgstore "github.com/gratefulagents/gratefulagents/internal/store/postgres"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// initSessionClient creates a Postgres-backed session client for the agent.
// Postgres is required — returns an error if DATABASE_URL is not set.
func initSessionClient(ctx context.Context, crdClient client.Client, runName, namespace, phase, currentStep string) (*sessionclient.Client, error) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL is required but not set")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to Postgres: %w", err)
	}

	stateStore := pgstore.NewFromPool(pool)
	contentBlobs, err := contentblob.NewS3FromEnv()
	if err != nil {
		stateStore.Close()
		return nil, fmt.Errorf("configuring S3 project asset storage: %w", err)
	}
	stateStore.SetProjectContentBlobStore(contentBlobs)
	sc, err := sessionclient.New(ctx, stateStore, crdClient, runName, namespace, phase, currentStep)
	if err != nil {
		stateStore.Close()
		return nil, fmt.Errorf("creating session client: %w", err)
	}

	log.Printf("Postgres session client initialized (session=%s)", sc.SessionID())
	return sc, nil
}
