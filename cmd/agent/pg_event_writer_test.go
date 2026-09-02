package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

type recordingStateStore struct {
	mu                 sync.Mutex
	writes             []writeCall
	writeStarted       chan struct{}
	writeRelease       chan struct{}
	blockUntilCanceled bool
}

type writeCall struct {
	eventType string
	summary   string
	detail    json.RawMessage
}

func (m *recordingStateStore) CreateSession(context.Context, string, string, string, string) (*store.Session, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetSession(context.Context, uuid.UUID) (*store.Session, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetSessionByRun(context.Context, string, string) (*store.Session, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) ListSessionsByNamespace(context.Context, string) ([]store.Session, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) UpdatePhase(context.Context, uuid.UUID, string, string) error {
	panic("unexpected call")
}
func (m *recordingStateStore) SetPendingQuestion(context.Context, uuid.UUID, string, string, string) error {
	panic("unexpected call")
}
func (m *recordingStateStore) ClearPendingQuestion(context.Context, uuid.UUID, string) error {
	panic("unexpected call")
}
func (m *recordingStateStore) SetPendingAction(context.Context, uuid.UUID, string, string, json.RawMessage, string) error {
	panic("unexpected call")
}
func (m *recordingStateStore) ClearPendingAction(context.Context, uuid.UUID, string) error {
	panic("unexpected call")
}
func (m *recordingStateStore) UpdateMetadata(context.Context, uuid.UUID, json.RawMessage) error {
	panic("unexpected call")
}
func (m *recordingStateStore) MergeSessionMetadata(context.Context, uuid.UUID, string, json.RawMessage) error {
	panic("unexpected call")
}
func (m *recordingStateStore) ListAllSessionMetrics(context.Context) ([]store.SessionMetricsEntry, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) DeleteAgentRunData(context.Context, string, string, string) error {
	panic("unexpected call")
}
func (m *recordingStateStore) AppendMessage(context.Context, uuid.UUID, string, string, json.RawMessage) (*store.Message, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetMessages(context.Context, uuid.UUID) ([]store.Message, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetMessagesIncludingCancelled(context.Context, uuid.UUID) ([]store.Message, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetMessagesSince(context.Context, uuid.UUID, int64) ([]store.Message, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) PollNewUserMessages(context.Context, uuid.UUID) ([]store.Message, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) MarkMessagesDelivered(context.Context, uuid.UUID, []int64) error {
	panic("unexpected call")
}

func (m *recordingStateStore) CancelUndeliveredUserMessage(context.Context, uuid.UUID, int64) error {
	panic("unexpected call")
}
func (m *recordingStateStore) UpsertSessionTranscript(context.Context, uuid.UUID, []byte, int32) error {
	panic("unexpected call")
}
func (m *recordingStateStore) GetSessionTranscript(context.Context, uuid.UUID) ([]byte, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) DeleteSessionTranscript(context.Context, uuid.UUID) error {
	panic("unexpected call")
}
func (m *recordingStateStore) WriteActivityEvent(ctx context.Context, _ uuid.UUID, eventType, summary string, detail json.RawMessage) (*store.ActivityEvent, error) {
	if m.writeStarted != nil {
		select {
		case m.writeStarted <- struct{}{}:
		default:
		}
	}
	if m.blockUntilCanceled {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if m.writeRelease != nil {
		<-m.writeRelease
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, writeCall{eventType: eventType, summary: summary, detail: append(json.RawMessage(nil), detail...)})
	return &store.ActivityEvent{EventType: eventType, Summary: summary, Detail: detail}, nil
}
func (m *recordingStateStore) GetRecentActivity(context.Context, uuid.UUID, int32) ([]store.ActivityEvent, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetAllActivity(context.Context, uuid.UUID) ([]store.ActivityEvent, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetActivityEventsSince(context.Context, uuid.UUID, int64) ([]store.ActivityEvent, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetSessionFingerprint(context.Context, uuid.UUID) (string, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetSessionConversationFingerprint(context.Context, uuid.UUID) (string, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) UpsertArtifact(context.Context, uuid.UUID, string, string, string, string, json.RawMessage) (*store.Artifact, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetArtifact(context.Context, uuid.UUID, string) (*store.Artifact, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetArtifacts(context.Context, uuid.UUID) ([]store.Artifact, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) SetResourceOwner(context.Context, string, string, string, string) error {
	panic("unexpected call")
}
func (m *recordingStateStore) GetResourceOwner(context.Context, string, string, string) (*store.ResourceOwnership, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) ListOwnedResources(context.Context, string, string) ([]store.ResourceOwnership, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) ShareResource(context.Context, *store.ResourceShare) (*store.ResourceShare, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) RevokeShare(context.Context, string) error { panic("unexpected call") }
func (m *recordingStateStore) UpdateSharePermission(context.Context, string, string) error {
	panic("unexpected call")
}
func (m *recordingStateStore) ListSharesForResource(context.Context, string, string, string) ([]store.ResourceShare, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) ListSharedWithMe(context.Context, string, string) ([]store.ResourceShare, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) GetSharePermission(context.Context, string, string, string, string) (*store.ResourceShare, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) CreateNotification(context.Context, *store.Notification) error {
	panic("unexpected call")
}
func (m *recordingStateStore) HasUnreadNotification(context.Context, string, string, string, string) (bool, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) ListNotifications(context.Context, string, bool, int32) ([]store.Notification, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) MarkNotificationRead(context.Context, string) error {
	panic("unexpected call")
}
func (m *recordingStateStore) MarkAllNotificationsRead(context.Context, string) error {
	panic("unexpected call")
}
func (m *recordingStateStore) GetUnreadNotificationCount(context.Context, string) (int32, error) {
	panic("unexpected call")
}
func (m *recordingStateStore) Close() error { return nil }

func TestPGEventWriterCloseCancelsBlockedStoreWithinDeadline(t *testing.T) {
	oldTimeout := pgEventWriterCloseTimeout
	pgEventWriterCloseTimeout = 25 * time.Millisecond
	t.Cleanup(func() { pgEventWriterCloseTimeout = oldTimeout })

	ss := &recordingStateStore{
		writeStarted:       make(chan struct{}, 1),
		blockUntilCanceled: true,
	}
	writer := newPGEventWriter(ss, uuid.New())
	if _, err := writer.Write([]byte(`{"type":"assistant_text","message":"first"}`)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := writer.Write([]byte(`{"type":"assistant_text","message":"buffered"}`)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	select {
	case <-ss.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for store write")
	}

	started := time.Now()
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("Close() took %v, want bounded by 250ms", elapsed)
	}
	if writer.unflushed != 2 {
		t.Fatalf("unflushed = %d, want 2", writer.unflushed)
	}
}

func TestPGEventWriterDoesNotBlockCallerWhenStoreIsSlow(t *testing.T) {
	ss := &recordingStateStore{
		writeStarted: make(chan struct{}, 1),
		writeRelease: make(chan struct{}),
	}
	writer := newPGEventWriter(ss, uuid.New())

	if _, err := writer.Write([]byte(`{"type":"assistant_text","message":"first"}`)); err != nil {
		t.Fatalf("Write(first) error = %v", err)
	}
	select {
	case <-ss.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first store write to block")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < pgEventWriterBuffer+25; i++ {
			if _, err := writer.Write([]byte(`{"type":"assistant_text","message":"event"}`)); err != nil {
				t.Errorf("Write() error = %v", err)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Write() blocked while store drain was stalled")
	}

	for i := 0; i < pgEventWriterBuffer+26; i++ {
		ss.writeRelease <- struct{}{}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()
	if got := len(ss.writes); got != pgEventWriterBuffer+26 {
		t.Fatalf("written events = %d, want %d", got, pgEventWriterBuffer+26)
	}
}

func TestPGEventWriterPreservesOrderingUnderBackpressure(t *testing.T) {
	ss := &recordingStateStore{
		writeStarted: make(chan struct{}, 1),
		writeRelease: make(chan struct{}),
	}
	writer := newPGEventWriter(ss, uuid.New())

	total := pgEventWriterBuffer + 25
	for i := 0; i < total; i++ {
		if _, err := writer.Write([]byte(fmt.Sprintf(`{"type":"assistant_text","message":"event-%04d"}`, i))); err != nil {
			t.Fatalf("Write(%d) error = %v", i, err)
		}
		if i == 0 {
			select {
			case <-ss.writeStarted:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for first store write to block")
			}
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = writer.Close()
	}()
	for i := 0; i < total; i++ {
		ss.writeRelease <- struct{}{}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() timed out")
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()
	if got := len(ss.writes); got != total {
		t.Fatalf("written events = %d, want %d", got, total)
	}
	for i, write := range ss.writes {
		want := fmt.Sprintf("event-%04d", i)
		if write.summary != want {
			t.Fatalf("write[%d].summary = %q, want %q", i, write.summary, want)
		}
	}
}

func TestPGEventWriterCloseDuringBackpressureFlushesWithoutPanic(t *testing.T) {
	ss := &recordingStateStore{
		writeStarted: make(chan struct{}, 1),
		writeRelease: make(chan struct{}),
	}
	writer := newPGEventWriter(ss, uuid.New())
	const total = 64
	for i := 0; i < total; i++ {
		if _, err := writer.Write([]byte(fmt.Sprintf(`{"type":"assistant_text","message":"event-%d"}`, i))); err != nil {
			t.Fatalf("Write(%d) error = %v", i, err)
		}
		if i == 0 {
			<-ss.writeStarted
		}
	}
	done := make(chan error, 1)
	go func() { done <- writer.Close() }()
	for i := 0; i < total; i++ {
		ss.writeRelease <- struct{}{}
	}
	if err := <-done; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if got := len(ss.writes); got != total {
		t.Fatalf("written events = %d, want %d", got, total)
	}
}

func TestPGEventWriterConcurrentWriteCloseRace(t *testing.T) {
	ss := &recordingStateStore{}
	writer := newPGEventWriter(ss, uuid.New())

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_, _ = writer.Write([]byte(fmt.Sprintf(`{"type":"assistant_text","message":"%d-%d"}`, id, i)))
			}
		}(g)
	}
	time.Sleep(time.Millisecond)
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	wg.Wait()
}

func TestPGEventWriterUsesToolNameWhenMessageIsEmpty(t *testing.T) {
	ss := &recordingStateStore{}
	writer := newPGEventWriter(ss, uuid.New())
	if _, err := writer.Write([]byte(`{"type":"tool_use","tool":"grep"}`)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()
	if len(ss.writes) != 1 {
		t.Fatalf("writes len = %d, want 1", len(ss.writes))
	}
	if got := ss.writes[0].summary; got != "grep" {
		t.Fatalf("summary = %q, want grep", got)
	}
}

// batchingStateStore adds the optional batch-write capability. Batches are
// recorded whole so tests can assert both batching and per-event ordering.
type batchingStateStore struct {
	recordingStateStore
	batchStarted chan struct{}
	batchRelease chan struct{}
	batches      [][]store.ActivityEventInput
}

func (m *batchingStateStore) WriteActivityEvents(_ context.Context, _ uuid.UUID, events []store.ActivityEventInput) ([]int64, error) {
	if m.batchStarted != nil {
		select {
		case m.batchStarted <- struct{}{}:
		default:
		}
	}
	if m.batchRelease != nil {
		<-m.batchRelease
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	batch := append([]store.ActivityEventInput(nil), events...)
	m.batches = append(m.batches, batch)
	ids := make([]int64, len(events))
	for i, ev := range events {
		m.writes = append(m.writes, writeCall{eventType: ev.EventType, summary: ev.Summary, detail: append(json.RawMessage(nil), ev.Detail...)})
		ids[i] = int64(len(m.writes))
	}
	return ids, nil
}

func TestPGEventWriterBatchesBufferedEventsInOrder(t *testing.T) {
	ss := &batchingStateStore{
		batchStarted: make(chan struct{}, 1),
		batchRelease: make(chan struct{}),
	}
	writer := newPGEventWriter(ss, uuid.New())

	// The first event is picked up alone (the drainer is idle); everything
	// written while its batch is blocked must be flushed in bounded batches.
	total := 3*pgEventWriterBatchSize + 5
	for i := 0; i < total; i++ {
		if _, err := writer.Write([]byte(fmt.Sprintf(`{"type":"tool_use","tool":"event-%04d"}`, i))); err != nil {
			t.Fatalf("Write(%d) error = %v", i, err)
		}
		if i == 0 {
			select {
			case <-ss.batchStarted:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for the first batch write")
			}
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = writer.Close()
	}()
	// One release per batch: 1 (first event) + ceil((total-1)/batchSize).
	for i := 0; i < 5; i++ {
		ss.batchRelease <- struct{}{}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() timed out")
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()
	if got := len(ss.writes); got != total {
		t.Fatalf("written events = %d, want %d", got, total)
	}
	for i, write := range ss.writes {
		if want := fmt.Sprintf("event-%04d", i); write.summary != want {
			t.Fatalf("write[%d].summary = %q, want %q", i, write.summary, want)
		}
		if write.eventType != "tool_use" {
			t.Fatalf("write[%d].eventType = %q, want tool_use", i, write.eventType)
		}
	}
	if got := len(ss.batches); got != 5 {
		t.Fatalf("batches = %d, want 5", got)
	}
	for i, batch := range ss.batches {
		if len(batch) > pgEventWriterBatchSize {
			t.Fatalf("batch[%d] size = %d, exceeds %d", i, len(batch), pgEventWriterBatchSize)
		}
	}
}

func TestPGEventWriterCountsWholeBatchAsUnflushedOnExpiry(t *testing.T) {
	oldTimeout := pgEventWriterCloseTimeout
	pgEventWriterCloseTimeout = 25 * time.Millisecond
	t.Cleanup(func() { pgEventWriterCloseTimeout = oldTimeout })

	ss := &batchingStateStore{
		batchStarted: make(chan struct{}, 1),
		batchRelease: make(chan struct{}),
	}
	writer := newPGEventWriter(ss, uuid.New())
	if _, err := writer.Write([]byte(`{"type":"assistant_text","message":"first"}`)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	<-ss.batchStarted
	for i := 0; i < 3; i++ {
		if _, err := writer.Write([]byte(`{"type":"assistant_text","message":"buffered"}`)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	close(ss.batchRelease)
	if writer.unflushed != 4 {
		t.Fatalf("unflushed = %d, want 4 (1 in-flight batch + 3 buffered)", writer.unflushed)
	}
}

func TestPGEventWriterByteBudgetDropsOldest(t *testing.T) {
	ss := &recordingStateStore{
		writeStarted: make(chan struct{}, 1),
		writeRelease: make(chan struct{}),
	}
	writer := newPGEventWriter(ss, uuid.New())

	if _, err := writer.Write([]byte(`{"type":"assistant_text","message":"blocking"}`)); err != nil {
		t.Fatalf("Write(blocking) error = %v", err)
	}
	<-ss.writeStarted

	// Four payloads fit the budget (just under 1/4 each, envelope included);
	// the fifth must evict the oldest buffered event while the count cap is
	// nowhere near.
	payload := make([]byte, pgEventWriterMaxBytes/4-256)
	for i := range payload {
		payload[i] = 'x'
	}
	for i := 0; i < 5; i++ {
		raw := []byte(fmt.Sprintf(`{"type":"assistant_text","message":"big-%d","status":"%s"}`, i, payload))
		if _, err := writer.Write(raw); err != nil {
			t.Fatalf("Write(big-%d) error = %v", i, err)
		}
	}

	writer.mu.Lock()
	buffered, bufBytes, dropped := writer.bufferedLocked(), writer.bufBytes, writer.dropped
	writer.mu.Unlock()
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if buffered != 4 {
		t.Fatalf("buffered = %d, want 4", buffered)
	}
	if bufBytes > pgEventWriterMaxBytes {
		t.Fatalf("bufBytes = %d, exceeds budget %d", bufBytes, pgEventWriterMaxBytes)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = writer.Close()
	}()
	for i := 0; i < 5; i++ {
		ss.writeRelease <- struct{}{}
	}
	<-done

	ss.mu.Lock()
	defer ss.mu.Unlock()
	if got := len(ss.writes); got != 5 {
		t.Fatalf("written events = %d, want 5", got)
	}
	for i, want := range []string{"blocking", "big-1", "big-2", "big-3", "big-4"} {
		if ss.writes[i].summary != want {
			t.Fatalf("write[%d].summary = %q, want %q", i, ss.writes[i].summary, want)
		}
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.bufBytes != 0 {
		t.Fatalf("bufBytes after drain = %d, want 0", writer.bufBytes)
	}
}
