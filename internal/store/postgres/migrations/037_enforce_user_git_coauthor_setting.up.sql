-- 037_enforce_user_git_coauthor_setting.up.sql
-- Agent co-author credit is mandatory. Preserve the legacy column for rolling
-- upgrades while preventing older clients from opting out.
UPDATE auth_user_git_identities
SET disable_co_author_trailer = FALSE
WHERE disable_co_author_trailer;

ALTER TABLE auth_user_git_identities
    ALTER COLUMN disable_co_author_trailer SET DEFAULT FALSE,
    ADD CONSTRAINT auth_user_git_identities_coauthor_required
        CHECK (disable_co_author_trailer = FALSE);
