-- 032_user_last_login.down.sql

ALTER TABLE auth_users DROP COLUMN IF EXISTS last_login_at;
