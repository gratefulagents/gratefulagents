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
	head      int
	closed    bool
	expired   bool
	inFlight  bool
	dropped   int64
	unflushed int64

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

const (
	pgEventWriterBuffer    = 1024
	pgEventWriterMaxEvents = 64 * 1024
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
		w.buf[w.head] = nil
		w.head++
		w.dropped++
		w.compactLocked()
	}
	w.buf = append(w.buf, json.RawMessage(cp))
	w.mu.Unlock()

	select {
	case w.notify <- struct{}{}:
	default:
	}
	return len(p), nil
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
		w.unflushed += int64(w.bufferedLocked())
		if w.inFlight {
			w.unflushed++
		}
		for i := w.head; i < len(w.buf); i++ {
			w.buf[i] = nil
		}
		w.buf = w.buf[:0]
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
	for {
		raw, ok := w.pop()
		if !ok {
			return
		}
		var env pgEventEnvelope
		_ = json.Unmarshal(raw, &env)

		eventType := env.Type
		if eventType == "" {
			eventType = "unknown"
		}
		summary := env.Message
		if summary == "" && env.Tool != "" {
			summary = env.Tool
		}

		ctx, cancel := context.WithTimeout(w.drainCtx, 5*time.Second)
		_, err := w.store.WriteActivityEvent(ctx, w.sessionID, eventType, summary, raw)
		cancel()
		w.mu.Lock()
		w.inFlight = false
		w.mu.Unlock()
		if err != nil {
			log.Printf("WARN: pgEventWriter: %v", err)
		}
	}
}

func (w *pgEventWriter) pop() (json.RawMessage, bool) {
	for {
		w.mu.Lock()
		if w.expired {
			w.mu.Unlock()
			return nil, false
		}
		if w.bufferedLocked() > 0 {
			raw := w.buf[w.head]
			w.buf[w.head] = nil
			w.head++
			w.inFlight = true
			w.compactLocked()
			w.mu.Unlock()
			return raw, true
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
