-- 005_pending_actions.up.sql
-- Add pending_actions column for structured quick-action buttons.
-- Used by AskUserQuestion (with choices) and present_plan (with mode-switch actions).

ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS pending_actions JSONB NOT NULL DEFAULT '[]';
