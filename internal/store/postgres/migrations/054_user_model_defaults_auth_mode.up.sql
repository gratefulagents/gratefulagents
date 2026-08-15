-- Persist which saved provider credential personal model defaults should use.
ALTER TABLE auth_user_model_defaults
    ADD COLUMN IF NOT EXISTS auth_mode TEXT NOT NULL DEFAULT '';
