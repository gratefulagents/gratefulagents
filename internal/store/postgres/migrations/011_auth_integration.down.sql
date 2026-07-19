-- 011_auth_integration.down.sql

ALTER TABLE resource_shares ADD COLUMN IF NOT EXISTS organization_id TEXT NOT NULL DEFAULT '';
ALTER TABLE resource_shares ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE resource_ownership ADD COLUMN IF NOT EXISTS organization_id TEXT NOT NULL DEFAULT '';
ALTER TABLE resource_ownership ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_resource_ownership_org_ws ON resource_ownership(organization_id, workspace_id);

DROP INDEX IF EXISTS idx_auth_sessions_expires;
DROP INDEX IF EXISTS idx_auth_sessions_user_id;
DROP INDEX IF EXISTS idx_auth_users_email;
DROP INDEX IF EXISTS idx_auth_users_google_id;

DROP TABLE IF EXISTS auth_sessions;
DROP TABLE IF EXISTS auth_users;
