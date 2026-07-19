-- 012_remove_stage.up.sql
-- Drop the legacy lifecycle column from durable session state.

ALTER TABLE agent_sessions DROP COLUMN IF EXISTS stage;
