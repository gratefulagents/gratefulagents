package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

type pgEventWriter struct {
	store     store.StateStore
	sessionID uuid.UUID

	mu        sync.Mutex
	notify    chan struct{}
	buf       []pgBufferedEvent
	bufBytes  int64
	head      int
	closed    bool
	expired   bool
	inFlight  int
	dropped   int64
	unflushed int64

	// enqueued counts every event accepted (including synthetic gap
	// markers); settled counts every event whose fate is final (written,
	// failed every retry, dropped under backpressure, or abandoned at the
	// close deadline). Flush waits for settled to reach the enqueued count
	// it observed. settleCh is closed and replaced on every settle.
	enqueued int64
	settled  int64
	settleCh chan struct{}
	// markerDrops is the number of dropped events not yet reported by an
	// events_dropped marker row.
	markerDrops int64

	dropWarnMu    sync.Mutex
	lastDropWarn  time.Time
	droppedWarned int64

	drainCtx    context.Context
	cancelDrain context.CancelFunc
	drainDone   chan struct{}
	closeDone   chan struct{}
	closeOnce   sync.Once
}

// pgBufferedEvent is one buffered stream event plus the idempotency key it is
// written with, fixed at enqueue time so every retry of its batch carries the
// same key.
type pgBufferedEvent struct {
	raw json.RawMessage
	id  uuid.UUID
}

type pgEventEnvelope struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Status  string `json:"status,omitempty"`
}

// activityEventBatchWriter is the optional store capability for writing a
// batch of activity events in one round trip. Declared locally (identical to
// store.ActivityEventBatchWriter) so the writer type-asserts against the
// capability rather than the store package's interface set.
type activityEventBatchWriter interface {
	WriteActivityEvents(ctx context.Context, sessionID uuid.UUID, events []store.ActivityEventInput) ([]int64, error)
}

const (
	pgEventWriterBuffer    = 1024
	pgEventWriterMaxEvents = 64 * 1024
	// pgEventWriterMaxBytes bounds buffered payload: a few large events
	// (base64 screenshots, big tool outputs) must not hold gigabytes of
	// memory just because the count cap is far away.
	pgEventWriterMaxBytes = 64 << 20
	// pgEventWriterBatchSize is the most events one drain pass hands to the
	// store at once.
	pgEventWriterBatchSize = 64
	// pgEventWriterDropWarnInterval rate-limits the backpressure WARN log
	// while drops are still happening (the total is repeated at Close).
	pgEventWriterDropWarnInterval = 30 * time.Second
)

var (
	pgEventWriterCloseTimeout = 5 * time.Second
	// pgEventWriterWriteAttempts and pgEventWriterRetryBackoff bound the
	// retries of one failed store write before its events count as unflushed.
	pgEventWriterWriteAttempts = 5
	pgEventWriterRetryBackoff  = 200 * time.Millisecond
)

func newPGEventWriter(ss store.StateStore, sessionID uuid.UUID) *pgEventWriter {
	drainCtx, cancelDrain := context.WithCancel(context.Background())
	w := &pgEventWriter{
		store:       ss,
		sessionID:   sessionID,
		notify:      make(chan struct{}, 1),
		buf:         make([]pgBufferedEvent, 0, pgEventWriterBuffer),
		settleCh:    make(chan struct{}),
		drainCtx:    drainCtx,
		cancelDrain: cancelDrain,
		drainDone:   make(chan struct{}),
		closeDone:   make(chan struct{}),
	}
	go w.drain()
	return w
}

func (w *pgEventWriter) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if w.bufferedLocked() >= pgEventWriterMaxEvents {
		w.dropOldestLocked()
	}
	w.buf = append(w.buf, pgBufferedEvent{raw: json.RawMessage(cp), id: uuid.New()})
	w.bufBytes += int64(len(cp))
	w.enqueued++
	// The newest event always stays buffered, even when it alone exceeds
	// the byte budget: dropping it would lose the event that just happened.
	for w.bufBytes > pgEventWriterMaxBytes && w.bufferedLocked() > 1 {
		w.dropOldestLocked()
	}
	dropped := w.dropped
	w.mu.Unlock()

	if dropped > 0 {
		w.warnDropped(dropped)
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (w *pgEventWriter) dropOldestLocked() {
	w.bufBytes -= int64(len(w.buf[w.head].raw))
	w.buf[w.head] = pgBufferedEvent{}
	w.head++
	w.dropped++
	w.markerDrops++
	w.settleLocked(1)
	w.compactLocked()
}

// settleLocked records n events whose fate is final and wakes Flush waiters.
func (w *pgEventWriter) settleLocked(n int64) {
	if n <= 0 {
		return
	}
	w.settled += n
	close(w.settleCh)
	w.settleCh = make(chan struct{})
}

// Flush blocks until every event accepted before the call has been written
// to the store (or has failed, been dropped, or been abandoned at close), or
// ctx is done. Synchronous writers call it before their own insert so a
// notice such as turn_interrupted, or a conversation message, is never
// recorded ahead of stream events that were emitted before it.
func (w *pgEventWriter) Flush(ctx context.Context) {
	if w == nil {
		return
	}
	w.mu.Lock()
	target := w.enqueued
	for w.settled < target && !w.expired {
		ch := w.settleCh
		w.mu.Unlock()
		select {
		case <-ch:
		case <-w.drainDone:
			return
		case <-ctx.Done():
			return
		}
		w.mu.Lock()
	}
	w.mu.Unlock()
}

// warnDropped logs backpressure drops while they happen, at most once per
// pgEventWriterDropWarnInterval, so a long run that is shedding events is
// visible in the logs before it exits.
func (w *pgEventWriter) warnDropped(dropped int64) {
	w.dropWarnMu.Lock()
	defer w.dropWarnMu.Unlock()
	if dropped <= w.droppedWarned || time.Since(w.lastDropWarn) < pgEventWriterDropWarnInterval {
		return
	}
	log.Printf("WARN: pgEventWriter: dropped %d oldest event(s) under backpressure (%d since last report)", dropped, dropped-w.droppedWarned)
	w.droppedWarned = dropped
	w.lastDropWarn = time.Now()
}

func (w *pgEventWriter) Close() error {
	w.closeOnce.Do(func() { go w.close() })
	<-w.closeDone
	return nil
}

func (w *pgEventWriter) close() {
	defer close(w.closeDone)

	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	select {
	case w.notify <- struct{}{}:
	default:
	}

	timer := time.NewTimer(pgEventWriterCloseTimeout)
	defer timer.Stop()
	select {
	case <-w.drainDone:
	case <-timer.C:
		w.mu.Lock()
		w.expired = true
		w.cancelDrain()
		abandoned := int64(w.bufferedLocked()) + int64(w.inFlight)
		w.unflushed += abandoned
		w.settleLocked(abandoned)
		for i := w.head; i < len(w.buf); i++ {
			w.buf[i] = pgBufferedEvent{}
		}
		w.buf = w.buf[:0]
		w.bufBytes = 0
		w.head = 0
		w.mu.Unlock()
	}

	w.mu.Lock()
	dropped, unflushed := w.dropped, w.unflushed
	w.mu.Unlock()
	if dropped > 0 {
		log.Printf("WARN: pgEventWriter: dropped %d oldest event(s) under backpressure", dropped)
	}
	if unflushed > 0 {
		log.Printf("WARN: pgEventWriter: failed to flush %d event(s) before close deadline", unflushed)
	}
}

func (w *pgEventWriter) drain() {
	defer close(w.drainDone)
	batchWriter, batching := w.store.(activityEventBatchWriter)
	for {
		batch, ok := w.popBatch()
		if !ok {
			return
		}
		if batching {
			w.writeBatch(batchWriter, batch)
		} else {
			w.writeOneByOne(batch)
		}
	}
}

func (w *pgEventWriter) writeBatch(batchWriter activityEventBatchWriter, batch []pgBufferedEvent) {
	inputs := make([]store.ActivityEventInput, 0, len(batch))
	for _, ev := range batch {
		eventType, summary := describePGEvent(ev.raw)
		inputs = append(inputs, store.ActivityEventInput{EventType: eventType, Summary: summary, Detail: ev.raw, ClientEventID: ev.id})
	}
	err := w.writeWithRetry(func(ctx context.Context) error {
		_, err := batchWriter.WriteActivityEvents(ctx, w.sessionID, inputs)
		return err
	})
	w.mu.Lock()
	if !w.expired {
		// After expiry close() already settled the in-flight batch.
		w.settleLocked(int64(w.inFlight))
		if err != nil {
			w.unflushed += int64(len(batch))
		}
	}
	w.inFlight = 0
	w.mu.Unlock()
	if err != nil {
		log.Printf("WARN: pgEventWriter: writing %d event(s): %v", len(batch), err)
	}
}

// writeWithRetry retries a failed store write with bounded exponential
// backoff while the drain context is alive, so a transient Postgres error does
// not silently lose the crash-safe copy of the events. The events stay in
// flight (ahead of everything buffered) for the whole retry, preserving order.
func (w *pgEventWriter) writeWithRetry(write func(context.Context) error) error {
	backoff := pgEventWriterRetryBackoff
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(w.drainCtx, 5*time.Second)
		err := write(ctx)
		cancel()
		if err == nil || attempt >= pgEventWriterWriteAttempts || w.drainCtx.Err() != nil {
			return err
		}
		select {
		case <-w.drainCtx.Done():
			return err
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

func (w *pgEventWriter) writeOneByOne(batch []pgBufferedEvent) {
	for _, ev := range batch {
		raw := ev.raw
		if w.drainCtx.Err() != nil {
			// Close expired mid-batch: the remaining events were already
			// counted as unflushed; do not spam one WARN per event.
			return
		}
		eventType, summary := describePGEvent(raw)
		err := w.writeWithRetry(func(ctx context.Context) error {
			_, err := w.store.WriteActivityEvent(ctx, w.sessionID, eventType, summary, raw)
			return err
		})
		w.mu.Lock()
		if !w.expired {
			w.settleLocked(1)
			if err != nil {
				w.unflushed++
			}
		}
		w.inFlight--
		w.mu.Unlock()
		if err != nil {
			log.Printf("WARN: pgEventWriter: %v", err)
		}
	}
}

func describePGEvent(raw json.RawMessage) (eventType, summary string) {
	var env pgEventEnvelope
	_ = json.Unmarshal(raw, &env)
	eventType = env.Type
	if eventType == "" {
		eventType = "unknown"
	}
	summary = env.Message
	if summary == "" && env.Tool != "" {
		summary = env.Tool
	}
	return eventType, summary
}

// popBatch blocks until events are buffered and hands back up to
// pgEventWriterBatchSize of them in arrival order. It returns false once the
// writer is closed and empty, or the close deadline expired.
func (w *pgEventWriter) popBatch() ([]pgBufferedEvent, bool) {
	for {
		w.mu.Lock()
		if w.expired {
			w.mu.Unlock()
			return nil, false
		}
		if n := w.bufferedLocked(); n > 0 {
			if n > pgEventWriterBatchSize {
				n = pgEventWriterBatchSize
			}
			batch := make([]pgBufferedEvent, 0, n+1)
			// Events dropped under backpressure were the oldest buffered,
			// so the gap sits right before the oldest survivor: lead the
			// batch with a marker so the timeline shows the gap instead of
			// silently skipping (e.g. a tool_start without its tool_end).
			if w.markerDrops > 0 {
				batch = append(batch, pgEventsDroppedMarker(w.markerDrops))
				w.markerDrops = 0
				w.enqueued++
			}
			batch = append(batch, w.buf[w.head:w.head+n]...)
			for i := 0; i < n; i++ {
				w.bufBytes -= int64(len(w.buf[w.head+i].raw))
				w.buf[w.head+i] = pgBufferedEvent{}
			}
			w.head += n
			w.inFlight = len(batch)
			w.compactLocked()
			w.mu.Unlock()
			return batch, true
		}
		if w.closed {
			w.mu.Unlock()
			return nil, false
		}
		w.mu.Unlock()
		<-w.notify
	}
}

func (w *pgEventWriter) bufferedLocked() int {
	return len(w.buf) - w.head
}

func (w *pgEventWriter) compactLocked() {
	if w.head == 0 {
		return
	}
	if w.head == len(w.buf) {
		w.buf = w.buf[:0]
		w.head = 0
		return
	}
	if w.head > pgEventWriterBuffer && w.head*2 >= len(w.buf) {
		copy(w.buf, w.buf[w.head:])
		w.buf = w.buf[:len(w.buf)-w.head]
		w.head = 0
	}
}

// pgEventsDroppedMarker builds the synthetic activity row that marks a
// backpressure gap in the Postgres copy of the event stream.
func pgEventsDroppedMarker(dropped int64) pgBufferedEvent {
	raw, _ := json.Marshal(map[string]any{
		"ts":      time.Now().UTC(),
		"type":    "events_dropped",
		"message": fmt.Sprintf("%d activity event(s) were dropped while the database was unavailable", dropped),
	})
	return pgBufferedEvent{raw: raw, id: uuid.New()}
}
