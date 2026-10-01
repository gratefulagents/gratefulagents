package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
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

// completeDurablePassState retires the committed durable pass in the working
// state; a pass that changed underneath (another owner reserved a newer one)
// is an error.
func completeDurablePassState(state *sessionclient.WorkingState, userMessageID, pass int64) error {
	if state.DurableRunMessageID != userMessageID || state.DurableRunPass != pass {
		return fmt.Errorf("durable SDK pass changed while completing message %d pass %d", userMessageID, pass)
	}
	state.DurableRunMessageID = 0
	state.DurableRunPass = 0
	return nil
}

// durableRunOpenMaxWait bounds how long a pass waits for another owner's
// lease on the same stored run (e.g. a partitioned previous pod that keeps
// renewing it). Vars so tests can shrink them.
var (
	durableRunOpenMaxWait      = 5 * time.Minute
	durableRunOpenInitialDelay = time.Second
	durableRunOpenMaxDelay     = 15 * time.Second
)

// errDurableRunOpenStopped reports that the user stopped the turn while it was
// waiting for the durable run lease.
var errDurableRunOpenStopped = errors.New("stopped by user while waiting for the durable SDK run lease")

// openSDKStoredRun opens (or resumes) the pass's stored run. A lease held by
// another owner is waited out with exponential backoff, bounded by
// durableRunOpenMaxWait; stopRequested (optional) is checked between attempts
// so a user stop is honored during the wait.
func openSDKStoredRun(
	ctx context.Context, cfg runConfig, userMessageID, pass int64,
	stopRequested func(context.Context) bool,
) (*agent.StoredRun, error) {
	if cfg.DurableRunStore == nil {
		return nil, errors.New("durable SDK run store is not configured")
	}
	if userMessageID <= 0 || pass <= 0 {
		return nil, errors.New("durable SDK run requires a persisted message and pass")
	}
	runID := sdkdurable.RunID(fmt.Sprintf("agentrun-%s-message-%d-pass-%d", cfg.TaskUID, userMessageID, pass))
	ctx, cancel := context.WithTimeout(ctx, durableRunOpenMaxWait)
	defer cancel()
	deadline, _ := ctx.Deadline()
	delay := durableRunOpenInitialDelay
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if stopRequested != nil && stopRequested(ctx) {
			return nil, errDurableRunOpenStopped
		}
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
		if time.Now().Add(delay).After(deadline) {
			return nil, fmt.Errorf("durable SDK run %s still leased by another owner after %s: %w",
				runID, durableRunOpenMaxWait, err)
		}
		log.Printf("WARN: durable SDK run %s is leased by another owner (attempt %d): %v — retrying in %s",
			runID, attempt, err, delay)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, durableRunOpenMaxDelay)
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
