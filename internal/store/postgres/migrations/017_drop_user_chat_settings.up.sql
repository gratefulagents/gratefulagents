-- 017_drop_user_chat_settings.up.sql
-- The modeless "New Chat" feature was removed. Per-user defaults for it are no
-- longer used; drop the table.

DROP TABLE IF EXISTS auth_user_chat_settings;
