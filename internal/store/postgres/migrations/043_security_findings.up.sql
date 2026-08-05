CREATE TABLE IF NOT EXISTS security_scans (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    namespace       TEXT NOT NULL,
    scan_name       TEXT NOT NULL,
    run_name        TEXT NOT NULL,
    session_id      UUID REFERENCES agent_sessions(id) ON DELETE SET NULL,
    repository      TEXT NOT NULL DEFAULT '',
    revision        TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'running',
    summary         TEXT NOT NULL DEFAULT '',
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    counts          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (namespace, run_name)
);

CREATE INDEX IF NOT EXISTS idx_security_scans_namespace_scan_name
    ON security_scans(namespace, scan_name);

CREATE TABLE IF NOT EXISTS security_findings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scan_id         UUID NOT NULL REFERENCES security_scans(id) ON DELETE CASCADE,
    namespace       TEXT NOT NULL,
    scan_name       TEXT NOT NULL,
    run_name        TEXT NOT NULL,
    session_id      UUID,
    fingerprint     TEXT NOT NULL,
    title           TEXT NOT NULL,
    category        TEXT NOT NULL,
    severity        TEXT NOT NULL,
    confidence      TEXT NOT NULL DEFAULT 'tentative',
    repository      TEXT NOT NULL DEFAULT '',
    revision        TEXT NOT NULL DEFAULT '',
    file_path       TEXT NOT NULL DEFAULT '',
    start_line      INT NOT NULL DEFAULT 0,
    end_line        INT NOT NULL DEFAULT 0,
    symbol          TEXT NOT NULL DEFAULT '',
    cwe             TEXT[] NOT NULL DEFAULT '{}',
    description     TEXT NOT NULL DEFAULT '',
    impact          TEXT NOT NULL DEFAULT '',
    attack_vector   TEXT NOT NULL DEFAULT '',
    remediation     TEXT NOT NULL DEFAULT '',
    references_urls TEXT[] NOT NULL DEFAULT '{}',
    source_agent    TEXT NOT NULL DEFAULT '',
    scan_step       TEXT NOT NULL DEFAULT '',
    score           DOUBLE PRECISION NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'open',
    duplicate_of    UUID REFERENCES security_findings(id) ON DELETE SET NULL,
    occurrences     INT NOT NULL DEFAULT 1,
    raw             JSONB NOT NULL DEFAULT '{}',
    first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (namespace, repository, fingerprint)
);

CREATE INDEX IF NOT EXISTS idx_security_findings_namespace_scan_name
    ON security_findings(namespace, scan_name);

CREATE INDEX IF NOT EXISTS idx_security_findings_namespace_run_name
    ON security_findings(namespace, run_name);

CREATE INDEX IF NOT EXISTS idx_security_findings_namespace_severity_status
    ON security_findings(namespace, severity, status);

CREATE INDEX IF NOT EXISTS idx_security_findings_scan_id
    ON security_findings(scan_id);

CREATE TABLE IF NOT EXISTS security_finding_events (
    id          BIGSERIAL PRIMARY KEY,
    finding_id  UUID NOT NULL REFERENCES security_findings(id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,
    actor       TEXT NOT NULL DEFAULT '',
    note        TEXT NOT NULL DEFAULT '',
    detail      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_security_finding_events_finding_id
    ON security_finding_events(finding_id);
