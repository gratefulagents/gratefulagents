-- 011_auth_integration.up.sql
-- Integrated auth: flat user model with roles, sessions for JWT refresh tokens.

CREATE TABLE IF NOT EXISTS auth_users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username        TEXT UNIQUE NOT NULL,
    email           TEXT UNIQUE,
    name            TEXT NOT NULL DEFAULT '',
    picture         TEXT NOT NULL DEFAULT '',
    password_hash   TEXT,
    google_id       TEXT UNIQUE,
    role            TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member', 'viewer')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_sessions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    refresh_token_hash  TEXT UNIQUE NOT NULL,
    expires_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_auth_users_google_id ON auth_users(google_id) WHERE google_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_auth_users_email ON auth_users(email) WHERE email IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_auth_sessions_user_id ON auth_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires ON auth_sessions(expires_at);

-- Drop org/workspace columns from collaboration tables (no longer needed).
ALTER TABLE resource_shares DROP COLUMN IF EXISTS organization_id;
ALTER TABLE resource_shares DROP COLUMN IF EXISTS workspace_id;
ALTER TABLE resource_ownership DROP COLUMN IF EXISTS organization_id;
ALTER TABLE resource_ownership DROP COLUMN IF EXISTS workspace_id;
DROP INDEX IF EXISTS idx_resource_ownership_org_ws;
