-- 026_user_git_identity.up.sql
-- Per-user git commit identity. When set, AgentRuns created by the user author
-- their commits with this name/email (via GIT_AUTHOR_* / GIT_COMMITTER_* env in
-- the worker pod); the agent stays credited through the required
-- Co-authored-by trailer.

CREATE TABLE IF NOT EXISTS auth_user_git_identities (
    user_id    UUID PRIMARY KEY REFERENCES auth_users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL DEFAULT '',
    email      TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
