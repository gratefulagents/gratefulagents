DROP INDEX IF EXISTS idx_security_findings_namespace_source_kind;

ALTER TABLE security_findings
    DROP COLUMN IF EXISTS source_kind,
    DROP COLUMN IF EXISTS tool,
    DROP COLUMN IF EXISTS tool_version,
    DROP COLUMN IF EXISTS rule_id,
    DROP COLUMN IF EXISTS correlated_fingerprints;
