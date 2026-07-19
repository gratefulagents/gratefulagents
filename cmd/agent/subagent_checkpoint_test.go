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

func TestSubAgentCheckpointRestoresTaskIDsAndTombstones(t *testing.T) {
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
	if !strings.Contains(notice, "failed tombstones") {
		t.Fatalf("notice = %q", notice)
	}
	if got, err := restored.GetStatus("task_done"); err != nil || got.Result != "approved" {
		t.Fatalf("done = %+v, %v", got, err)
	}
	if got, err := restored.GetStatus("task_active"); err != nil || got.Status != agent.SubAgentTaskFailed || !strings.Contains(got.Error, "runtime restarted") {
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
