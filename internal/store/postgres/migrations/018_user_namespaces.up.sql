-- 018_user_namespaces.up.sql
-- Each user gets a personal Kubernetes namespace (firstname-lastname-<hash>)
-- where their saved credential secrets and projects live. The mapping is
-- persisted so the namespace stays stable even if the user's display name
-- changes later.

CREATE TABLE IF NOT EXISTS auth_user_namespaces (
    user_id    UUID PRIMARY KEY REFERENCES auth_users(id) ON DELETE CASCADE,
    namespace  TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
