package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkdurable "github.com/gratefulagents/sdk/pkg/agentsdk/durable"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const durableRunLeaseTTL = 30 * time.Second

type sdkDurableRuntime struct {
	db    *sql.DB
	store sdkdurable.RunStore
}

func newSDKDurableRuntime(ctx context.Context) (*sdkDurableRuntime, error) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is required for durable SDK runs")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening durable SDK database: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting durable SDK database: %w", err)
	}
	store, err := sdkdurable.NewPostgresStore(db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("creating durable SDK store: %w", err)
	}
	return &sdkDurableRuntime{db: db, store: store}, nil
}

func (r *sdkDurableRuntime) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func reserveSDKDurablePass(ctx context.Context, sc *sessionclient.Client, userMessageID int64) (int64, error) {
	if userMessageID <= 0 {
		return 0, errors.New("cannot reserve durable SDK run without a persisted user message ID")
	}
	var pass int64
	err := sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
		if state.DurableRunMessageID == userMessageID {
			pass = state.DurableRunPass
			return nil
		}
		// A genuinely newer claimed user message is an explicit decision to
		// leave an unreconciled prior turn behind and start a fresh durable pass.
		// The old immutable run remains available for audit and operator review.
		state.DurableRunNextPass++
		state.DurableRunMessageID = userMessageID
		state.DurableRunPass = state.DurableRunNextPass
		pass = state.DurableRunPass
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("reserving durable SDK pass: %w", err)
	}
	return pass, nil
}

func completeSDKDurablePass(ctx context.Context, sc *sessionclient.Client, userMessageID, pass int64) error {
	return sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
		if state.DurableRunMessageID != userMessageID || state.DurableRunPass != pass {
			return fmt.Errorf("durable SDK pass changed while completing message %d pass %d", userMessageID, pass)
		}
		state.DurableRunMessageID = 0
		state.DurableRunPass = 0
		return nil
	})
}

func openSDKStoredRun(ctx context.Context, cfg runConfig, userMessageID, pass int64) (*agent.StoredRun, error) {
	if cfg.DurableRunStore == nil {
		return nil, errors.New("durable SDK run store is not configured")
	}
	if userMessageID <= 0 || pass <= 0 {
		return nil, errors.New("durable SDK run requires a persisted message and pass")
	}
	runID := sdkdurable.RunID(fmt.Sprintf("agentrun-%s-message-%d-pass-%d", cfg.TaskUID, userMessageID, pass))
	for {
		run, err := agent.OpenStoredRun(ctx, cfg.DurableRunStore, agent.StoredRunOptions{
			TenantID:       cfg.DurableRunTenant,
			RunID:          runID,
			Owner:          cfg.DurableRunOwner,
			LeaseTTL:       durableRunLeaseTTL,
			Classification: sdkdurable.DataSensitive,
		})
		if err == nil {
			return run, nil
		}
		if !errors.Is(err, sdkdurable.ErrLeaseHeld) && !errors.Is(err, sdkdurable.ErrAlreadyExists) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func closeSDKStoredRun(run *agent.StoredRun) error {
	if run == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return run.Close(ctx)
}
