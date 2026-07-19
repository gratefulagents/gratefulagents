-- 037_enforce_user_git_coauthor_setting.down.sql
ALTER TABLE auth_user_git_identities
    DROP CONSTRAINT IF EXISTS auth_user_git_identities_coauthor_required;
