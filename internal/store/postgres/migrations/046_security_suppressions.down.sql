DROP INDEX IF EXISTS idx_security_findings_suppression_expiry;

ALTER TABLE security_findings
    DROP COLUMN IF EXISTS suppressed_by,
    DROP COLUMN IF EXISTS suppressed_reason,
    DROP COLUMN IF EXISTS suppressed_owner,
    DROP COLUMN IF EXISTS suppression_expires_at,
    DROP COLUMN IF EXISTS suppressed_at;
