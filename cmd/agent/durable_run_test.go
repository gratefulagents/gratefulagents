package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkdurable "github.com/gratefulagents/sdk/pkg/agentsdk/durable"
)

func TestReserveSDKDurablePassResumesAndAdvances(t *testing.T) {
	sc, _ := newSubAgentCheckpointTestClient(t)
	ctx := context.Background()

	first, err := reserveSDKDurablePass(ctx, sc, 101)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := reserveSDKDurablePass(ctx, sc, 101)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || resumed != first {
		t.Fatalf("first=%d resumed=%d", first, resumed)
	}
	if err := sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
		return completeDurablePassState(state, 101, first)
	}); err != nil {
		t.Fatal(err)
	}
	second, err := reserveSDKDurablePass(ctx, sc, 101)
	if err != nil {
		t.Fatal(err)
	}
	if second != 2 {
		t.Fatalf("second=%d, want 2", second)
	}
}

func TestReserveSDKDurablePassNewMessageAbandonsPriorPass(t *testing.T) {
	sc, _ := newSubAgentCheckpointTestClient(t)
	ctx := context.Background()
	if _, err := reserveSDKDurablePass(ctx, sc, 101); err != nil {
		t.Fatal(err)
	}
	pass, err := reserveSDKDurablePass(ctx, sc, 102)
	if err != nil {
		t.Fatal(err)
	}
	if pass != 2 {
		t.Fatalf("pass=%d, want 2", pass)
	}
}

func TestCompleteSDKDurablePassRejectsStaleIdentity(t *testing.T) {
	sc, _ := newSubAgentCheckpointTestClient(t)
	ctx := context.Background()
	pass, err := reserveSDKDurablePass(ctx, sc, 101)
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
		return completeDurablePassState(state, 101, pass+1)
	}); err == nil {
		t.Fatal("expected stale pass rejection")
	}
}

func TestOpenSDKStoredRunBoundsLeaseWaitAndHonorsStop(t *testing.T) {
	store, err := sdkdurable.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := runConfig{TaskUID: "uid", DurableRunTenant: "tenant", DurableRunOwner: "old-pod", DurableRunStore: store}
	held, err := openSDKStoredRun(context.Background(), cfg, 101, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeSDKStoredRun(held); err != nil {
			t.Error(err)
		}
	}()
	cfg.DurableRunOwner = "new-pod"
	oldWait, oldInitial, oldMax := durableRunOpenMaxWait, durableRunOpenInitialDelay, durableRunOpenMaxDelay
	durableRunOpenMaxWait, durableRunOpenInitialDelay, durableRunOpenMaxDelay =
		30*time.Millisecond, time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() {
		durableRunOpenMaxWait, durableRunOpenInitialDelay, durableRunOpenMaxDelay = oldWait, oldInitial, oldMax
	})
	if _, err := openSDKStoredRun(context.Background(), cfg, 101, 1,
		func(context.Context) bool { return true },
	); !errors.Is(err, errDurableRunOpenStopped) {
		t.Fatalf("stop err=%v", err)
	}
	start := time.Now()
	if _, err := openSDKStoredRun(context.Background(), cfg, 101, 1, nil); !errors.Is(err, sdkdurable.ErrLeaseHeld) &&
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lease timeout err=%v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("lease wait exceeded bound: %v", elapsed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := openSDKStoredRun(ctx, cfg, 101, 1, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestCommitTurnDetachedFromShutdownRetainsReplayableCheckpoint(t *testing.T) {
	for _, failTranscript := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "uncommitted"}[failTranscript], func(t *testing.T) {
			sc, ss := newRecoveryClient(t)
			ss.transcriptErr = nil
			if failTranscript {
				ss.transcriptErr = errors.New("store unavailable")
			}
			pass, err := reserveSDKDurablePass(context.Background(), sc, 101)
			if err != nil {
				t.Fatal(err)
			}
			store, err := sdkdurable.NewFilesystemStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cfg := runConfig{TaskUID: "uid", DurableRunTenant: "tenant", DurableRunOwner: "pod", DurableRunStore: store}
			run, err := openSDKStoredRun(context.Background(), cfg, 101, pass, nil)
			if err != nil {
				t.Fatal(err)
			}
			model := &retryingCriticModel{responses: []*agent.ModelResponse{{
				Items: []agent.RunItem{{
					Type:    agent.RunItemMessage,
					Message: &agent.MessageOutput{Text: "done"},
				}},
			}}}
			runner := agent.NewRunnerWithModel(model)
			worker := &agent.Agent{Name: "worker"}
			result, err := runner.Run(context.Background(), worker, nil, agent.RunConfig{Durable: run.RunConfig()})
			if err != nil {
				t.Fatal(err)
			}
			r := &chatRuntime{cfg: cfg, sc: sc, tx: transcriptState{items: result.NewItems}}
			turn := &userTurn{messageID: 101, claimPending: true}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			exit := r.commitTurn(ctx, turn,
				&preparedTurn{turnPolicy: &turnPolicy{}, durablePass: pass, storedRun: run},
				turnOutcome{result: result}, nil)
			if failTranscript {
				if exit == nil || exit.Status != "failed" || !turn.claimPending {
					t.Fatalf("failed commit exit=%+v turn=%+v", exit, turn)
				}
			} else if exit != nil || turn.claimPending {
				t.Fatalf("commit exit=%+v turn=%+v", exit, turn)
			}
			cfg.DurableRunOwner = "replacement"
			reopened, err := openSDKStoredRun(context.Background(), cfg, 101, pass, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := closeSDKStoredRun(reopened); err != nil {
					t.Error(err)
				}
			}()
			resume := reopened.RunConfig()
			if resume.Resume == nil || resume.Resume.Boundary != agent.DurableBoundaryRunCompleted {
				t.Fatalf("completed checkpoint not preserved: %+v", resume.Resume)
			}
			replayed, err := runner.Run(context.Background(), worker, nil, agent.RunConfig{Durable: resume})
			if err != nil || replayed.FinalText() != "done" {
				t.Fatalf("replayed=%+v err=%v", replayed, err)
			}
			model.mu.Lock()
			calls := model.calls
			model.mu.Unlock()
			if calls != 1 {
				t.Fatalf("model called %d times; completed pass must replay without new effects", calls)
			}
			state, err := sc.ReadWorkingState(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !failTranscript && (state.DurableRunPass != 0 || state.SelfAssistantMessageID == 0 || ss.transcriptWrites != 1) {
				t.Fatalf("commit state=%+v transcript writes=%d", state, ss.transcriptWrites)
			}
		})
	}
}
