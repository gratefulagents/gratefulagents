-- 012_remove_stage.down.sql

ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS stage TEXT NOT NULL DEFAULT '';
