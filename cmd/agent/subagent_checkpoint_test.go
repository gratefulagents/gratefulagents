package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

type subAgentCheckpointStore struct{ transcriptFakeStore }

func (s *subAgentCheckpointStore) GetSession(context.Context, uuid.UUID) (*store.Session, error) {
	return s.session, nil
}
func (s *subAgentCheckpointStore) MergeSessionMetadata(_ context.Context, _ uuid.UUID, key string, value json.RawMessage) error {
	var metadata map[string]json.RawMessage
	if len(s.session.Metadata) > 0 {
		if err := json.Unmarshal(s.session.Metadata, &metadata); err != nil {
			return err
		}
	}
	if metadata == nil {
		metadata = make(map[string]json.RawMessage)
	}
	metadata[key] = append(json.RawMessage(nil), value...)
	encoded, err := json.Marshal(metadata)
	if err == nil {
		s.session.Metadata = encoded
	}
	return err
}
func newSubAgentCheckpointTestClient(t *testing.T) (*sessionclient.Client, *subAgentCheckpointStore) {
	t.Helper()
	fake := &subAgentCheckpointStore{transcriptFakeStore: transcriptFakeStore{session: &store.Session{ID: uuid.New()}}}
	sc, err := sessionclient.New(context.Background(), fake, nil, "run", "ns", "running", "")
	if err != nil {
		t.Fatalf("sessionclient.New: %v", err)
	}
	return sc, fake
}

func TestSubAgentCheckpointRestoresTaskIDsAndReconcilingState(t *testing.T) {
	sc, _ := newSubAgentCheckpointTestClient(t)
	state := agent.SubAgentSchedulerCheckpoint{Records: []agent.SubAgentSchedulerCheckpointRecord{
		{Task: agent.SubAgentTask{ID: "task_done", AgentName: "reviewer", Status: agent.SubAgentTaskCompleted, Result: "approved"}},
		{Task: agent.SubAgentTask{ID: "task_active", AgentName: "executor", Status: agent.SubAgentTaskRunning, Message: "finish work"}},
	}}
	raw, _ := json.Marshal(persistedSubAgentCheckpoint{Version: 1, State: state})
	if err := sc.WriteSubAgentCheckpoint(context.Background(), raw); err != nil {
		t.Fatal(err)
	}

	restored := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{})
	notice, err := restoreSubAgentCheckpoint(context.Background(), sc, restored)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "reconciling") {
		t.Fatalf("notice = %q", notice)
	}
	if got, err := restored.GetStatus("task_done"); err != nil || got.Result != "approved" {
		t.Fatalf("done = %+v, %v", got, err)
	}
	if got, err := restored.GetStatus("task_active"); err != nil ||
		got.Status != agent.SubAgentTaskReconciling ||
		!strings.Contains(got.Error, "runtime restarted") {
		t.Fatalf("active = %+v, %v", got, err)
	}
}

func TestEmptyReplacementSchedulerDoesNotErasePreviousCheckpoint(t *testing.T) {
	sc, _ := newSubAgentCheckpointTestClient(t)
	previousState := agent.SubAgentSchedulerCheckpoint{Records: []agent.SubAgentSchedulerCheckpointRecord{{Task: agent.SubAgentTask{ID: "task_old", Status: agent.SubAgentTaskRunning}}}}
	previous, _ := json.Marshal(persistedSubAgentCheckpoint{Version: 1, State: previousState})
	if err := sc.WriteSubAgentCheckpoint(context.Background(), previous); err != nil {
		t.Fatal(err)
	}

	empty := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{})
	writer := startSubAgentCheckpointLoop(sc, empty)
	writer.StopAndFlush()
	got, err := sc.ReadSubAgentCheckpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(previous) {
		t.Fatalf("empty scheduler replaced prior checkpoint: got %s want %s", got, previous)
	}
}

func TestSubAgentCheckpointPreservesCompleteHistory(t *testing.T) {
	sc, _ := newSubAgentCheckpointTestClient(t)
	scheduler := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{})
	state := agent.SubAgentSchedulerCheckpoint{Records: make([]agent.SubAgentSchedulerCheckpointRecord, 300)}
	for i := range state.Records {
		state.Records[i].Task = agent.SubAgentTask{ID: fmt.Sprintf("task_%03d", i), Status: agent.SubAgentTaskCompleted, Result: strings.Repeat("result", 100)}
	}
	if err := scheduler.RestoreSchedulerCheckpoint(state); err != nil {
		t.Fatal(err)
	}
	writer := startSubAgentCheckpointLoop(sc, scheduler)
	if err := writer.persistCheckpoint(scheduler.SchedulerCheckpoint()); err != nil {
		t.Fatal(err)
	}
	if err := writer.StopAndFlush(); err != nil {
		t.Fatal(err)
	}
	raw, err := sc.ReadSubAgentCheckpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var saved persistedSubAgentCheckpoint
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.State.Records) != len(state.Records) || saved.State.Records[0].Task.Result != state.Records[0].Task.Result {
		t.Fatalf("checkpoint lost accepted task history: got %d records", len(saved.State.Records))
	}
}

func TestSubAgentCheckpointPreservesOptedInParentContext(t *testing.T) {
	sc, _ := newSubAgentCheckpointTestClient(t)
	scheduler := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{})
	state := agent.SubAgentSchedulerCheckpoint{Records: []agent.SubAgentSchedulerCheckpointRecord{{
		Task: agent.SubAgentTask{ID: "task_shared", AgentName: "executor", Status: agent.SubAgentTaskCompleted},
		ParentContext: []agent.LLMRunItemSnapshot{{
			Type:        "message",
			AgentName:   "parent",
			MessageText: "decision needed by the child",
		}},
	}}}
	if err := scheduler.RestoreSchedulerCheckpoint(state); err != nil {
		t.Fatal(err)
	}

	writer := startSubAgentCheckpointLoop(sc, scheduler)
	t.Cleanup(func() {
		if err := writer.StopAndFlush(); err != nil {
			t.Errorf("flush sub-agent checkpoint: %v", err)
		}
	})
	if err := writer.persistCheckpoint(scheduler.SchedulerCheckpoint()); err != nil {
		t.Fatal(err)
	}
	raw, err := sc.ReadSubAgentCheckpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	restored := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{})
	if _, err := restoreSubAgentCheckpoint(context.Background(), sc, restored); err != nil {
		t.Fatal(err)
	}
	checkpoint := restored.SchedulerCheckpoint()
	if len(checkpoint.Records) != 1 || len(checkpoint.Records[0].ParentContext) != 1 {
		t.Fatalf("restored checkpoint lost parent context: %s", raw)
	}
	got := checkpoint.Records[0].ParentContext[0]
	if got.AgentName != "parent" || got.MessageText != "decision needed by the child" {
		t.Fatalf("restored parent context = %+v", got)
	}
}

// A restored task whose child checkpoint sits at an unreconcilable boundary is
// attempted exactly once, reported as stuck, and never retried on later turns.
func TestResumeReconcilingSubAgentTasksAttemptsOnceAndReportsStuck(t *testing.T) {
	registry := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{
		Checkpoint: func(agent.SubAgentSchedulerCheckpoint) error { return nil },
	})
	record := agent.SubAgentSchedulerCheckpointRecord{
		Task: agent.SubAgentTask{
			ID: "task_stuck", AgentName: "executor", Status: agent.SubAgentTaskRunning, Message: "finish work",
		},
		DurableCheckpoint: &agent.DurableCheckpoint{
			SchemaVersion: agent.DurableCheckpointSchemaVersion,
			Boundary:      agent.DurableBoundaryModelCompleted,
		},
	}
	// The baseline type is not re-exported by pkg/agentsdk; populate it through
	// JSON so the resume passes the security check and reaches the boundary
	// decision.
	if err := json.Unmarshal([]byte(`{"security_baseline":{"tool_access_level":"full"}}`), &record); err != nil {
		t.Fatal(err)
	}
	state := agent.SubAgentSchedulerCheckpoint{Records: []agent.SubAgentSchedulerCheckpointRecord{record}}
	if err := registry.RestoreSchedulerCheckpoint(state); err != nil {
		t.Fatal(err)
	}
	attempted := map[string]struct{}{}

	stuck := resumeReconcilingSubAgentTasks(context.Background(), registry, attempted)
	if len(stuck) != 1 || stuck[0].ID != "task_stuck" || stuck[0].AgentName != "executor" {
		t.Fatalf("stuck = %+v", stuck)
	}
	if !strings.Contains(stuck[0].Reason, "reconciliation") {
		t.Fatalf("reason = %q", stuck[0].Reason)
	}
	if _, ok := attempted["task_stuck"]; !ok {
		t.Fatal("failed resume was not recorded as attempted")
	}

	// Second turn: still reconciling, but not retried and not re-reported.
	if again := resumeReconcilingSubAgentTasks(context.Background(), registry, attempted); len(again) != 0 {
		t.Fatalf("second attempt re-reported stuck tasks: %+v", again)
	}
	task, _ := registry.GetStatus("task_stuck")
	if task.Status != agent.SubAgentTaskReconciling {
		t.Fatalf("status = %q, want reconciling", task.Status)
	}

	notice := stuckSubAgentNotice(stuck)
	for _, want := range []string{"[SYSTEM]", "task_stuck", "executor", `subagent_control action="cancel"`} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice missing %q:\n%s", want, notice)
		}
	}
	if activity := stuckSubAgentActivity(stuck); !strings.Contains(activity, "task_stuck") {
		t.Fatalf("activity = %q", activity)
	}
	if stuckSubAgentNotice(nil) != "" || stuckSubAgentActivity(nil) != "" {
		t.Fatal("empty stuck list must produce no notice")
	}
}

func TestResumeReconcilingSubAgentTasksSkipsTerminalAndNilRegistry(t *testing.T) {
	if got := resumeReconcilingSubAgentTasks(context.Background(), nil, map[string]struct{}{}); got != nil {
		t.Fatalf("nil registry returned %+v", got)
	}
	registry := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{
		Checkpoint: func(agent.SubAgentSchedulerCheckpoint) error { return nil },
	})
	state := agent.SubAgentSchedulerCheckpoint{Records: []agent.SubAgentSchedulerCheckpointRecord{
		{Task: agent.SubAgentTask{ID: "task_done", AgentName: "reviewer", Status: agent.SubAgentTaskCompleted, Result: "ok"}},
	}}
	if err := registry.RestoreSchedulerCheckpoint(state); err != nil {
		t.Fatal(err)
	}
	attempted := map[string]struct{}{}
	got := resumeReconcilingSubAgentTasks(context.Background(), registry, attempted)
	if len(got) != 0 || len(attempted) != 0 {
		t.Fatalf("terminal task was touched: stuck=%+v attempted=%v", got, attempted)
	}
}

func TestResumeReconcilingSubAgentTasksClassifiesSentinelErrors(t *testing.T) {
	registry := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{
		Checkpoint: func(agent.SubAgentSchedulerCheckpoint) error { return nil },
	})
	// One task needs reconciliation (unresolved model_completed boundary); the
	// other is rejected by configuration (no agent named "ghost" is registered).
	needsReconcile := agent.SubAgentSchedulerCheckpointRecord{
		Task: agent.SubAgentTask{ID: "task_reconcile", AgentName: "executor", Status: agent.SubAgentTaskRunning},
		DurableCheckpoint: &agent.DurableCheckpoint{
			SchemaVersion: agent.DurableCheckpointSchemaVersion,
			Boundary:      agent.DurableBoundaryModelCompleted,
		},
	}
	rejected := agent.SubAgentSchedulerCheckpointRecord{
		Task: agent.SubAgentTask{ID: "task_rejected", AgentName: "ghost", Status: agent.SubAgentTaskPending},
	}
	for _, record := range []*agent.SubAgentSchedulerCheckpointRecord{&needsReconcile, &rejected} {
		if err := json.Unmarshal([]byte(`{"security_baseline":{"tool_access_level":"full"}}`), record); err != nil {
			t.Fatal(err)
		}
	}
	state := agent.SubAgentSchedulerCheckpoint{
		Records: []agent.SubAgentSchedulerCheckpointRecord{needsReconcile, rejected},
	}
	if err := registry.RestoreSchedulerCheckpoint(state); err != nil {
		t.Fatal(err)
	}
	attempted := map[string]struct{}{}
	stuck := resumeReconcilingSubAgentTasks(context.Background(), registry, attempted)
	if len(stuck) != 2 {
		t.Fatalf("stuck = %+v", stuck)
	}
	reasons := map[string]string{}
	for _, task := range stuck {
		reasons[task.ID] = task.Reason
	}
	if !strings.Contains(reasons["task_reconcile"], agent.ErrSubAgentReconciliationRequired.Error()) {
		t.Fatalf("task_reconcile reason = %q", reasons["task_reconcile"])
	}
	if !strings.Contains(reasons["task_rejected"], agent.ErrSubAgentResumeRejected.Error()) {
		t.Fatalf("task_rejected reason = %q", reasons["task_rejected"])
	}
	if len(attempted) != 2 {
		t.Fatalf("attempted = %v", attempted)
	}
}

// A registry without a checkpoint hook fails resume with a non-sentinel error;
// that is unexpected rather than permanent, so it is retried next turn and not
// reported as stuck.
func TestResumeReconcilingSubAgentTasksRetriesNonSentinelErrors(t *testing.T) {
	registry := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{})
	state := agent.SubAgentSchedulerCheckpoint{Records: []agent.SubAgentSchedulerCheckpointRecord{
		{Task: agent.SubAgentTask{ID: "task_active", AgentName: "executor", Status: agent.SubAgentTaskRunning}},
	}}
	if err := registry.RestoreSchedulerCheckpoint(state); err != nil {
		t.Fatal(err)
	}
	attempted := map[string]struct{}{}
	if stuck := resumeReconcilingSubAgentTasks(context.Background(), registry, attempted); len(stuck) != 0 {
		t.Fatalf("transient error reported as stuck: %+v", stuck)
	}
	if _, marked := attempted["task_active"]; marked {
		t.Fatal("transient error must leave the task eligible for retry")
	}
}
