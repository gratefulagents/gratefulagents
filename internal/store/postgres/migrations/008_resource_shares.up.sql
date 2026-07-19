-- 008_resource_shares.up.sql
-- Resource sharing for collaboration.

CREATE TABLE IF NOT EXISTS resource_shares (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_type         TEXT NOT NULL,
    resource_id           TEXT NOT NULL,
    resource_namespace    TEXT NOT NULL,
    shared_with_user_id   TEXT NOT NULL,
    shared_by_user_id     TEXT NOT NULL,
    permission            TEXT NOT NULL DEFAULT 'viewer' CHECK (permission IN ('viewer', 'collaborator')),
    organization_id       TEXT NOT NULL DEFAULT '',
    workspace_id          TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE(resource_type, resource_id, resource_namespace, shared_with_user_id)
);

CREATE INDEX IF NOT EXISTS idx_resource_shares_user ON resource_shares(shared_with_user_id);
CREATE INDEX IF NOT EXISTS idx_resource_shares_resource ON resource_shares(resource_type, resource_id, resource_namespace);
