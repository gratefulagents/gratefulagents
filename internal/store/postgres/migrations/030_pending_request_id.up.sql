ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS pending_request_id TEXT NOT NULL DEFAULT '';
UPDATE agent_sessions SET pending_request_id = md5(random()::text || clock_timestamp()::text || id::text) WHERE pending_input_type <> '' AND pending_request_id = '';
CREATE OR REPLACE FUNCTION maintain_pending_request_id() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.pending_input_type = '' THEN NEW.pending_request_id := '';
    ELSE NEW.pending_request_id := md5(random()::text || clock_timestamp()::text || NEW.id::text);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS maintain_pending_request_id_on_update ON agent_sessions;
CREATE TRIGGER maintain_pending_request_id_on_update BEFORE UPDATE OF pending_question, pending_actions, pending_input_type ON agent_sessions FOR EACH ROW EXECUTE FUNCTION maintain_pending_request_id();
CREATE UNIQUE INDEX IF NOT EXISTS idx_conversation_messages_overseer_delivery ON conversation_messages (session_id, (metadata->>'delivery_id')) WHERE metadata ? 'overseer_resolution';
