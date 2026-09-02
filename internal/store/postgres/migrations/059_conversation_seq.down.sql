-- Restore the 056/057 trigger wiring and drop conversation_seq.

DROP INDEX IF EXISTS idx_conversation_messages_client_message_id;

DROP TRIGGER IF EXISTS session_interrupts_insert_bump_conversation_seq ON session_interrupts;
DROP TRIGGER IF EXISTS agent_artifacts_update_bump_conversation_seq ON agent_artifacts;
DROP TRIGGER IF EXISTS agent_artifacts_insert_bump_conversation_seq ON agent_artifacts;
DROP TRIGGER IF EXISTS conversation_messages_update_bump_conversation_seq ON conversation_messages;
DROP TRIGGER IF EXISTS conversation_messages_insert_bump_conversation_seq ON conversation_messages;
DROP FUNCTION IF EXISTS session_conversation_bump_seq();

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

DROP TRIGGER IF EXISTS session_interrupts_insert_bump_change_seq ON session_interrupts;
CREATE TRIGGER session_interrupts_insert_bump_change_seq
AFTER INSERT ON session_interrupts
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_children_bump_change_seq();

CREATE OR REPLACE FUNCTION agent_sessions_notify_change()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('session_change', NEW.id::text);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION agent_sessions_bump_change_seq()
RETURNS TRIGGER AS $$
BEGIN
    NEW.change_seq := OLD.change_seq + 1;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE agent_sessions DROP COLUMN IF EXISTS conversation_seq;
