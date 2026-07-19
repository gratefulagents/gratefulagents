-- 031_user_role_models.up.sql
-- Personal provider-specific model overrides for specialist roles. Missing rows
-- inherit the cluster RoleInstruction defaults.

CREATE TABLE IF NOT EXISTS auth_user_role_models (
    user_id     UUID NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    role_name   TEXT NOT NULL,
    provider    TEXT NOT NULL,
    model       TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_name, provider),
    CHECK (octet_length(role_name) BETWEEN 1 AND 253),
    CHECK (provider IN ('openai', 'anthropic', 'copilot', 'gemini', 'openrouter', 'groq')),
    CHECK (octet_length(model) BETWEEN 1 AND 512)
);
