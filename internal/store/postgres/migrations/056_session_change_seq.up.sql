-- Cheap watch fingerprints, change notifications, and poll indexes.
--
-- change_seq is a per-session monotonic version counter: any write to the
-- session row or to its child tables (messages, activity events, artifacts)
-- bumps it, so watchers detect "something changed" with one primary-key read
-- instead of aggregating the whole transcript. A counter — not a tail
-- fingerprint — is required because MarkMessagesDelivered and cancellation
-- mutate OLD message rows in place without appending anything.
--
-- The same session-row update path emits a pg_notify wake-up hint on the
-- session_change channel (payload: session UUID). Notifications are lossy by
-- design (dropped across reconnects, never queued for non-listeners), so they
-- only shorten poll latency; the polling probe stays authoritative.

ALTER TABLE agent_sessions
    ADD COLUMN IF NOT EXISTS change_seq BIGINT NOT NULL DEFAULT 0;

-- Every UPDATE of a session row bumps its version, including the no-op
-- updates routed through the child-table trigger below.
CREATE OR REPLACE FUNCTION agent_sessions_bump_change_seq()
RETURNS TRIGGER AS $$
BEGIN
    NEW.change_seq := OLD.change_seq + 1;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS agent_sessions_bump_change_seq_on_update ON agent_sessions;
CREATE TRIGGER agent_sessions_bump_change_seq_on_update
BEFORE UPDATE ON agent_sessions
FOR EACH ROW EXECUTE FUNCTION agent_sessions_bump_change_seq();

-- Wake-up hint, delivered on commit. Identical notifications inside one
-- transaction are coalesced by Postgres, so multi-row writes cost one send.
CREATE OR REPLACE FUNCTION agent_sessions_notify_change()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('session_change', NEW.id::text);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS agent_sessions_notify_change_on_update ON agent_sessions;
CREATE TRIGGER agent_sessions_notify_change_on_update
AFTER UPDATE ON agent_sessions
FOR EACH ROW EXECUTE FUNCTION agent_sessions_notify_change();

-- Child-table writes route through the session row so one counter covers new
-- messages, in-place delivered/cancelled metadata flips, activity events, and
-- plan artifact upserts. The migration-001 updated_at trigger also fires on
-- these bumps, so agent_sessions.updated_at now means "last write of any
-- kind" — which the retention sweep relies on to keep recently active
-- sessions untouched. Deletes are excluded — retention purges of old child
-- rows must not look like live session activity.
--
-- These are STATEMENT-level triggers with transition tables, not row-level:
-- a row-level trigger would take the session row lock in the middle of a
-- multi-row statement (message locks, then session, then more messages) and
-- deadlock against concurrent single-row writers that lock message-then-
-- session. Firing once after the statement keeps a consistent
-- messages-then-session lock order everywhere.
CREATE OR REPLACE FUNCTION session_children_bump_change_seq()
RETURNS TRIGGER AS $$
BEGIN
    UPDATE agent_sessions s
       SET change_seq = s.change_seq + 1
     WHERE s.id IN (SELECT DISTINCT session_id FROM new_rows);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS conversation_messages_insert_bump_change_seq ON conversation_messages;
CREATE TRIGGER conversation_messages_insert_bump_change_seq
AFTER INSERT ON conversation_messages
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_children_bump_change_seq();

DROP TRIGGER IF EXISTS conversation_messages_update_bump_change_seq ON conversation_messages;
CREATE TRIGGER conversation_messages_update_bump_change_seq
AFTER UPDATE ON conversation_messages
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_children_bump_change_seq();

DROP TRIGGER IF EXISTS activity_events_insert_bump_change_seq ON activity_events;
CREATE TRIGGER activity_events_insert_bump_change_seq
AFTER INSERT ON activity_events
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_children_bump_change_seq();

DROP TRIGGER IF EXISTS agent_artifacts_insert_bump_change_seq ON agent_artifacts;
CREATE TRIGGER agent_artifacts_insert_bump_change_seq
AFTER INSERT ON agent_artifacts
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_children_bump_change_seq();

DROP TRIGGER IF EXISTS agent_artifacts_update_bump_change_seq ON agent_artifacts;
CREATE TRIGGER agent_artifacts_update_bump_change_seq
AFTER UPDATE ON agent_artifacts
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_children_bump_change_seq();

-- Overseer-hold gate probe: PollNewUserMessages and the GetMessages family
-- test COALESCE(metadata, '{}'::jsonb) ? 'overseer_held'; this partial index
-- makes the min-held-id probe an index descent instead of a per-candidate
-- correlated scan of the session's messages.
CREATE INDEX IF NOT EXISTS idx_conversation_messages_overseer_held
    ON conversation_messages (session_id, id)
    WHERE (COALESCE(metadata, '{}'::jsonb) ? 'overseer_held');
