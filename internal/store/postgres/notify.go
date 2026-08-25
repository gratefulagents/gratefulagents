package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// sessionEventChannel is the per-session LISTEN/NOTIFY channel that wakes the
// agent loop the moment a new user message or interrupt becomes visible,
// replacing multi-second poll latency. Postgres channel identifiers allow up
// to 63 bytes; this prefix plus a UUID fits.
func sessionEventChannel(sessionID uuid.UUID) string {
	return "agent_session_events_" + sessionID.String()
}

// pgExecutor is satisfied by both *pgxpool.Pool and pgx.Tx so notifications
// can be emitted inside the transaction that makes the event durable — NOTIFY
// fires only on commit, so listeners never observe a wakeup before the row.
type pgExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// notifySessionEvent is a best-effort wakeup. Polling remains the correctness
// backstop, so notification failures are deliberately swallowed.
func notifySessionEvent(ctx context.Context, exec pgExecutor, sessionID uuid.UUID, kind string) {
	_, _ = exec.Exec(ctx, `SELECT pg_notify($1, $2)`, sessionEventChannel(sessionID), kind)
}

// ListenSession opens a dedicated connection subscribed to the session's
// event channel. The subscription is active when this returns, so a caller
// that listens before querying cannot miss a wakeup.
func (s *Store) ListenSession(ctx context.Context, sessionID uuid.UUID) (store.SessionEventListener, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{sessionEventChannel(sessionID)}.Sanitize()); err != nil {
		conn.Release()
		return nil, err
	}
	return &sessionEventListener{conn: conn}, nil
}

type sessionEventListener struct {
	conn *pgxpool.Conn
}

// Wait blocks until a notification arrives (true), the timeout elapses
// (false, nil), or the connection/context fails (error). A failed listener
// must be Closed and re-established; callers fall back to polling meanwhile.
func (l *sessionEventListener) Wait(ctx context.Context, timeout time.Duration) (bool, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := l.conn.Conn().WaitForNotification(waitCtx); err != nil {
		if waitCtx.Err() != nil && ctx.Err() == nil {
			return false, nil // timeout: no event, connection still healthy
		}
		return false, err
	}
	return true, nil
}

// Close destroys the dedicated connection. Hijacking keeps a connection with
// an active LISTEN (and possibly buffered notifications) out of the pool.
func (l *sessionEventListener) Close() {
	conn := l.conn.Hijack()
	_ = conn.Close(context.Background())
}
