package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/gratefulagents/internal/tools"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkdurable "github.com/gratefulagents/sdk/pkg/agentsdk/durable"
	agentpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func fastTransientRetries(t *testing.T) {
	t.Helper()
	base, limit := transientRetryBaseDelay, transientRetryMaxDelay
	transientRetryBaseDelay, transientRetryMaxDelay = time.Millisecond, time.Millisecond
	t.Cleanup(func() { transientRetryBaseDelay, transientRetryMaxDelay = base, limit })
}

func TestLoadProgressMetricsBaselineUsesMaximumAndFailsClosed(t *testing.T) {
	fastTransientRetries(t)
	sc, ss := newSubAgentCheckpointTestClient(t)
	ss.session.Metadata = json.RawMessage(`{"metrics":{"cost_usd":4.5,"input_tokens":10,"output_tokens":90}}`)
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"}}
	run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{CostUsd: "2.5", InputTokens: 100, OutputTokens: 30}
	unavailable := false
	reads := 0
	c := fake.NewClientBuilder().
		WithScheme(permissionModeScheme(t)).
		WithObjects(run).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(
				ctx context.Context, cl client.WithWatch,
				key client.ObjectKey, obj client.Object, opts ...client.GetOption,
			) error {
				reads++
				if unavailable || reads == 1 {
					return errors.New("API temporarily unavailable")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()
	baseline, err := loadProgressMetricsBaseline(context.Background(), c, sc, "run", "ns")
	if err != nil || baseline.CostUSD != 4.5 || baseline.InputTokens != 100 || baseline.OutputTokens != 90 || reads != 2 {
		t.Fatalf("baseline=%+v reads=%d err=%v", baseline, reads, err)
	}
	unavailable = true
	baseline, err = loadProgressMetricsBaseline(context.Background(), c, sc, "run", "ns")
	if err != nil || baseline.CostUSD != 4.5 {
		t.Fatalf("Postgres fallback=%+v err=%v", baseline, err)
	}
	ss.session.Metadata = json.RawMessage(`{"metrics":"invalid"}`)
	if _, err := loadProgressMetricsBaseline(context.Background(), c, sc, "run", "ns"); err == nil {
		t.Fatal("unreadable metrics must not reset spend to zero")
	}
	if _, err := isDelegatedChildFromCRD(context.Background(), c, "run", "ns"); err == nil {
		t.Fatal("unreadable delegation must not default to a user-facing run")
	}
}

func TestPreflightAllowsDegradedPodWithoutRemoteWrites(t *testing.T) {
	for _, missingProfile := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-profile-%v", missingProfile), func(t *testing.T) {
			run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"}}
			if missingProfile {
				run.Spec.RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: "missing"}
			}
			c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run).Build()
			r := &chatRuntime{
				cfg: runConfig{
					TaskName:        "run",
					Namespace:       "ns",
					GitRemoteWrites: agentpolicy.GitRemoteWritesDisabled,
				},
				crd:     c,
				tracker: agent.NewRunProgress(),
			}
			policy, action, exit := r.preflight(context.Background(), &userTurn{})
			if policy == nil || action != proceed || exit != nil {
				t.Fatalf("policy=%+v action=%v exit=%+v", policy, action, exit)
			}
		})
	}
}

type recoveryStore struct {
	transcriptErr    error
	transcriptWrites int
	history          []store.Message
	*stopQueueFakeStore
	appended          []store.Message
	activities        []string
	claimFailures     int
	messageReads      []int64
	loseClaimResponse bool
	claimedMessage    *store.Message
	claimToken        uuid.UUID
	claimCalls        int
	stoppedHook       func()
	inputTypes        []string
	sessionErr        error
	workingStateErr   error
	historyErr        error
}

func (s *recoveryStore) GetSession(ctx context.Context, id uuid.UUID) (*store.Session, error) {
	if s.sessionErr != nil {
		return nil, s.sessionErr
	}
	return s.stopQueueFakeStore.GetSession(ctx, id)
}

func (s *recoveryStore) UpdateSessionMetadataSection(ctx context.Context, id uuid.UUID, key string, mutate func(json.RawMessage) (json.RawMessage, error)) error {
	if key == "working_state" && s.workingStateErr != nil {
		return s.workingStateErr
	}
	return s.stopQueueFakeStore.UpdateSessionMetadataSection(ctx, id, key, mutate)
}

func (s *recoveryStore) GetMessages(context.Context, uuid.UUID) ([]store.Message, error) {
	return s.history, s.historyErr
}

func (s *recoveryStore) GetSessionTranscript(context.Context, uuid.UUID) ([]byte, error) {
	return nil, nil
}

func (s *recoveryStore) AppendMessage(
	ctx context.Context, _ uuid.UUID, role, content string, metadata json.RawMessage,
) (*store.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	msg := store.Message{ID: int64(100 + len(s.appended)), Role: role, Content: content, Metadata: metadata}
	s.appended = append(s.appended, msg)
	return &msg, nil
}

func (s *recoveryStore) WriteActivityEvent(
	_ context.Context, _ uuid.UUID, eventType, summary string, detail json.RawMessage,
) (*store.ActivityEvent, error) {
	s.activities = append(s.activities, eventType)
	return &store.ActivityEvent{}, nil
}

func (s *recoveryStore) ClaimUserMessage(
	ctx context.Context, id uuid.UUID, messageID int64, claim uuid.UUID,
) (*store.Message, bool, error) {
	s.claimCalls++
	if s.claimFailures > 0 {
		s.claimFailures--
		return nil, false, errors.New("Postgres failover")
	}
	if s.claimedMessage != nil && s.claimedMessage.ID == messageID {
		if s.claimToken == claim {
			return s.claimedMessage, true, nil
		}
		return nil, false, nil
	}
	msg, won, err := s.stopQueueFakeStore.ClaimUserMessage(ctx, id, messageID, claim)
	if err == nil && won {
		s.claimedMessage, s.claimToken = msg, claim
		if s.loseClaimResponse {
			s.loseClaimResponse = false
			return nil, false, errors.New("claim committed but response lost")
		}
	}
	return msg, won, err
}

func (s *recoveryStore) GetMessagesSince(_ context.Context, _ uuid.UUID, after int64) ([]store.Message, error) {
	s.messageReads = append(s.messageReads, after)
	return nil, nil
}

func (s *recoveryStore) SetPendingQuestion(_ context.Context, _ uuid.UUID, _ string, _ string, inputType string) error {
	s.inputTypes = append(s.inputTypes, inputType)
	if inputType == string(platformv1alpha1.UserInputStopped) && s.stoppedHook != nil {
		s.stoppedHook()
	}
	return nil
}

func newRecoveryClient(t *testing.T) (*sessionclient.Client, *recoveryStore) {
	t.Helper()
	ss := &recoveryStore{stopQueueFakeStore: newStopQueueTestStore(nil)}
	sc, err := sessionclient.New(context.Background(), ss, nil, "run", "ns", "running", "")
	if err != nil {
		t.Fatal(err)
	}
	return sc, ss
}

func TestShutdownBetweenPassesEnqueuesContinuationOnce(t *testing.T) {
	for _, claimPending := range []bool{true, false} {
		t.Run(fmt.Sprintf("pending-%v", claimPending), func(t *testing.T) {
			sc, ss := newRecoveryClient(t)
			r := &chatRuntime{sc: sc}
			turn := &userTurn{messageID: 1, claimPending: claimPending}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if exit := r.agentLoop(ctx, turn); exit == nil || exit.Status != "" {
				t.Fatalf("shutdown exit=%+v", exit)
			}
			r.enqueueContinuation(ctx, turn)
			want := 1
			if claimPending {
				want = 0
			}
			if len(ss.appended) != want {
				t.Fatalf("continuations=%d want=%d", len(ss.appended), want)
			}
			if want == 1 && (ss.appended[0].Role != "user" || ss.appended[0].Content != podResumeContinuationPrompt) {
				t.Fatalf("continuation=%+v", ss.appended[0])
			}
		})
	}
}

func TestClaimUserMessageRetriesTransientStoreErrors(t *testing.T) {
	fastTransientRetries(t)
	sc, ss := newRecoveryClient(t)
	ss.pending = []store.Message{{ID: 7, Role: "user", Content: "continue"}}
	ss.claimFailures = 2
	got, won, err := claimUserMessageWithRetry(context.Background(), sc,
		sessionclient.UserMessage{Message: store.Message{ID: 7, Content: "continue"}})
	if err != nil || !won || got.ID != 7 || ss.claimFailures != 0 {
		t.Fatalf("claimed=%+v won=%v err=%v", got, won, err)
	}
}

func TestClaimUserMessageRetriesCommittedWriteWithLostResponse(t *testing.T) {
	fastTransientRetries(t)
	sc, ss := newRecoveryClient(t)
	ss.pending = []store.Message{{ID: 7, Role: "user", Content: "continue"}}
	ss.loseClaimResponse = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, won, err := claimNextUserMessage(ctx, sc,
		[]sessionclient.UserMessage{{Message: ss.pending[0]}}, 0, map[int64]struct{}{})
	if err != nil || !won || got.ID != 7 || got.Content != "continue" || ss.claimCalls != 2 || len(ss.claimedIDs) != 1 {
		t.Fatalf("claimed=%+v won=%v calls=%d claimedIDs=%v err=%v", got, won, ss.claimCalls, ss.claimedIDs, err)
	}
}

func TestContinuationNudgeNotDuplicatedInTranscriptOrTail(t *testing.T) {
	sc, ss := newRecoveryClient(t)
	r := &chatRuntime{sc: sc}
	turn := &userTurn{tracker: &agent.AutoTracker{}}
	action, exit := r.decideNext(context.Background(), turn, turnOutcome{result: &agent.RunResult{}}, nil)
	if action != continueAgent || exit != nil || len(ss.appended) != 1 || turn.promptMessageID != ss.appended[0].ID {
		t.Fatalf("action=%v exit=%+v turn=%+v", action, exit, turn)
	}
	if items := outOfBandMessageItems(ss.appended, 0, sessionclient.WorkingState{}, 0, turn.promptMessageID); len(items) != 0 {
		t.Fatalf("nudge folded twice: %+v", items)
	}
	for _, item := range buildTurnInput(nil, ss.appended, sessionclient.WorkingState{}, turn.promptMessageID, 8) {
		if item.Message != nil && strings.Contains(item.Message.Text, turn.prompt) {
			t.Fatal("nudge duplicated in durable tail")
		}
	}
}

func TestLoadTurnMessagesUsesWatermarkAfterInitialLoad(t *testing.T) {
	sc, ss := newRecoveryClient(t)
	r := &chatRuntime{sc: sc, tx: transcriptState{floor: 2, seen: 50, items: []agent.RunItem{{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "history"}}}}}
	r.loadTurnMessages(context.Background(), nil, 2)
	r.loadTurnMessages(context.Background(), nil, 2)
	r.loadTurnMessages(context.Background(), nil, 60)
	if fmt.Sprint(ss.messageReads) != "[2 50 60]" {
		t.Fatalf("message query floors=%v", ss.messageReads)
	}
}

func TestDelegatedChildFailsInsteadOfParking(t *testing.T) {
	r := &chatRuntime{cfg: runConfig{DelegatedChild: true}}
	if exit := r.pauseForCostCap(context.Background(), "cap reached"); exit == nil || exit.Status != "failed" {
		t.Fatalf("cost pause=%+v", exit)
	}
	sc, _ := newRecoveryClient(t)
	r.sc = sc
	_, exit := r.pollSteering(context.Background(), &userTurn{autoLoopCount: agent.DefaultMaxAutoLoops})
	if exit == nil || exit.Status != "failed" {
		t.Fatalf("auto cap=%+v", exit)
	}
}

func (s *recoveryStore) UpsertSessionTranscript(ctx context.Context, _ uuid.UUID, _ []byte, _ int32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.transcriptWrites++
	return s.transcriptErr
}

func (s *recoveryStore) AppendAssistantForDurablePass(ctx context.Context, id uuid.UUID, _ uuid.UUID, _ string, content string) (*store.Message, error) {
	return s.AppendMessage(ctx, id, "assistant", content, nil)
}

func TestDecideNextIgnoresObsoleteMCPApprovalAnnotation(t *testing.T) {
	sc, _ := newRecoveryClient(t)
	r := &chatRuntime{sc: sc}
	post := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			"platform.gratefulagents.dev/mcp-break-glass-request": `{"id":"old-request","server":"github"}`,
		}},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseWaitingApproval},
	}
	action, exit := r.decideNext(context.Background(), &userTurn{tracker: &agent.AutoTracker{}}, turnOutcome{result: &agent.RunResult{}}, post)
	if action != continueAgent || exit != nil {
		t.Fatalf("obsolete MCP approval paused the run: action=%v exit=%+v", action, exit)
	}
}

func TestDelegatedChildInputAndCircuitBreakerAreTerminal(t *testing.T) {
	for _, toolName := range []string{"AskUserQuestion", "present_plan"} {
		t.Run(toolName, func(t *testing.T) {
			r := &chatRuntime{cfg: runConfig{DelegatedChild: true}}
			result := &agent.RunResult{NewItems: []agent.RunItem{{Type: agent.RunItemToolCall, ToolCall: &agent.ToolCallData{Name: toolName, Input: json.RawMessage(`{"question":"Continue?","summary":"Approve plan?"}`)}}}}
			_, exit := r.decideNext(context.Background(), &userTurn{tracker: &agent.AutoTracker{}}, turnOutcome{result: result}, nil)
			if exit == nil || exit.Status != "failed" {
				t.Fatalf("input pause exit=%+v", exit)
			}
		})
	}
	r := &chatRuntime{cfg: runConfig{DelegatedChild: true}}
	tracker := &agent.AutoTracker{}
	for range 20 {
		tracker.Update(nil)
	}
	_, exit := r.decideNext(context.Background(), &userTurn{tracker: tracker}, turnOutcome{result: &agent.RunResult{}}, nil)
	if exit == nil || exit.Status != "failed" || !strings.Contains(exit.Error, "circuit breaker") {
		t.Fatalf("breaker exit=%+v", exit)
	}
	sc, _ := newRecoveryClient(t)
	r.sc = sc
	if exit := r.parkAfterStop(context.Background(), 1, "", ""); exit == nil || exit.Status != "failed" {
		t.Fatalf("stop exit=%+v", exit)
	}
}

type stopBlockingModel struct {
	retryingCriticModel
	started chan struct{}
	stopped chan struct{}
}

func (m *stopBlockingModel) GetResponse(ctx context.Context, _ agent.ModelRequest) (*agent.ModelResponse, error) {
	close(m.started)
	<-ctx.Done()
	close(m.stopped)
	return nil, ctx.Err()
}

func (m *stopBlockingModel) StreamResponse(ctx context.Context, req agent.ModelRequest) (*agent.ModelStream, error) {
	_, err := m.GetResponse(ctx, req)
	return nil, err
}

func TestStopBeforeExecutionCancelsActiveChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	model := &stopBlockingModel{started: make(chan struct{}), stopped: make(chan struct{})}
	scheduler := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{
		Runner:     agent.NewRunnerWithModel(model),
		Agents:     map[string]*agent.Agent{"child": {Name: "child"}},
		Checkpoint: func(agent.SubAgentSchedulerCheckpoint) error { return nil },
	})
	defer scheduler.CancelAll()
	if _, err := scheduler.SpawnAsync(ctx, "child", "keep working", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.started:
	case <-ctx.Done():
		t.Fatal("child did not start")
	}
	sc, ss := newRecoveryClient(t)
	acknowledged := false
	ss.stoppedHook = func() {
		acknowledged = true
		if scheduler.HasActiveTasks() {
			t.Error("UI acknowledged Stopped with active child tasks")
		}
	}
	durableStore, err := sdkdurable.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := runConfig{TaskUID: "uid", DurableRunTenant: "tenant", DurableRunOwner: "old-pod", DurableRunStore: durableStore}
	held, err := openSDKStoredRun(ctx, cfg, 101, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeSDKStoredRun(held); err != nil {
			t.Error(err)
		}
	}()
	cfg.DurableRunOwner = "replacement"
	checks := 0
	_, err = openSDKStoredRun(ctx, cfg, 101, 1, func(context.Context) bool {
		checks++
		return checks > 1
	})
	if !errors.Is(err, errDurableRunOpenStopped) {
		t.Fatalf("lease wait stop=%v", err)
	}
	r := &chatRuntime{cfg: cfg, sc: sc, subAgentRegistry: scheduler}
	if exit := r.parkAfterStop(ctx, 101, "", ""); exit != nil {
		t.Fatalf("stop exit=%+v", exit)
	}
	if !acknowledged {
		t.Fatal("stop was not acknowledged")
	}
	select {
	case <-model.stopped:
	case <-ctx.Done():
		t.Fatal("child model call continued after acknowledged stop")
	}
}

func TestPreflightRefusesCappedTurnOnUnpricedModel(t *testing.T) {
	orig := modelPricingKnownForTurn
	t.Cleanup(func() { modelPricingKnownForTurn = orig })
	modelPricingKnownForTurn = func(_ runConfig, model string) bool { return model == "priced" }

	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"}}
	run.Spec.Model = "priced"
	run.Spec.Limits = &platformv1alpha1.AgentRunLimits{MaxCostUsd: "5"}
	c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run).Build()
	sc, ss := newRecoveryClient(t)
	cfg := runConfig{
		TaskName: "run", Namespace: "ns", Provider: "openai", GitRemoteWrites: agentpolicy.GitRemoteWritesDisabled,
	}
	r := &chatRuntime{cfg: cfg, crd: c, sc: sc, tracker: agent.NewRunProgress()}
	policy, action, exit := r.preflight(context.Background(), &userTurn{})
	if policy == nil || action != proceed || exit != nil {
		t.Fatalf("priced turn: policy=%+v action=%v exit=%+v", policy, action, exit)
	}

	// The model switches mid-run to one without pricing metadata.
	run.Spec.Model = "unpriced"
	if err := c.Update(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	policy, action, exit = r.preflight(context.Background(), &userTurn{})
	if policy != nil || action != awaitUser || exit != nil {
		t.Fatalf("unpriced turn: policy=%+v action=%v exit=%+v", policy, action, exit)
	}
	if fmt.Sprint(ss.activities) != "[cost_cap_unenforced]" || len(ss.inputTypes) != 1 ||
		ss.inputTypes[0] != string(platformv1alpha1.UserInputCircuitBreak) {
		t.Fatalf("activities=%v inputs=%v", ss.activities, ss.inputTypes)
	}
}

func TestReadTurnWorkingStateKeepsFloorOnReadError(t *testing.T) {
	fastTransientRetries(t)
	sc, ss := newRecoveryClient(t)
	ss.sessionErr = errors.New("Postgres failover")
	r := &chatRuntime{sc: sc, tx: transcriptState{floor: 42}}
	if state := r.readTurnWorkingState(context.Background()); state.HistoryFloorMessageID != 42 {
		t.Fatalf("floor=%d, want the in-memory floor 42", state.HistoryFloorMessageID)
	}
	ss.sessionErr = nil
	if err := sc.UpdateWorkingState(context.Background(), func(state *sessionclient.WorkingState) error {
		state.HistoryFloorMessageID = 50
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if state := r.readTurnWorkingState(context.Background()); state.HistoryFloorMessageID != 50 {
		t.Fatalf("floor=%d, want the durable floor 50", state.HistoryFloorMessageID)
	}
}

func TestAutonomousTurnMarkerSetAfterClaimAndClearedOnPark(t *testing.T) {
	for _, claimPending := range []bool{true, false} {
		t.Run(fmt.Sprintf("pending-%v", claimPending), func(t *testing.T) {
			sc, _ := newRecoveryClient(t)
			// No AgentRun: preflight fails right after the marker write.
			c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).Build()
			r := &chatRuntime{cfg: runConfig{TaskName: "run", Namespace: "ns"}, crd: c, sc: sc}
			if _, exit := r.runPass(context.Background(), &userTurn{messageID: 1, claimPending: claimPending}); exit == nil {
				t.Fatal("expected preflight failure")
			}
			state, err := sc.ReadWorkingState(context.Background())
			if err != nil || state.AutonomousTurnActive == claimPending || r.autonomousTurnMarked == claimPending {
				t.Fatalf("marker=%v marked=%v err=%v", state.AutonomousTurnActive, r.autonomousTurnMarked, err)
			}
		})
	}

	sc, _ := newRecoveryClient(t)
	r := &chatRuntime{sc: sc}
	r.setAutonomousTurnMarker(context.Background(), true)
	if exit := r.agentLoop(context.Background(), &userTurn{autoLoopCount: agent.DefaultMaxAutoLoops}); exit != nil {
		t.Fatalf("turn-limit park exit=%+v", exit)
	}
	if state, _ := sc.ReadWorkingState(context.Background()); state.AutonomousTurnActive || r.autonomousTurnMarked {
		t.Fatal("parking the turn must clear the marker")
	}
}

func TestRestoreResumesCrashedAutonomousTurnOnce(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending-%v", pending), func(t *testing.T) {
			sc, ss := newRecoveryClient(t)
			if pending { // A graceful exit already enqueued the continuation.
				ss.pending = []store.Message{{ID: 9, Role: "user", Content: podResumeContinuationPrompt}}
			}
			if err := sc.UpdateWorkingState(context.Background(), func(state *sessionclient.WorkingState) error {
				state.AutonomousTurnActive = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			r := &chatRuntime{sc: sc}
			if exit := r.restore(context.Background()); exit != nil {
				t.Fatalf("restore exit=%+v", exit)
			}
			want := 1
			if pending {
				want = 0
			}
			if len(ss.appended) != want || !r.autonomousTurnMarked {
				t.Fatalf("continuations=%d want=%d marked=%v", len(ss.appended), want, r.autonomousTurnMarked)
			}
			if want == 1 && ss.appended[0].Content != podResumeContinuationPrompt {
				t.Fatalf("continuation=%+v", ss.appended[0])
			}
		})
	}

	sc, ss := newRecoveryClient(t)
	if exit := (&chatRuntime{sc: sc}).restore(context.Background()); exit != nil || len(ss.appended) != 0 {
		t.Fatalf("no marker: exit=%+v continuations=%d", exit, len(ss.appended))
	}
}

func TestPreflightParksWhenGitPolicyRestartCannotBeRequested(t *testing.T) {
	run, profile := writeProfileRun()
	profile.Spec.Security.GitRemoteWrites = platformv1alpha1.GitRemoteWritesDisabled
	failPatch := interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return errors.New("API unavailable")
		},
	}
	c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run, profile).
		WithInterceptorFuncs(failPatch).Build()
	sc, ss := newRecoveryClient(t)
	cfg := runConfig{TaskName: run.Name, Namespace: run.Namespace, GitRemoteWrites: agentpolicy.GitRemoteWritesEnabled}
	r := &chatRuntime{cfg: cfg, crd: c, sc: sc, tracker: agent.NewRunProgress()}
	policy, action, exit := r.preflight(context.Background(), &userTurn{})
	if policy != nil || action != awaitUser || exit != nil {
		t.Fatalf("policy=%+v action=%v exit=%+v", policy, action, exit)
	}
	if fmt.Sprint(ss.activities) != "[runtime_config]" || len(ss.inputTypes) != 1 ||
		ss.inputTypes[0] != string(platformv1alpha1.UserInputCircuitBreak) {
		t.Fatalf("activities=%v inputs=%v", ss.activities, ss.inputTypes)
	}
}

func TestNextUserTurnPreparesResumeWithoutClaimingFollowup(t *testing.T) {
	sc, ss := newRecoveryClient(t)
	ss.history = []store.Message{{ID: 7, Role: "user", Content: "original request"}, {ID: 8, Role: "user", Content: "queued followup", DeliveryState: "pending"}}
	ss.pending = append([]store.Message(nil), ss.history[1:]...)
	if _, err := reserveSDKDurablePass(context.Background(), sc, 7); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sessionclient.RequestResume(ctx, ss, sc.SessionID(), "retry-1", "circuit-break-1"); err != nil {
		t.Fatal(err)
	}
	r := &chatRuntime{sc: sc}
	turn, exit := r.nextUserTurn(ctx)
	if exit != nil || turn == nil {
		t.Fatalf("turn=%+v exit=%+v", turn, exit)
	}
	if turn.prompt == "" || turn.messageID != 7 || turn.promptMessageID != 0 || turn.claimPending || !turn.firstPass {
		t.Fatalf("unexpected resume turn: %+v", turn)
	}
	if len(ss.pending) != 1 || ss.pending[0].Content != "queued followup" || len(ss.appended) != 0 || len(ss.claimedIDs) != 0 {
		t.Fatalf("resume wrote/claimed messages: appended=%v claimed=%v", ss.appended, ss.claimedIDs)
	}
	if again, err := sc.PendingResume(ctx); err != nil || again == nil {
		t.Fatalf("resume lost before commit: %v %v", again, err)
	}
	if pass, err := reserveSDKDurablePass(ctx, sc, turn.messageID); err != nil || pass != 2 {
		t.Fatalf("resume cannot start durable pass: pass=%d err=%v", pass, err)
	}
}

func TestRestoreWithResumeControlDoesNotQueueContinuation(t *testing.T) {
	sc, ss := newRecoveryClient(t)
	ctx := context.Background()
	if err := sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error { state.AutonomousTurnActive = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := sessionclient.RequestResume(ctx, ss, sc.SessionID(), "retry-1", "pending-1"); err != nil {
		t.Fatal(err)
	}
	r := &chatRuntime{sc: sc}
	if exit := r.restore(ctx); exit != nil {
		t.Fatalf("restore failed: %+v", exit)
	}
	if len(ss.appended) != 0 {
		t.Fatalf("resume queued continuation: %+v", ss.appended)
	}
	if req, err := sc.PendingResume(ctx); err != nil || req == nil {
		t.Fatalf("lost resume: %+v %v", req, err)
	}
}

func TestResumeControlClaimsRecoveredRequest(t *testing.T) {
	sc, ss := newRecoveryClient(t)
	ss.pending = []store.Message{{ID: 7, Role: "user", Content: "original request", DeliveryState: "pending"}}
	ss.pending = append(ss.pending, store.Message{ID: 8, Role: "user", Content: "unrelated followup", DeliveryState: "pending"})
	ss.history = append([]store.Message(nil), ss.pending...)
	if _, err := reserveSDKDurablePass(context.Background(), sc, 7); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sessionclient.RequestResume(ctx, ss, sc.SessionID(), "retry-1", "pending-1"); err != nil {
		t.Fatal(err)
	}
	r := &chatRuntime{sc: sc}
	turn, exit := r.nextUserTurn(ctx)
	if turn == nil || exit != nil || turn.messageID != 7 || len(ss.pending) != 1 || ss.pending[0].ID != 8 || len(ss.claimedIDs) != 1 {
		t.Fatalf("turn=%+v exit=%+v pending=%v claims=%v", turn, exit, ss.pending, ss.claimedIDs)
	}
	if len(ss.appended) != 0 {
		t.Fatalf("resume appended messages: %v", ss.appended)
	}
}

func TestResumePreparationFailureAndCrashRemainRecoverable(t *testing.T) {
	for _, failure := range []string{"history", "working-state", "crash"} {
		t.Run(failure, func(t *testing.T) {
			sc, ss := newRecoveryClient(t)
			ctx := context.Background()
			ss.history = []store.Message{{ID: 7, Role: "user", Content: "original"}}
			ss.pending = []store.Message{{ID: 8, Role: "user", Content: "followup", DeliveryState: "pending"}}
			if _, err := reserveSDKDurablePass(ctx, sc, 7); err != nil {
				t.Fatal(err)
			}
			if err := sessionclient.RequestResume(ctx, ss, sc.SessionID(), "retry", ""); err != nil {
				t.Fatal(err)
			}
			if failure == "history" {
				ss.historyErr = errors.New("history unavailable")
			}
			if failure == "working-state" {
				ss.workingStateErr = errors.New("write unavailable")
			}
			turn, exit := (&chatRuntime{sc: sc}).nextUserTurn(ctx)
			if failure != "crash" && (exit == nil || turn != nil) {
				t.Fatalf("turn=%+v exit=%+v", turn, exit)
			}
			if failure == "crash" && (exit != nil || turn == nil) {
				t.Fatalf("turn=%+v exit=%+v", turn, exit)
			}
			if req, err := sc.PendingResume(ctx); err != nil || req == nil {
				t.Fatalf("lost retry: %v %v", req, err)
			}
			ss.historyErr, ss.workingStateErr = nil, nil
			replacement := &chatRuntime{sc: sc}
			if exit := replacement.restore(ctx); exit != nil {
				t.Fatalf("restore=%+v", exit)
			}
			turn, exit = replacement.nextUserTurn(ctx)
			if exit != nil || turn == nil || turn.messageID != 7 {
				t.Fatalf("replacement turn=%+v exit=%+v", turn, exit)
			}
			if pass, err := reserveSDKDurablePass(ctx, sc, 7); err != nil || pass != 2 {
				t.Fatalf("pass=%d err=%v", pass, err)
			}
			if len(ss.pending) != 1 || ss.pending[0].ID != 8 || len(ss.claimedIDs) != 0 || len(ss.appended) != 0 {
				t.Fatalf("queue mutated: %+v claims=%v appended=%v", ss.pending, ss.claimedIDs, ss.appended)
			}
		})
	}
}
func TestResumeBeforeFirstClaimLeavesQueueIntact(t *testing.T) {
	sc, ss := newRecoveryClient(t)
	ctx := context.Background()
	ss.pending = []store.Message{{ID: 7, Role: "user", Content: "original", DeliveryState: "pending"}}
	ss.history = append([]store.Message(nil), ss.pending...)
	if err := sessionclient.RequestResume(ctx, ss, sc.SessionID(), "retry", ""); err != nil {
		t.Fatal(err)
	}
	if turn, exit := (&chatRuntime{sc: sc}).nextUserTurn(ctx); turn != nil || exit != nil {
		t.Fatalf("turn=%+v exit=%+v", turn, exit)
	}
	if len(ss.pending) != 1 || len(ss.claimedIDs) != 0 {
		t.Fatalf("pending=%v claims=%v", ss.pending, ss.claimedIDs)
	}
	if req, err := sc.PendingResume(ctx); err != nil || req != nil {
		t.Fatalf("empty resume=%v err=%v", req, err)
	}
}

func TestResumeAcknowledgedOnlyAfterDurableCommit(t *testing.T) {
	for _, failTranscript := range []bool{false, true} {
		t.Run(fmt.Sprint(failTranscript), func(t *testing.T) {
			sc, ss := newRecoveryClient(t)
			ctx := context.Background()
			ss.history = []store.Message{{ID: 7, Role: "user", Content: "original"}}
			if err := sessionclient.RequestResume(ctx, ss, sc.SessionID(), "retry", ""); err != nil {
				t.Fatal(err)
			}
			r := &chatRuntime{sc: sc, finishSummary: &tools.FinishSummaryHolder{}}
			turn, exit := r.nextUserTurn(ctx)
			if exit != nil || turn == nil {
				t.Fatalf("turn=%+v exit=%+v", turn, exit)
			}
			pass, err := reserveSDKDurablePass(ctx, sc, 7)
			if err != nil {
				t.Fatal(err)
			}
			if failTranscript {
				ss.transcriptErr = errors.New("transcript unavailable")
			}
			result := &agent.RunResult{NewItems: []agent.RunItem{{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "done"}}}}
			r.tx.items = result.NewItems
			err = r.commitTurnState(ctx, turn, &preparedTurn{turnPolicy: &turnPolicy{}, durablePass: pass}, result, nil)
			if (err != nil) != failTranscript {
				t.Fatalf("commit error=%v", err)
			}
			req, err := sc.PendingResume(ctx)
			if err != nil || (req != nil) != failTranscript {
				t.Fatalf("pending=%v err=%v", req, err)
			}
		})
	}
}
