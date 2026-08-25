package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/store"
	pgstore "github.com/gratefulagents/gratefulagents/internal/store/postgres"
)

// setupPGStore is like setupTestStore but returns the concrete store and pool
// for tests exercising Postgres-only extensions (change_seq, LISTEN/NOTIFY,
// retention sweeps).
func setupPGStore(t *testing.T) (*pgstore.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to test db: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("running migrations: %v", err)
	}
	for _, table := range []string{"durable_runs", "agent_run_wake_intents", "session_interrupts", "agent_artifacts", "activity_events", "conversation_messages", "agent_sessions"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("cleaning table %s: %v", table, err)
		}
	}
	return pgstore.NewFromPool(pool), pool
}

func fingerprint(t *testing.T, s *pgstore.Store, id uuid.UUID) string {
	t.Helper()
	fp, err := s.GetSessionFingerprint(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSessionFingerprint: %v", err)
	}
	return fp
}

// The fingerprint must change on every watch-visible write — including the
// in-place metadata flips (MarkMessagesDelivered, cancellation) that never
// append a row, which is exactly why change_seq is a counter and not a tail
// aggregate.
func TestSessionFingerprintChangesOnEveryWrite(t *testing.T) {
	s, _ := setupPGStore(t)
	ctx := context.Background()

	sess, err := s.CreateSession(ctx, "fp-run", "default", "running", "work")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	last := fingerprint(t, s, sess.ID)
	step := func(name string, write func() error) {
		t.Helper()
		if err := write(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		next := fingerprint(t, s, sess.ID)
		if next == last {
			t.Fatalf("%s did not change the session fingerprint (still %q)", name, next)
		}
		last = next
	}

	var msg *store.Message
	step("AppendMessage", func() error {
		var err error
		msg, err = s.AppendMessage(ctx, sess.ID, "user", "hello", json.RawMessage(`{"mode":"enqueue"}`))
		return err
	})
	step("MarkMessagesDelivered", func() error {
		return s.MarkMessagesDelivered(ctx, sess.ID, []int64{msg.ID})
	})
	step("WriteActivityEvent", func() error {
		_, err := s.WriteActivityEvent(ctx, sess.ID, "tool_call", "ran a tool", json.RawMessage(`{}`))
		return err
	})
	step("UpsertArtifact", func() error {
		_, err := s.UpsertArtifact(ctx, sess.ID, "plan", "the plan", "", "", nil)
		return err
	})
	step("UpdatePhase", func() error {
		return s.UpdatePhase(ctx, sess.ID, "running", "next-step")
	})

	var cancelTarget *store.Message
	step("AppendMessage(second)", func() error {
		var err error
		cancelTarget, err = s.AppendMessage(ctx, sess.ID, "user", "queued", json.RawMessage(`{"mode":"enqueue"}`))
		return err
	})
	step("CancelUndeliveredUserMessage", func() error {
		return s.CancelUndeliveredUserMessage(ctx, sess.ID, cancelTarget.ID)
	})

	versions, err := s.GetAgentRunSummaryVersions(ctx, "default")
	if err != nil {
		t.Fatalf("GetAgentRunSummaryVersions: %v", err)
	}
	if versions["default/fp-run"] == "" {
		t.Fatal("summary versions missing the session")
	}
}

// Concurrent read-modify-write updates of the same metadata section must
// serialize under the session row lock: no update may be lost.
func TestUpdateSessionMetadataSectionSerializes(t *testing.T) {
	s, _ := setupPGStore(t)
	ctx := context.Background()

	sess, err := s.CreateSession(ctx, "meta-run", "default", "running", "work")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const writers = 8
	const perWriter = 5
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				err := s.UpdateSessionMetadataSection(ctx, sess.ID, "counter", func(raw json.RawMessage) (json.RawMessage, error) {
					n := 0
					if len(raw) > 0 {
						if err := json.Unmarshal(raw, &n); err != nil {
							return nil, err
						}
					}
					return json.Marshal(n + 1)
				})
				if err != nil {
					t.Errorf("UpdateSessionMetadataSection: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	var metadata struct {
		Counter int `json:"counter"`
	}
	if err := json.Unmarshal(got.Metadata, &metadata); err != nil {
		t.Fatalf("decoding metadata: %v", err)
	}
	if metadata.Counter != writers*perWriter {
		t.Fatalf("counter = %d, want %d (lost updates)", metadata.Counter, writers*perWriter)
	}
}

// A committed write to a watched session must produce a LISTEN wake-up hint.
func TestSessionChangeListenerDeliversWakeups(t *testing.T) {
	s, _ := setupPGStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, err := s.CreateSession(ctx, "listen-run", "default", "running", "work")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	s.StartSessionChangeListener(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for !s.SessionChangeListenerHealthy() {
		if time.Now().After(deadline) {
			t.Fatal("listener never became healthy")
		}
		time.Sleep(10 * time.Millisecond)
	}

	wake, unsubscribe := s.SubscribeSessionChanges(sess.ID)
	defer unsubscribe()

	if _, err := s.WriteActivityEvent(ctx, sess.ID, "tool_call", "wake up", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("WriteActivityEvent: %v", err)
	}

	select {
	case <-wake:
	case <-time.After(5 * time.Second):
		t.Fatal("no wake-up hint delivered after a committed session write")
	}
}

// backdateSession rewinds a session's updated_at, bypassing the migration-001
// trigger that would otherwise reset it to now() on any UPDATE.
func backdateSession(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin backdate: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("disabling triggers: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_sessions SET updated_at = now() - interval '60 days' WHERE id = $1`, id); err != nil {
		t.Fatalf("backdating session: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit backdate: %v", err)
	}
}

// The retention sweep must delete expired durable runs (cascading events) and
// activity events of old terminal sessions that already have an S3 activity
// artifact — and nothing else.
func TestPurgeExpiredSessionData(t *testing.T) {
	s, pool := setupPGStore(t)
	ctx := context.Background()

	// Durable runs: one expired, one future, one without a deadline.
	now := time.Now().UTC()
	for _, run := range []struct {
		id     string
		retain any
	}{
		{"expired", now.Add(-time.Hour)},
		{"future", now.Add(time.Hour)},
		{"forever", nil},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO durable_runs (tenant_id, run_id, revision, event_sequence, snapshot, retain_until, created_at, updated_at)
			VALUES ('t', $1, 1, 1, '\x00', $2, now(), now())`, run.id, run.retain); err != nil {
			t.Fatalf("inserting durable run %s: %v", run.id, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO durable_events (tenant_id, run_id, sequence, body)
			VALUES ('t', $1, 1, '\x00')`, run.id); err != nil {
			t.Fatalf("inserting durable event %s: %v", run.id, err)
		}
	}

	// Terminal session, old, with an S3 activity artifact: purgeable.
	oldSess, err := s.CreateSession(ctx, "old-run", "default", "running", "work")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.WriteActivityEvent(ctx, oldSess.ID, "tool_call", "old", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("WriteActivityEvent: %v", err)
	}
	if _, err := s.UpsertArtifact(ctx, oldSess.ID, "activity_log", "", "s3://bucket/log.jsonl", "", nil); err != nil {
		t.Fatalf("UpsertArtifact: %v", err)
	}
	if err := s.UpdatePhase(ctx, oldSess.ID, "succeeded", "done"); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	backdateSession(t, pool, oldSess.ID)

	// Active session: must keep its events even though they are old rows.
	liveSess, err := s.CreateSession(ctx, "live-run", "default", "running", "work")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.WriteActivityEvent(ctx, liveSess.ID, "tool_call", "live", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("WriteActivityEvent: %v", err)
	}

	// Terminal but without an S3 artifact: events must be kept.
	noArtifactSess, err := s.CreateSession(ctx, "noart-run", "default", "running", "work")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.WriteActivityEvent(ctx, noArtifactSess.ID, "tool_call", "keep", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("WriteActivityEvent: %v", err)
	}
	if err := s.UpdatePhase(ctx, noArtifactSess.ID, "failed", "done"); err != nil {
		t.Fatalf("UpdatePhase: %v", err)
	}
	backdateSession(t, pool, noArtifactSess.ID)

	counts, more, err := s.PurgeExpiredSessionData(ctx, pgstore.SessionRetentionPolicy{
		PurgeExpiredDurableRuns: true,
		ActivityEventDays:       30,
	}, 100)
	if err != nil {
		t.Fatalf("PurgeExpiredSessionData: %v", err)
	}
	if more {
		t.Fatal("sweep reported more work below the batch limit")
	}
	if counts.DurableRunsDeleted != 1 {
		t.Fatalf("DurableRunsDeleted = %d, want 1", counts.DurableRunsDeleted)
	}
	if counts.ActivityEventsDeleted != 1 {
		t.Fatalf("ActivityEventsDeleted = %d, want 1", counts.ActivityEventsDeleted)
	}

	var durableRuns, durableEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM durable_runs`).Scan(&durableRuns); err != nil {
		t.Fatalf("counting durable runs: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM durable_events`).Scan(&durableEvents); err != nil {
		t.Fatalf("counting durable events: %v", err)
	}
	if durableRuns != 2 || durableEvents != 2 {
		t.Fatalf("durable rows = %d/%d, want 2/2 (expired run + its events gone)", durableRuns, durableEvents)
	}
	for _, keep := range []struct {
		name string
		id   uuid.UUID
		want int
	}{
		{"purgeable", oldSess.ID, 0},
		{"live", liveSess.ID, 1},
		{"no-artifact", noArtifactSess.ID, 1},
	} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_events WHERE session_id = $1`, keep.id).Scan(&n); err != nil {
			t.Fatalf("counting %s events: %v", keep.name, err)
		}
		if n != keep.want {
			t.Fatalf("%s session has %d activity events, want %d", keep.name, n, keep.want)
		}
	}

	// Idempotent: a second sweep finds nothing.
	counts, _, err = s.PurgeExpiredSessionData(ctx, pgstore.SessionRetentionPolicy{
		PurgeExpiredDurableRuns: true,
		ActivityEventDays:       30,
	}, 100)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if counts.DurableRunsDeleted != 0 || counts.ActivityEventsDeleted != 0 {
		t.Fatalf("second sweep deleted %+v, want nothing", counts)
	}
}
