-- 033_xai_role_models.up.sql
-- Allow xAI/Grok selections in personal role-model overrides.

ALTER TABLE auth_user_role_models
    DROP CONSTRAINT IF EXISTS auth_user_role_models_provider_check;

ALTER TABLE auth_user_role_models
    ADD CONSTRAINT auth_user_role_models_provider_check
    CHECK (provider IN ('openai', 'anthropic', 'copilot', 'gemini', 'openrouter', 'groq', 'xai'));
