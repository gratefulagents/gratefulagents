-- 019_slack.up.sql
-- Persistent state for the SlackAgent connector: thread<->run mapping, reply
-- draft approvals, and event de-duplication.

-- Maps a Slack conversation thread to an AgentRun so follow-up messages resume
-- the same run and completion results post back to the right thread.
CREATE TABLE IF NOT EXISTS slack_threads (
    slack_agent     TEXT NOT NULL,
    channel_id      TEXT NOT NULL,
    thread_ts       TEXT NOT NULL,
    run_namespace   TEXT NOT NULL DEFAULT '',
    run_name        TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT 'command',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (slack_agent, channel_id, thread_ts)
);

CREATE INDEX IF NOT EXISTS idx_slack_threads_run
    ON slack_threads(run_namespace, run_name);

-- Reply drafts proposed to the owner for incoming DMs from other people. The
-- owner must approve before anything is sent. Doubles as an audit trail.
CREATE TABLE IF NOT EXISTS slack_drafts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slack_agent     TEXT NOT NULL,
    owner_subject   TEXT NOT NULL DEFAULT '',
    channel_id      TEXT NOT NULL,
    thread_ts       TEXT NOT NULL DEFAULT '',
    target_user     TEXT NOT NULL DEFAULT '',
    incoming_text   TEXT NOT NULL DEFAULT '',
    draft_text      TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending',
    notify_msg_ts   TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_slack_drafts_agent_status
    ON slack_drafts(slack_agent, status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_slack_drafts_owner
    ON slack_drafts(owner_subject, status, created_at DESC);

-- Idempotency guard: Socket Mode can redeliver events; record handled envelope
-- ids so reprocessing is a no-op.
CREATE TABLE IF NOT EXISTS slack_events (
    slack_agent     TEXT NOT NULL,
    envelope_id     TEXT NOT NULL,
    seen_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (slack_agent, envelope_id)
);

CREATE INDEX IF NOT EXISTS idx_slack_events_seen_at
    ON slack_events(seen_at);
