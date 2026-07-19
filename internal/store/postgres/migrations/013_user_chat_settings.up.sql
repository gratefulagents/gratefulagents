-- 013_user_chat_settings.up.sql
-- Per-user default chat settings used by modeless "New Chat" runs.

CREATE TABLE IF NOT EXISTS auth_user_chat_settings (
    user_id          UUID PRIMARY KEY REFERENCES auth_users(id) ON DELETE CASCADE,
    default_model    TEXT NOT NULL DEFAULT '',
    default_provider TEXT NOT NULL DEFAULT '',
    system_prompt    TEXT NOT NULL DEFAULT '',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
