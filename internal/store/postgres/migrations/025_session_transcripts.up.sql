-- 025_session_transcripts.up.sql
-- One bounded transcript snapshot per session (gzip JSON of the agent's
-- in-memory run-item transcript). Upserted in place after each successful
-- turn so a restarted pod can replay full context instead of the lossy
-- durable tail. Row size is bounded by the SDK's mid-run compaction plus a
-- hard byte cap enforced by the writer; the row dies with the session.
CREATE TABLE IF NOT EXISTS session_transcripts (
    session_id  UUID PRIMARY KEY REFERENCES agent_sessions(id) ON DELETE CASCADE,
    data        BYTEA NOT NULL,
    item_count  INT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
