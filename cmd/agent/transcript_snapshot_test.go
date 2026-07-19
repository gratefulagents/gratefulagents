package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// transcriptFakeStore implements just enough of store.StateStore (via
// interface embedding) for sessionclient transcript persistence.
type transcriptFakeStore struct {
	store.StateStore
	session    *store.Session
	transcript []byte
	itemCount  int32
	upserts    int
	deletes    int
	failUpsert error
}

func (s *transcriptFakeStore) GetSessionByRun(context.Context, string, string) (*store.Session, error) {
	return s.session, nil
}

func (s *transcriptFakeStore) GetResourceOwner(context.Context, string, string, string) (*store.ResourceOwnership, error) {
	return nil, nil
}

func (s *transcriptFakeStore) UpsertSessionTranscript(_ context.Context, _ uuid.UUID, data []byte, itemCount int32) error {
	if s.failUpsert != nil {
		return s.failUpsert
	}
	s.upserts++
	s.transcript = append([]byte(nil), data...)
	s.itemCount = itemCount
	return nil
}

func (s *transcriptFakeStore) GetSessionTranscript(context.Context, uuid.UUID) ([]byte, error) {
	if s.transcript == nil {
		return nil, nil
	}
	return append([]byte(nil), s.transcript...), nil
}

func (s *transcriptFakeStore) DeleteSessionTranscript(context.Context, uuid.UUID) error {
	s.deletes++
	s.transcript = nil
	s.itemCount = 0
	return nil
}

func newTranscriptTestClient(t *testing.T) (*sessionclient.Client, *transcriptFakeStore) {
	t.Helper()
	fake := &transcriptFakeStore{session: &store.Session{ID: uuid.New()}}
	sc, err := sessionclient.New(context.Background(), fake, nil, "run", "ns", "running", "")
	if err != nil {
		t.Fatalf("sessionclient.New: %v", err)
	}
	return sc, fake
}

func sampleTranscriptItems() []agent.RunItem {
	return []agent.RunItem{
		{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "user asks"}},
		{
			Type:      agent.RunItemReasoning,
			Agent:     &agent.Agent{Name: "main"},
			Reasoning: &agent.ReasoningData{ID: "r1", Text: "thinking", Signature: "sig", EncryptedContent: "enc"},
		},
		{
			Type:     agent.RunItemToolCall,
			Agent:    &agent.Agent{Name: "main"},
			ToolCall: &agent.ToolCallData{ID: "call-1", Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)},
		},
		{
			Type:       agent.RunItemToolOutput,
			ToolOutput: &agent.ToolOutputData{CallID: "call-1", Content: "file contents", IsError: false},
		},
		{
			Type:        agent.RunItemHandoffCall,
			Agent:       &agent.Agent{Name: "main"},
			HandoffCall: &agent.HandoffCallData{FromAgent: "main", ToAgent: "executor"},
		},
		{
			Type:          agent.RunItemHandoffOutput,
			HandoffOutput: &agent.HandoffOutputData{FromAgent: "main", ToAgent: "executor"},
		},
		{
			Type:         agent.RunItemToolApproval,
			ToolApproval: &agent.ToolApprovalData{ToolName: "bash", Input: json.RawMessage(`{}`), CallID: "call-2", Approved: true},
		},
		{
			Type:       agent.RunItemCompaction,
			Agent:      &agent.Agent{Name: "main"},
			Compaction: &agent.CompactionData{ID: "c1", Content: "summary of dropped history", CreatedBy: "compactor"},
		},
		{Type: agent.RunItemMessage, Agent: &agent.Agent{Name: "main"}, Message: &agent.MessageOutput{Text: "assistant replies"}},
	}
}

func TestTranscriptSnapshotRoundTripPreservesAllItemTypes(t *testing.T) {
	items := sampleTranscriptItems()

	persisted, ok := persistedItemsFromRun(items)
	if !ok {
		t.Fatal("persistedItemsFromRun returned !ok")
	}
	data, err := encodeTranscriptSnapshot(transcriptSnapshot{
		Version:                transcriptSnapshotVersion,
		FloorMessageID:         3,
		SeenMessageID:          17,
		SelfAssistantMessageID: 18,
		Items:                  persisted,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	snap, err := decodeTranscriptSnapshot(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.FloorMessageID != 3 || snap.SeenMessageID != 17 || snap.SelfAssistantMessageID != 18 {
		t.Fatalf("watermarks lost: %+v", snap)
	}

	restored, ok := runItemsFromPersisted(snap.Items)
	if !ok {
		t.Fatal("runItemsFromPersisted returned !ok")
	}
	if len(restored) != len(items) {
		t.Fatalf("restored %d items, want %d", len(restored), len(items))
	}
	for i, item := range restored {
		if item.Type != items[i].Type {
			t.Errorf("item %d type = %v, want %v", i, item.Type, items[i].Type)
		}
		if (item.Agent != nil) != (items[i].Agent != nil) {
			t.Errorf("item %d agent presence = %v, want %v", i, item.Agent != nil, items[i].Agent != nil)
		}
		if item.Agent != nil && item.Agent.Name != items[i].Agent.Name {
			t.Errorf("item %d agent name = %q, want %q", i, item.Agent.Name, items[i].Agent.Name)
		}
	}
	if restored[2].ToolCall == nil || restored[2].ToolCall.ID != "call-1" || string(restored[2].ToolCall.Input) != `{"path":"a.go"}` {
		t.Errorf("tool call not preserved: %+v", restored[2].ToolCall)
	}
	if restored[3].ToolOutput == nil || restored[3].ToolOutput.CallID != "call-1" || restored[3].ToolOutput.Content != "file contents" {
		t.Errorf("tool output not preserved: %+v", restored[3].ToolOutput)
	}
	if restored[1].Reasoning == nil || restored[1].Reasoning.Signature != "sig" || restored[1].Reasoning.EncryptedContent != "enc" {
		t.Errorf("reasoning not preserved: %+v", restored[1].Reasoning)
	}
	if restored[7].Compaction == nil || restored[7].Compaction.Content != "summary of dropped history" {
		t.Errorf("compaction not preserved: %+v", restored[7].Compaction)
	}
}

func TestPersistedItemsFromRunStripsImages(t *testing.T) {
	items := []agent.RunItem{
		{Type: agent.RunItemMessage, Message: &agent.MessageOutput{
			Text:   "look at this",
			Images: []agent.ImageAttachment{{MediaType: "image/png", Data: strings.Repeat("A", 4096)}},
		}},
		{Type: agent.RunItemMessage, Message: &agent.MessageOutput{
			Images: []agent.ImageAttachment{{MediaType: "image/png", Data: "xyz"}},
		}},
	}

	persisted, ok := persistedItemsFromRun(items)
	if !ok {
		t.Fatal("persistedItemsFromRun returned !ok")
	}
	if len(persisted[0].Message.Images) != 0 {
		t.Error("images not stripped from message with text")
	}
	if persisted[0].Message.Text != "look at this" {
		t.Errorf("text changed: %q", persisted[0].Message.Text)
	}
	if persisted[1].Message.Text != transcriptImagePlaceholder {
		t.Errorf("image-only message text = %q, want placeholder", persisted[1].Message.Text)
	}
	// Source items must not be mutated (the loop keeps using them in memory).
	if len(items[0].Message.Images) != 1 || items[1].Message.Text != "" {
		t.Error("persistedItemsFromRun mutated source items")
	}
}

func TestRunItemsFromPersistedRejectsUnknownType(t *testing.T) {
	if _, ok := runItemsFromPersisted([]persistedRunItem{{Type: "hologram"}}); ok {
		t.Fatal("expected !ok for unknown item type")
	}
	if _, ok := persistedItemsFromRun([]agent.RunItem{{Type: agent.RunItemType(99)}}); ok {
		t.Fatal("expected !ok for unknown run item type")
	}
}

func TestDecodeTranscriptSnapshotRejectsVersionAndGarbage(t *testing.T) {
	data, err := encodeTranscriptSnapshot(transcriptSnapshot{Version: transcriptSnapshotVersion + 1})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := decodeTranscriptSnapshot(data); err == nil {
		t.Fatal("expected version error")
	}
	if _, err := decodeTranscriptSnapshot([]byte("not gzip")); err == nil {
		t.Fatal("expected decode error for garbage")
	}
}

func TestPersistAndLoadTranscriptSnapshot(t *testing.T) {
	ctx := context.Background()
	sc, fake := newTranscriptTestClient(t)

	persistTranscriptSnapshot(ctx, sc, sampleTranscriptItems(), 3, 17, 18)
	if fake.upserts != 1 {
		t.Fatalf("upserts = %d, want 1", fake.upserts)
	}
	if fake.itemCount != int32(len(sampleTranscriptItems())) {
		t.Fatalf("itemCount = %d, want %d", fake.itemCount, len(sampleTranscriptItems()))
	}

	restored := loadTranscriptSnapshot(ctx, sc, 3)
	if restored == nil {
		t.Fatal("loadTranscriptSnapshot returned nil")
	}
	if len(restored.Items) != len(sampleTranscriptItems()) {
		t.Fatalf("restored %d items, want %d", len(restored.Items), len(sampleTranscriptItems()))
	}
	if restored.SeenMessageID != 17 || restored.SelfAssistantMessageID != 18 || restored.FloorMessageID != 3 {
		t.Fatalf("watermarks wrong: %+v", restored)
	}
	if restored.PendingUserMessageID != 0 {
		t.Fatalf("turn-end snapshot pending id = %d, want 0 (no in-flight prompt)", restored.PendingUserMessageID)
	}
}

func TestLoadTranscriptSnapshotDiscardsWhenFloorMoved(t *testing.T) {
	ctx := context.Background()
	sc, fake := newTranscriptTestClient(t)

	persistTranscriptSnapshot(ctx, sc, sampleTranscriptItems(), 3, 17, 18)
	if restored := loadTranscriptSnapshot(ctx, sc, 42); restored != nil {
		t.Fatal("expected nil for moved history floor")
	}
	if fake.transcript != nil {
		t.Fatal("stale snapshot not deleted")
	}
}

func TestLoadTranscriptSnapshotHandlesMissingAndCorrupt(t *testing.T) {
	ctx := context.Background()
	sc, fake := newTranscriptTestClient(t)

	if restored := loadTranscriptSnapshot(ctx, sc, 0); restored != nil {
		t.Fatal("expected nil when no snapshot stored")
	}

	fake.transcript = []byte("corrupt")
	if restored := loadTranscriptSnapshot(ctx, sc, 0); restored != nil {
		t.Fatal("expected nil for corrupt snapshot")
	}
	if fake.transcript != nil {
		t.Fatal("corrupt snapshot not deleted")
	}
}

func TestPersistTranscriptSnapshotEmptyClearsRow(t *testing.T) {
	ctx := context.Background()
	sc, fake := newTranscriptTestClient(t)

	persistTranscriptSnapshot(ctx, sc, sampleTranscriptItems(), 0, 0, 0)
	if fake.transcript == nil {
		t.Fatal("expected stored snapshot")
	}
	// Interrupted-run reset: empty transcript clears the durable row.
	persistTranscriptSnapshot(ctx, sc, nil, 0, 0, 0)
	if fake.transcript != nil {
		t.Fatal("empty transcript did not clear stored snapshot")
	}
}

// The mid-turn persist used by the interrupted-turn, failed-turn, budget, and
// pod-termination paths: it must record the in-flight prompt as pending, and
// an empty transcript must keep the previously stored snapshot instead of
// clearing it — a user stop or turn failure without partial-result state must
// never wipe the last completed turn's resume context.
func TestPersistInFlightTranscriptSnapshotPendingAndEmptyKeep(t *testing.T) {
	ctx := context.Background()
	sc, fake := newTranscriptTestClient(t)

	persistInFlightTranscriptSnapshot(ctx, sc, sampleTranscriptItems(), 3, 17, 18, 99)
	if fake.upserts != 1 {
		t.Fatalf("upserts = %d, want 1", fake.upserts)
	}
	restored := loadTranscriptSnapshot(ctx, sc, 3)
	if restored == nil {
		t.Fatal("loadTranscriptSnapshot returned nil")
	}
	if restored.PendingUserMessageID != 99 {
		t.Fatalf("pending id = %d, want 99 (in-flight prompt rides along)", restored.PendingUserMessageID)
	}

	persistInFlightTranscriptSnapshot(ctx, sc, nil, 3, 17, 18, 100)
	if fake.upserts != 1 || fake.deletes != 0 || fake.transcript == nil {
		t.Fatalf("empty in-flight persist must be a no-op, got upserts=%d deletes=%d stored=%v",
			fake.upserts, fake.deletes, fake.transcript != nil)
	}
}

func TestPersistTranscriptSnapshotEnforcesByteCap(t *testing.T) {
	ctx := context.Background()
	sc, fake := newTranscriptTestClient(t)
	t.Setenv("TRANSCRIPT_SNAPSHOT_MAX_BYTES", "64")

	persistTranscriptSnapshot(ctx, sc, sampleTranscriptItems(), 0, 0, 0)
	if fake.transcript != nil {
		t.Fatal("oversized snapshot should not be stored")
	}
	if fake.deletes == 0 {
		t.Fatal("oversized snapshot should clear any previous row")
	}
}

func TestPersistTranscriptSnapshotDisabledByZeroCap(t *testing.T) {
	ctx := context.Background()
	sc, fake := newTranscriptTestClient(t)
	t.Setenv("TRANSCRIPT_SNAPSHOT_MAX_BYTES", "0")

	persistTranscriptSnapshot(ctx, sc, sampleTranscriptItems(), 0, 0, 0)
	if fake.upserts != 0 {
		t.Fatal("persistence should be disabled when cap is zero")
	}
}

// --- Pod-termination flush (pause/wake pod deletion) ---

// flushFakeStore extends the transcript fake with the metadata/activity
// surface flushPodTerminationState touches.
type flushFakeStore struct {
	transcriptFakeStore
	metadataMerges int
	activityEvents []string
}

func (s *flushFakeStore) GetSession(context.Context, uuid.UUID) (*store.Session, error) {
	return s.session, nil
}

func (s *flushFakeStore) MergeSessionMetadata(context.Context, uuid.UUID, string, json.RawMessage) error {
	s.metadataMerges++
	return nil
}

func (s *flushFakeStore) WriteActivityEvent(_ context.Context, _ uuid.UUID, eventType, _ string, _ json.RawMessage) (*store.ActivityEvent, error) {
	s.activityEvents = append(s.activityEvents, eventType)
	return &store.ActivityEvent{}, nil
}

func newFlushTestClient(t *testing.T) (*sessionclient.Client, *flushFakeStore) {
	t.Helper()
	fake := &flushFakeStore{transcriptFakeStore: transcriptFakeStore{session: &store.Session{ID: uuid.New()}}}
	sc, err := sessionclient.New(context.Background(), fake, nil, "run", "ns", "running", "")
	if err != nil {
		t.Fatalf("sessionclient.New: %v", err)
	}
	return sc, fake
}

// A pod termination mid-turn must persist the interrupted turn's partial
// transcript so the resumed run continues with full context instead of
// amnesia about the in-flight work. The prompt that started the turn is
// recorded as pending: the resume cursor re-delivers it, and the resumed
// loop must not replay it verbatim on top of the preserved progress.
func TestFlushPodTerminationStatePersistsPartialTranscript(t *testing.T) {
	sc, fake := newFlushTestClient(t)

	result := &agent.RunResult{
		NewItems:     sampleTranscriptItems(),
		FinalHistory: sampleTranscriptItems(),
	}
	flushPodTerminationState(sc, result, 3, 17, 18, 9)

	if fake.upserts != 1 {
		t.Fatalf("transcript upserts = %d, want 1", fake.upserts)
	}
	if fake.metadataMerges == 0 {
		t.Error("expected working-state summary to be persisted")
	}
	if len(fake.activityEvents) != 1 || fake.activityEvents[0] != "pod_terminating" {
		t.Errorf("activity events = %v, want [pod_terminating]", fake.activityEvents)
	}

	restored := loadTranscriptSnapshot(context.Background(), sc, 3)
	if restored == nil {
		t.Fatal("expected the flushed transcript to be restorable")
	}
	if len(restored.Items) != len(sampleTranscriptItems()) {
		t.Fatalf("restored %d items, want %d", len(restored.Items), len(sampleTranscriptItems()))
	}
	if restored.SeenMessageID != 17 || restored.SelfAssistantMessageID != 18 {
		t.Fatalf("watermarks wrong: %+v", restored)
	}
	if restored.PendingUserMessageID != 9 {
		t.Fatalf("pending user message id = %d, want 9", restored.PendingUserMessageID)
	}

	// The next completed turn's snapshot clears the pending marker: the
	// interrupted prompt was answered, so a later restart must replay any
	// re-delivered message verbatim again.
	persistTranscriptSnapshot(context.Background(), sc, sampleTranscriptItems(), 3, 17, 18)
	if restored := loadTranscriptSnapshot(context.Background(), sc, 3); restored == nil || restored.PendingUserMessageID != 0 {
		t.Fatalf("turn-end persist should clear the pending marker, got %+v", restored)
	}
}

// Without a partial result (older SDK, or cancellation before any turn
// completed) the flush must leave the previous turn's stored snapshot
// untouched — persisting nothing is degradation, clearing would be data loss.
func TestFlushPodTerminationStateWithoutPartialKeepsStoredSnapshot(t *testing.T) {
	sc, fake := newFlushTestClient(t)

	// Seed a snapshot from the last completed turn.
	persistTranscriptSnapshot(context.Background(), sc, sampleTranscriptItems(), 3, 17, 18)
	if fake.upserts != 1 {
		t.Fatalf("seed upserts = %d, want 1", fake.upserts)
	}

	flushPodTerminationState(sc, nil, 3, 17, 18, 9)
	// Interrupted results (unresolved tool approval) reset to the durable
	// tail as well: their history has an unpaired tool_use.
	flushPodTerminationState(sc, &agent.RunResult{
		FinalHistory: sampleTranscriptItems(),
		Interruption: &agent.Interruption{ToolName: "Bash"},
	}, 3, 17, 18, 9)

	if fake.upserts != 1 {
		t.Errorf("upserts = %d, want 1 (no new snapshot without a usable partial)", fake.upserts)
	}
	if fake.deletes != 0 {
		t.Errorf("deletes = %d, want 0 (stored snapshot must survive)", fake.deletes)
	}
	if len(fake.activityEvents) != 2 {
		t.Errorf("activity events = %v, want two pod_terminating entries", fake.activityEvents)
	}
	restored := loadTranscriptSnapshot(context.Background(), sc, 3)
	if restored == nil {
		t.Fatal("stored snapshot from the last completed turn must remain restorable")
	}
	// The kept snapshot predates the interrupted prompt: it must NOT carry a
	// pending marker, so the re-delivered message replays verbatim (its
	// content is not inside this older transcript).
	if restored.PendingUserMessageID != 0 {
		t.Errorf("kept snapshot pending id = %d, want 0", restored.PendingUserMessageID)
	}
}

// An interrupted partial that compresses above the size cap must keep the
// previously stored snapshot: that row is the last completed turn's valid
// resume state (no assistant reply was durably appended since), so clearing
// it would drop the restarted pod to the lossy durable tail instead of
// degrading to the previous full-context snapshot.
func TestFlushPodTerminationStateOversizedPartialKeepsStoredSnapshot(t *testing.T) {
	sc, fake := newFlushTestClient(t)

	// Seed a snapshot from the last completed turn under the default cap.
	persistTranscriptSnapshot(context.Background(), sc, sampleTranscriptItems(), 3, 17, 18)
	if fake.upserts != 1 {
		t.Fatalf("seed upserts = %d, want 1", fake.upserts)
	}

	// Any non-trivial partial breaches a 64-byte cap once gzipped.
	t.Setenv("TRANSCRIPT_SNAPSHOT_MAX_BYTES", "64")
	flushPodTerminationState(sc, &agent.RunResult{
		NewItems:     sampleTranscriptItems(),
		FinalHistory: sampleTranscriptItems(),
	}, 3, 17, 18, 9)

	if fake.upserts != 1 {
		t.Errorf("upserts = %d, want 1 (oversized partial must not be stored)", fake.upserts)
	}
	if fake.deletes != 0 {
		t.Errorf("deletes = %d, want 0 (prior snapshot must survive an oversized partial)", fake.deletes)
	}
	if restored := loadTranscriptSnapshot(context.Background(), sc, 3); restored == nil {
		t.Error("stored snapshot from the last completed turn must remain restorable")
	}
}
