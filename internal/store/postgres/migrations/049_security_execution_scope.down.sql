DROP INDEX CONCURRENTLY IF EXISTS idx_security_findings_namespace_scan_execution;

ALTER TABLE security_findings
    DROP COLUMN IF EXISTS execution_id,
    DROP COLUMN IF EXISTS task_name;
