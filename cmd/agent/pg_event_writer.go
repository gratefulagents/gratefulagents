package main

import (
	"context"
	"encoding/json"
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
	buf       []json.RawMessage
	bufBytes  int64
	head      int
	closed    bool
	expired   bool
	inFlight  int
	dropped   int64
	unflushed int64

	dropWarnMu    sync.Mutex
	lastDropWarn  time.Time
	droppedWarned int64

	drainCtx    context.Context
	cancelDrain context.CancelFunc
	drainDone   chan struct{}
	closeDone   chan struct{}
	closeOnce   sync.Once
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

var pgEventWriterCloseTimeout = 5 * time.Second

func newPGEventWriter(ss store.StateStore, sessionID uuid.UUID) *pgEventWriter {
	drainCtx, cancelDrain := context.WithCancel(context.Background())
	w := &pgEventWriter{
		store:       ss,
		sessionID:   sessionID,
		notify:      make(chan struct{}, 1),
		buf:         make([]json.RawMessage, 0, pgEventWriterBuffer),
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
	w.buf = append(w.buf, json.RawMessage(cp))
	w.bufBytes += int64(len(cp))
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
	w.bufBytes -= int64(len(w.buf[w.head]))
	w.buf[w.head] = nil
	w.head++
	w.dropped++
	w.compactLocked()
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
		w.unflushed += int64(w.bufferedLocked()) + int64(w.inFlight)
		for i := w.head; i < len(w.buf); i++ {
			w.buf[i] = nil
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

func (w *pgEventWriter) writeBatch(batchWriter activityEventBatchWriter, batch []json.RawMessage) {
	inputs := make([]store.ActivityEventInput, 0, len(batch))
	for _, raw := range batch {
		eventType, summary := describePGEvent(raw)
		inputs = append(inputs, store.ActivityEventInput{EventType: eventType, Summary: summary, Detail: raw})
	}
	ctx, cancel := context.WithTimeout(w.drainCtx, 5*time.Second)
	_, err := batchWriter.WriteActivityEvents(ctx, w.sessionID, inputs)
	cancel()
	w.mu.Lock()
	w.inFlight = 0
	w.mu.Unlock()
	if err != nil {
		log.Printf("WARN: pgEventWriter: writing %d event(s): %v", len(batch), err)
	}
}

func (w *pgEventWriter) writeOneByOne(batch []json.RawMessage) {
	for _, raw := range batch {
		if w.drainCtx.Err() != nil {
			// Close expired mid-batch: the remaining events were already
			// counted as unflushed; do not spam one WARN per event.
			return
		}
		eventType, summary := describePGEvent(raw)
		ctx, cancel := context.WithTimeout(w.drainCtx, 5*time.Second)
		_, err := w.store.WriteActivityEvent(ctx, w.sessionID, eventType, summary, raw)
		cancel()
		w.mu.Lock()
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
func (w *pgEventWriter) popBatch() ([]json.RawMessage, bool) {
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
			batch := make([]json.RawMessage, n)
			copy(batch, w.buf[w.head:w.head+n])
			for i := 0; i < n; i++ {
				w.bufBytes -= int64(len(w.buf[w.head+i]))
				w.buf[w.head+i] = nil
			}
			w.head += n
			w.inFlight = n
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
