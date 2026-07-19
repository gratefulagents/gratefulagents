-- 032_user_last_login.up.sql
-- Track each user's most recent login for the admin user-management view.

ALTER TABLE auth_users ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ;
