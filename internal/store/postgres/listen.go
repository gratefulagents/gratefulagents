package postgres

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// sessionChangeChannel is the Postgres NOTIFY channel emitted by the
// migration-056 triggers whenever a session row (or one of its child tables)
// changes. Payload: the session UUID.
const sessionChangeChannel = "session_change"

// sessionChangeBroker fans a single LISTEN connection out to in-process
// subscribers. Notifications are wake-up hints only: they carry no data and
// are lossy across reconnects, so subscribers must keep polling (at a relaxed
// interval) and treat the channel purely as a latency optimization.
type sessionChangeBroker struct {
	mu      sync.Mutex
	subs    map[uuid.UUID]map[int64]chan struct{}
	nextID  int64
	healthy atomic.Bool
}

func (b *sessionChangeBroker) subscribe(sessionID uuid.UUID) (<-chan struct{}, func()) {
	// Buffer one wake-up: a notification arriving while the watcher is busy
	// building a frame must not be lost, and more than one pending wake-up
	// is indistinguishable from one.
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs == nil {
		b.subs = make(map[uuid.UUID]map[int64]chan struct{})
	}
	b.nextID++
	id := b.nextID
	if b.subs[sessionID] == nil {
		b.subs[sessionID] = make(map[int64]chan struct{})
	}
	b.subs[sessionID][id] = ch
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs[sessionID], id)
		if len(b.subs[sessionID]) == 0 {
			delete(b.subs, sessionID)
		}
	}
}

func (b *sessionChangeBroker) notify(sessionID uuid.UUID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs[sessionID] {
		select {
		case ch <- struct{}{}:
		default: // a wake-up is already pending
		}
	}
}

// SubscribeSessionChanges registers a wake-up hint channel for one session.
// Implements store.SessionChangeSubscriber. The channel receives (at least)
// one value after any committed change to the session; call cancel when the
// watcher ends. Wake-ups are delivered only while the listener is healthy —
// combine with SessionChangeListenerHealthy to pick the poll interval.
func (s *Store) SubscribeSessionChanges(sessionID uuid.UUID) (<-chan struct{}, func()) {
	return s.changes.subscribe(sessionID)
}

// SessionChangeListenerHealthy reports whether the LISTEN connection is
// currently established, i.e. whether wake-up hints are being delivered.
func (s *Store) SessionChangeListenerHealthy() bool {
	return s.changes.healthy.Load()
}

// StartSessionChangeListener starts the background LISTEN loop that feeds
// SubscribeSessionChanges. It is optional: without it, subscribers simply
// never receive wake-ups and watchers fall back to their fast poll interval.
// The loop reconnects with backoff until ctx is cancelled.
func (s *Store) StartSessionChangeListener(ctx context.Context) {
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			err := s.runSessionChangeListener(ctx)
			if ctx.Err() != nil {
				return
			}
			log.Printf("WARN: session change listener disconnected (retrying in %s): %v", backoff, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}()
}

func (s *Store) runSessionChangeListener(ctx context.Context) error {
	poolConn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// Hijack the connection: a connection with LISTEN state must never
	// return to the pool, where its notifications would leak into unrelated
	// queries.
	conn := poolConn.Hijack()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	if _, err := conn.Exec(ctx, "LISTEN "+sessionChangeChannel); err != nil {
		return err
	}
	s.changes.healthy.Store(true)
	defer s.changes.healthy.Store(false)

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if notification.Channel != sessionChangeChannel {
			continue
		}
		sessionID, err := uuid.Parse(notification.Payload)
		if err != nil {
			continue
		}
		s.changes.notify(sessionID)
	}
}
