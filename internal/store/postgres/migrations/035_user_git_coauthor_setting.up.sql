-- 035_user_git_coauthor_setting.up.sql
-- Allow users to opt out of the agent Co-authored-by trailer. The stored value
-- is an opt-out so existing users and rows retain the current default behavior.

ALTER TABLE auth_user_git_identities
    ADD COLUMN IF NOT EXISTS disable_co_author_trailer BOOLEAN NOT NULL DEFAULT FALSE;
