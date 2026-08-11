CREATE TABLE IF NOT EXISTS security_finding_artifacts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    finding_id UUID NOT NULL REFERENCES security_findings(id) ON DELETE CASCADE,
    execution_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('poc_candidate', 'poc_validation', 'bounty_submission', 'submission_bundle')),
    content JSONB NOT NULL DEFAULT '{}',
    s3_key TEXT NOT NULL DEFAULT '',
    sha256 TEXT NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    media_type TEXT NOT NULL DEFAULT '',
    filename TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    actor_run TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (finding_id, execution_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_security_finding_artifacts_finding
    ON security_finding_artifacts(finding_id);

CREATE TRIGGER update_security_finding_artifacts_updated_at
BEFORE UPDATE ON security_finding_artifacts
FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
