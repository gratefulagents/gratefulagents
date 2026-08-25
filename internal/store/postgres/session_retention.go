package postgres

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

// Session/durable-run retention. See RETENTION.md in this package for the
// full design. Both sweeps are batch-limited, idempotent, and resumable:
// every statement's predicate becomes false for rows it already processed.

// defaultRetentionBatchLimit bounds one purge statement when the caller
// passes batchLimit <= 0.
const defaultRetentionBatchLimit = 500

// SessionRetentionPolicy configures the periodic purge of session-scoped and
// SDK durable-run state.
type SessionRetentionPolicy struct {
	// PurgeExpiredDurableRuns removes durable_runs rows (and their cascaded
	// durable_events) whose explicit retain_until deadline has elapsed.
	// retain_until is opt-in per run, so honoring it is always safe.
	PurgeExpiredDurableRuns bool
	// ActivityEventDays purges activity_events belonging to terminal
	// sessions untouched for at least this many days, and only when the
	// session already has a non-empty S3 activity-log artifact (the terminal
	// read path prefers that artifact, so the Postgres rows are redundant).
	// Zero disables the sweep. Conversation messages are never purged here —
	// they are the user's visible chat history.
	ActivityEventDays int32
}

// SessionRetentionPolicyFromEnv builds the policy from environment variables:
// SESSION_ACTIVITY_RETENTION_DAYS (integer days, default 0 = disabled) and
// DURABLE_RUN_RETENTION (default enabled; set "false"/"0" to disable).
func SessionRetentionPolicyFromEnv() SessionRetentionPolicy {
	policy := SessionRetentionPolicy{PurgeExpiredDurableRuns: true}
	if v := os.Getenv("DURABLE_RUN_RETENTION"); v != "" {
		if enabled, err := strconv.ParseBool(v); err == nil {
			policy.PurgeExpiredDurableRuns = enabled
		}
	}
	if v := os.Getenv("SESSION_ACTIVITY_RETENTION_DAYS"); v != "" {
		if days, err := strconv.Atoi(v); err == nil && days > 0 {
			policy.ActivityEventDays = int32(days) //nolint:gosec // validated positive int
		}
	}
	return policy
}

// IsZero reports whether the policy purges nothing.
func (p SessionRetentionPolicy) IsZero() bool {
	return !p.PurgeExpiredDurableRuns && p.ActivityEventDays <= 0
}

// SessionRetentionCounts reports one sweep's work.
type SessionRetentionCounts struct {
	DurableRunsDeleted    int64
	ActivityEventsDeleted int64
}

// PurgeExpiredSessionData applies the retention policy, one bounded batch per
// call. It returns the per-class delete counts and whether another batch is
// likely needed (a class hit its batch limit).
func (s *Store) PurgeExpiredSessionData(ctx context.Context, policy SessionRetentionPolicy, batchLimit int) (SessionRetentionCounts, bool, error) {
	var counts SessionRetentionCounts
	if policy.IsZero() {
		return counts, false, nil
	}
	if batchLimit <= 0 {
		batchLimit = defaultRetentionBatchLimit
	}
	moreWork := false

	if policy.PurgeExpiredDurableRuns {
		// durable_events cascade via the durable_runs foreign key. retain_until
		// is set explicitly by the SDK's durable-run store; rows without it are
		// retained forever.
		tag, err := s.pool.Exec(ctx, `
			DELETE FROM durable_runs
			WHERE (tenant_id, run_id) IN (
				SELECT tenant_id, run_id FROM durable_runs
				WHERE retain_until IS NOT NULL AND retain_until <= now()
				ORDER BY retain_until
				LIMIT $1
			)`, batchLimit)
		if err != nil {
			return counts, false, fmt.Errorf("purging expired durable runs: %w", err)
		}
		counts.DurableRunsDeleted = tag.RowsAffected()
		if counts.DurableRunsDeleted >= int64(batchLimit) {
			moreWork = true
		}
	}

	if policy.ActivityEventDays > 0 {
		// Only terminal sessions whose event log already lives in S3: the
		// dashboard's terminal activity read path prefers the S3 artifact, so
		// deleting the Postgres rows loses nothing. activity_events has no
		// UPDATE/DELETE change_seq trigger, so a purge does not masquerade as
		// live session activity.
		tag, err := s.pool.Exec(ctx, `
			DELETE FROM activity_events
			WHERE id IN (
				SELECT e.id FROM activity_events e
				JOIN agent_sessions s ON s.id = e.session_id
				WHERE s.phase IN ('succeeded', 'failed', 'cancelled')
				  AND s.updated_at < now() - make_interval(days => $1)
				  AND EXISTS (
					SELECT 1 FROM agent_artifacts a
					WHERE a.session_id = s.id AND a.kind = 'activity_log' AND a.s3_url <> ''
				  )
				ORDER BY e.id
				LIMIT $2
			)`, policy.ActivityEventDays, batchLimit)
		if err != nil {
			return counts, false, fmt.Errorf("purging expired activity events: %w", err)
		}
		counts.ActivityEventsDeleted = tag.RowsAffected()
		if counts.ActivityEventsDeleted >= int64(batchLimit) {
			moreWork = true
		}
	}

	return counts, moreWork, nil
}

// StartSessionRetentionWorker runs PurgeExpiredSessionData every interval
// until ctx is cancelled, draining extra batches back-to-back when a sweep
// hits its batch limit. It is safe to run on every replica: sweeps are
// idempotent and batch-limited, so overlapping runs only waste a little work.
func (s *Store) StartSessionRetentionWorker(ctx context.Context, policy SessionRetentionPolicy, interval time.Duration, batchLimit int) {
	if policy.IsZero() {
		return
	}
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			for {
				sweepCtx, cancel := context.WithTimeout(ctx, time.Minute)
				counts, more, err := s.PurgeExpiredSessionData(sweepCtx, policy, batchLimit)
				cancel()
				if err != nil {
					log.Printf("WARN: session retention sweep: %v", err)
					break
				}
				if counts.DurableRunsDeleted > 0 || counts.ActivityEventsDeleted > 0 {
					log.Printf("session retention: purged %d durable runs, %d activity events",
						counts.DurableRunsDeleted, counts.ActivityEventsDeleted)
				}
				if !more || ctx.Err() != nil {
					break
				}
			}
		}
	}()
}
