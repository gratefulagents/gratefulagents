-- Conversation-only watch fingerprint and per-session wake-up channels.
--
-- change_seq (migration 056) bumps on every write of any kind, including
-- activity_events inserts. The dashboard conversation watch keys off the
-- session fingerprint, so every tool-call log line invalidated the whole
-- conversation frame. conversation_seq is a second monotonic counter that
-- advances only for conversation-relevant writes:
--   * direct agent_sessions updates that change something other than the
--     counters/updated_at themselves (phase, pending question, metadata, ...),
--   * conversation_messages inserts and in-place updates,
--   * agent_artifacts inserts and updates (plan artifact),
--   * session_interrupts inserts.
-- activity_events inserts still bump only change_seq.
--
-- The session-row notify trigger additionally emits on a per-session channel
-- (session_change_<uuid-without-dashes>, 47 chars, under the 63-char
-- identifier limit) so an agent pod can LISTEN for its own session only
-- instead of receiving the fleet-wide session_change stream.

ALTER TABLE agent_sessions
    ADD COLUMN IF NOT EXISTS conversation_seq BIGINT NOT NULL DEFAULT 0;

-- Every UPDATE still bumps change_seq. conversation_seq advances only when
-- the row changed in some column other than the counters and updated_at, so
-- the counter-only UPDATE issued by the activity_events child trigger leaves
-- it untouched while phase/pending-input/metadata changes advance it. The
-- diff excludes change_seq because the 056 child trigger's UPDATE sets it.
CREATE OR REPLACE FUNCTION agent_sessions_bump_change_seq()
RETURNS TRIGGER AS $$
BEGIN
    NEW.change_seq := OLD.change_seq + 1;
    IF (to_jsonb(NEW) - 'change_seq' - 'conversation_seq' - 'updated_at')
       IS DISTINCT FROM
       (to_jsonb(OLD) - 'change_seq' - 'conversation_seq' - 'updated_at') THEN
        NEW.conversation_seq := OLD.conversation_seq + 1;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Feed both the global channel (dashboard) and the per-session channel
-- (agent pods). Same coalescing rules apply per channel.
CREATE OR REPLACE FUNCTION agent_sessions_notify_change()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('session_change', NEW.id::text);
    PERFORM pg_notify('session_change_' || replace(NEW.id::text, '-', ''), NEW.id::text);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- Conversation-relevant child tables bump both counters in one session-row
-- UPDATE (the BEFORE UPDATE row trigger above adds change_seq on top). These
-- replace the 056/057 triggers on the same tables rather than sitting beside
-- them, so each child statement still costs exactly one session UPDATE.
-- Statement-level with transition tables for the 056 lock-order reasons.
CREATE OR REPLACE FUNCTION session_conversation_bump_seq()
RETURNS TRIGGER AS $$
BEGIN
    UPDATE agent_sessions s
       SET conversation_seq = s.conversation_seq + 1
     WHERE s.id IN (SELECT DISTINCT session_id FROM new_rows);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS conversation_messages_insert_bump_change_seq ON conversation_messages;
DROP TRIGGER IF EXISTS conversation_messages_insert_bump_conversation_seq ON conversation_messages;
CREATE TRIGGER conversation_messages_insert_bump_conversation_seq
AFTER INSERT ON conversation_messages
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_conversation_bump_seq();

DROP TRIGGER IF EXISTS conversation_messages_update_bump_change_seq ON conversation_messages;
DROP TRIGGER IF EXISTS conversation_messages_update_bump_conversation_seq ON conversation_messages;
CREATE TRIGGER conversation_messages_update_bump_conversation_seq
AFTER UPDATE ON conversation_messages
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_conversation_bump_seq();

DROP TRIGGER IF EXISTS agent_artifacts_insert_bump_change_seq ON agent_artifacts;
DROP TRIGGER IF EXISTS agent_artifacts_insert_bump_conversation_seq ON agent_artifacts;
CREATE TRIGGER agent_artifacts_insert_bump_conversation_seq
AFTER INSERT ON agent_artifacts
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_conversation_bump_seq();

DROP TRIGGER IF EXISTS agent_artifacts_update_bump_change_seq ON agent_artifacts;
DROP TRIGGER IF EXISTS agent_artifacts_update_bump_conversation_seq ON agent_artifacts;
CREATE TRIGGER agent_artifacts_update_bump_conversation_seq
AFTER UPDATE ON agent_artifacts
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_conversation_bump_seq();

DROP TRIGGER IF EXISTS session_interrupts_insert_bump_change_seq ON session_interrupts;
DROP TRIGGER IF EXISTS session_interrupts_insert_bump_conversation_seq ON session_interrupts;
CREATE TRIGGER session_interrupts_insert_bump_conversation_seq
AFTER INSERT ON session_interrupts
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_conversation_bump_seq();

-- activity_events keeps the 056 change_seq-only trigger.

