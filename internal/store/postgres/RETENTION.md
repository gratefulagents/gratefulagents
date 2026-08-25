# Chat & Activity Persistence: Retention and Purge Design

Status: partially implemented (see "Implemented today"). This document is the
authority for what may be deleted from the session-scoped Postgres tables,
when, and why.

## Data classes and their lifecycles

| Table | Written by | Grows with | Deleted by |
|---|---|---|---|
| `agent_sessions` | controller/agent | #runs | explicit run deletion (`DeleteAgentRunData`) |
| `conversation_messages` | agent, dashboard, overseer | chat turns | cascade on session delete |
| `activity_events` | agent event writer | every SDK event (largest table) | cascade on session delete, **retention sweep** |
| `agent_artifacts` | agent (plan, activity_log, …) | #artifact kinds | cascade on session delete |
| `session_transcripts` | agent snapshotter | transcript size (upsert, bounded) | cascade on session delete |
| `durable_runs` / `durable_events` | SDK durable-run store | SDK runs & their event logs | FK cascade, **retention sweep** |

Everything session-scoped cascades from `agent_sessions` via foreign keys, so
explicit run deletion was already complete. What was missing before the sweep
was *time-based* retention: nothing expired on its own, and
`durable_runs.retain_until` (an explicit per-run deadline set by the SDK, with
a supporting partial index since migration 042) had no reader.

## Implemented today (`session_retention.go`)

`Store.PurgeExpiredSessionData` runs two batch-limited, idempotent classes;
`Store.StartSessionRetentionWorker` drives it every 10 minutes from
`cmd/main.go`. Batches are `LIMIT`-bounded with deterministic ordering, so a
sweep is resumable and safe to run on every replica concurrently.

1. **Expired durable runs** (default **on**, `DURABLE_RUN_RETENTION=false` to
   disable): deletes `durable_runs` rows whose `retain_until` elapsed;
   `durable_events` cascade. `retain_until` is opt-in per run — a NULL
   deadline means "retain forever" — so honoring it is always safe.

2. **Terminal-session activity events** (default **off**, enable with
   `SESSION_ACTIVITY_RETENTION_DAYS=<n>`): deletes `activity_events` for
   sessions that are

   - terminal (`phase IN ('succeeded','failed','cancelled')`), and
   - untouched for ≥ n days (`agent_sessions.updated_at`, which since
     migration 056 advances on *any* session write), and
   - already covered by a non-empty S3 `activity_log` artifact.

   The S3 requirement is the safety property: the dashboard's terminal
   activity read path prefers the immutable S3 event log
   (`activityLogVersion`), so the Postgres rows are redundant by the time they
   qualify. Terminal sessions *without* an S3 artifact keep their rows
   indefinitely — losing the only copy of an activity log is worse than the
   storage cost.

   Deleting activity events does not fire the change_seq trigger (insert-only
   on that table), so a purge never masquerades as live session activity.

## Deliberately NOT purged (product decisions, not engineering ones)

- **`conversation_messages`** — the user's visible chat history. Purging it
  changes what users see when they reopen an old run. If ever added, it must
  be a separate opt-in knob with its own S3-export story, and must keep the
  lifecycle invariants (a purged pending message must not resurrect as a
  hole; safest is to purge only sessions with no pending/claimed rows).
- **`agent_artifacts` / `session_transcripts`** — small (one row per kind /
  bounded upsert) and load-bearing for resume and terminal views.
- **`agent_sessions` rows themselves** — the anchor for ownership, sharing,
  and metrics aggregation. Cheap to keep; deleting them is what explicit run
  deletion is for.

## Operational notes

- Knobs: `SESSION_ACTIVITY_RETENTION_DAYS` (int days, 0/unset = off),
  `DURABLE_RUN_RETENTION` (bool, default true). Interval and batch size are
  fixed (10 min, 500) until there is evidence tuning them matters.
- The sweep drains extra batches back-to-back after a full batch, so a large
  backlog converges without waiting one interval per batch.
- Multi-replica managers may sweep concurrently; the statements are
  idempotent and batch-limited, so overlap only wastes a little work. Move to
  a leader-elected runner only if sweep contention ever shows up in practice.
