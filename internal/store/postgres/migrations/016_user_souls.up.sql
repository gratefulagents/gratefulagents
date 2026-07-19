-- 016_user_souls.up.sql
-- Per-user SOUL: a personal role/persona definition ("SOUL.md") that a user
-- edits for their own agent. Other users' agents can consult it via the
-- ask_teammate tool to get that teammate's likely perspective.

CREATE TABLE IF NOT EXISTS auth_user_souls (
    user_id    UUID PRIMARY KEY REFERENCES auth_users(id) ON DELETE CASCADE,
    content    TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
