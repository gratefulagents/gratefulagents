ALTER TABLE auth_user_chat_settings
    ADD COLUMN IF NOT EXISTS default_auth_mode TEXT NOT NULL DEFAULT '';
