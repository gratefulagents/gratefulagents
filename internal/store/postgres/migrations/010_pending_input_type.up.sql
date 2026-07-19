-- 010_pending_input_type.up.sql
-- Add pending_input_type column to classify the kind of user input requested.
-- Values: question, approval, plan_review, turn_limit, idle, circuit_breaker, stopped.

ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS pending_input_type TEXT NOT NULL DEFAULT '';
