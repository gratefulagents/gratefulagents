-- 005_pending_actions.down.sql
ALTER TABLE agent_sessions DROP COLUMN IF EXISTS pending_actions;
