-- Governed suppressions (SecurityPolicyPack suppression rules). A finding is
-- suppressed while suppressed_by is non-empty: it is excluded from
-- failOnSeverity gating and default list/summary results but never deleted,
-- and every transition is audited in security_finding_events.
ALTER TABLE security_findings
    ADD COLUMN IF NOT EXISTS suppressed_by TEXT,
    ADD COLUMN IF NOT EXISTS suppressed_reason TEXT,
    ADD COLUMN IF NOT EXISTS suppressed_owner TEXT,
    ADD COLUMN IF NOT EXISTS suppression_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS suppressed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_security_findings_suppression_expiry
    ON security_findings(namespace, suppression_expires_at)
    WHERE suppressed_by IS NOT NULL;
