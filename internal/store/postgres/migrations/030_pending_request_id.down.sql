DROP INDEX IF EXISTS idx_conversation_messages_overseer_delivery;
DROP TRIGGER IF EXISTS maintain_pending_request_id_on_update ON agent_sessions;
DROP FUNCTION IF EXISTS maintain_pending_request_id();
ALTER TABLE agent_sessions DROP COLUMN IF EXISTS pending_request_id;
