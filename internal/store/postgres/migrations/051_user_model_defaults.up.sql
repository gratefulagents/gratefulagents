-- 051_user_model_defaults.up.sql
-- Per-user default provider/model/reasoning-level preference. `disabled` keeps
-- the saved values but tells clients not to auto-apply them.

CREATE TABLE IF NOT EXISTS auth_user_model_defaults (
    user_id         UUID PRIMARY KEY REFERENCES auth_users(id) ON DELETE CASCADE,
    provider        TEXT NOT NULL DEFAULT '',
    model           TEXT NOT NULL DEFAULT '',
    reasoning_level TEXT NOT NULL DEFAULT '',
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
