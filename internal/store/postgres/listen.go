package postgres

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// sessionChangeChannel is the Postgres NOTIFY channel emitted by the
// migration-056 triggers whenever a session row (or one of its child tables)
// changes. Payload: the session UUID.
const sessionChangeChannel = "session_change"

// sessionChangeChannelPrefix prefixes the per-session channels emitted by the
// migration-059 notify trigger alongside the global channel.
const sessionChangeChannelPrefix = "session_change_"

const (
	// listenLivenessInterval bounds each WaitForNotification so a silently
	// dead TCP connection (NAT timeout, killed proxy) is detected by a Ping
	// instead of hanging the listener forever with healthy=true.
	listenLivenessInterval = 45 * time.Second
	listenPingTimeout      = 10 * time.Second
	// listenBackoffResetAfter: a connection that stayed up this long counts
	// as a recovery, so the next disconnect retries quickly again instead of
	// inheriting the escalated backoff from an earlier outage.
	listenBackoffResetAfter = 30 * time.Second
	listenMaxBackoff        = 30 * time.Second
)

// sessionChangeChannelFor returns the per-session NOTIFY channel name:
// "session_change_" + the UUID as 32 hex characters without dashes (47 chars,
// within Postgres' 63-char identifier limit).
func sessionChangeChannelFor(sessionID uuid.UUID) string {
	return sessionChangeChannelPrefix + strings.ReplaceAll(sessionID.String(), "-", "")
}

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
// SubscribeSessionChanges for every session (the global session_change
// channel). It is optional: without it, subscribers simply never receive
// wake-ups and watchers fall back to their fast poll interval. The loop
// reconnects with backoff until ctx is cancelled.
func (s *Store) StartSessionChangeListener(ctx context.Context) {
	s.startListener(ctx, []string{sessionChangeChannel}, globalSessionChangeRouter)
}

// StartSessionChangeListenerFor is the agent-pod variant: it LISTENs only on
// the per-session channel for sessionID, so the pod is not woken by every
// other session in the fleet. Every notification on that channel is routed to
// subscribers of sessionID regardless of payload.
func (s *Store) StartSessionChangeListenerFor(ctx context.Context, sessionID uuid.UUID) {
	s.startListener(ctx, []string{sessionChangeChannelFor(sessionID)}, perSessionChangeRouter(sessionID))
}

// notificationRouter maps a raw notification to the session whose
// subscribers should be woken; ok=false drops it.
type notificationRouter func(channel, payload string) (id uuid.UUID, ok bool)

func globalSessionChangeRouter(channel, payload string) (uuid.UUID, bool) {
	if channel != sessionChangeChannel {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(payload)
	return id, err == nil
}

func perSessionChangeRouter(sessionID uuid.UUID) notificationRouter {
	channel := sessionChangeChannelFor(sessionID)
	return func(ch, _ string) (uuid.UUID, bool) {
		return sessionID, ch == channel
	}
}

func (s *Store) startListener(ctx context.Context, channels []string, route notificationRouter) {
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			listeningSince, err := s.runSessionChangeListener(ctx, channels, route)
			if ctx.Err() != nil {
				return
			}
			// Reset only after a connection that was actually LISTENing for a
			// while: a slow Acquire followed by a LISTEN failure must keep
			// backing off instead of hot-looping against an exhausted pool.
			if !listeningSince.IsZero() && time.Since(listeningSince) >= listenBackoffResetAfter {
				backoff = time.Second
			}
			log.Printf("WARN: session change listener disconnected (retrying in %s): %v", backoff, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < listenMaxBackoff {
				backoff *= 2
			}
		}
	}()
}

// runSessionChangeListener returns when the connection fails or ctx ends,
// together with the time LISTEN became active (zero when it never did).
func (s *Store) runSessionChangeListener(ctx context.Context, channels []string, route notificationRouter) (time.Time, error) {
	poolConn, err := s.pool.Acquire(ctx)
	if err != nil {
		return time.Time{}, err
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

	for _, channel := range channels {
		if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
			return time.Time{}, err
		}
	}
	listeningSince := time.Now()
	s.changes.healthy.Store(true)
	defer s.changes.healthy.Store(false)

	for {
		waitCtx, cancelWait := context.WithTimeout(ctx, listenLivenessInterval)
		notification, err := conn.WaitForNotification(waitCtx)
		cancelWait()
		if err != nil {
			if ctx.Err() != nil {
				return listeningSince, ctx.Err()
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				return listeningSince, err
			}
			// Quiet period: verify the connection is still alive. A dead
			// socket surfaces here instead of hanging indefinitely.
			pingCtx, cancelPing := context.WithTimeout(ctx, listenPingTimeout)
			err = conn.Ping(pingCtx)
			cancelPing()
			if err != nil {
				return listeningSince, err
			}
			continue
		}
		sessionID, ok := route(notification.Channel, notification.Payload)
		if !ok {
			continue
		}
		s.changes.notify(sessionID)
	}
}
