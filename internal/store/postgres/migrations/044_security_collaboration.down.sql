DROP TABLE IF EXISTS security_saved_filters;

DROP TABLE IF EXISTS security_finding_observations;

DROP INDEX IF EXISTS idx_security_findings_namespace_assignee;

ALTER TABLE security_findings
    DROP COLUMN IF EXISTS assignee,
    DROP COLUMN IF EXISTS accepted_risk_expires_at,
    DROP COLUMN IF EXISTS ticket_url,
    DROP COLUMN IF EXISTS ticket_provider,
    DROP COLUMN IF EXISTS baseline_state,
    DROP COLUMN IF EXISTS resolved_at,
    DROP COLUMN IF EXISTS triaged_at;
