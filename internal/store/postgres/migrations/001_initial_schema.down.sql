-- 001_initial_schema.down.sql

DROP TRIGGER IF EXISTS update_agent_artifacts_updated_at ON agent_artifacts;
DROP TRIGGER IF EXISTS update_agent_sessions_updated_at ON agent_sessions;
DROP FUNCTION IF EXISTS update_updated_at_column();
DROP TABLE IF EXISTS agent_artifacts;
DROP TABLE IF EXISTS activity_events;
DROP TABLE IF EXISTS conversation_messages;
DROP TABLE IF EXISTS agent_sessions;
