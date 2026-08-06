ALTER TABLE security_findings
    ADD COLUMN IF NOT EXISTS assignee TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS accepted_risk_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS ticket_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ticket_provider TEXT NOT NULL DEFAULT '',
    -- baseline_state is new | recurring | regressed | resolved | reopened;
    -- NULL for legacy rows written before observation tracking existed.
    ADD COLUMN IF NOT EXISTS baseline_state TEXT,
    ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS triaged_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_security_findings_namespace_assignee
    ON security_findings(namespace, assignee);

-- One row per finding observation per scan run, enabling deterministic
-- scan-to-scan baseline comparison (new / recurring / resolved).
CREATE TABLE IF NOT EXISTS security_finding_observations (
    id          BIGSERIAL PRIMARY KEY,
    namespace   TEXT NOT NULL,
    scan_name   TEXT NOT NULL,
    repository  TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL,
    run_name    TEXT NOT NULL,
    revision    TEXT NOT NULL DEFAULT '',
    severity    TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_security_finding_observations_namespace_scan_run
    ON security_finding_observations(namespace, scan_name, run_name);

CREATE INDEX IF NOT EXISTS idx_security_finding_observations_fingerprint
    ON security_finding_observations(fingerprint);

-- Per-user saved finding filters, scoped to (namespace, owner).
CREATE TABLE IF NOT EXISTS security_saved_filters (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    namespace   TEXT NOT NULL,
    owner       TEXT NOT NULL,
    name        TEXT NOT NULL,
    query       JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (namespace, owner, name)
);
