-- 035_user_git_coauthor_setting.down.sql

ALTER TABLE auth_user_git_identities
    DROP COLUMN IF EXISTS disable_co_author_trailer;
