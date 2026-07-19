-- 007_resource_ownership.up.sql
-- Resource ownership tracking for collaboration.
-- Maps agent runs and projects to their owning user.

CREATE TABLE IF NOT EXISTS resource_ownership (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_type       TEXT NOT NULL,
    resource_id         TEXT NOT NULL,
    resource_namespace  TEXT NOT NULL,
    owner_id            TEXT NOT NULL,
    organization_id     TEXT NOT NULL DEFAULT '',
    workspace_id        TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE(resource_type, resource_id, resource_namespace)
);

CREATE INDEX IF NOT EXISTS idx_resource_ownership_owner ON resource_ownership(owner_id);
CREATE INDEX IF NOT EXISTS idx_resource_ownership_org_ws ON resource_ownership(organization_id, workspace_id);
