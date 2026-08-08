-- Deterministic execution scope for security findings.
-- execution_id groups every run of one deterministic execution (fan-out
-- instances and retries all report the same id) so findings can be
-- aggregated and budgeted per execution rather than per run_name, and
-- task_name identifies which task inside that execution reported the
-- finding so per-task budgets can be enforced.
--
-- This migration runs outside a transaction (noTxMigrations) so the index can
-- be built CONCURRENTLY without taking a lock that blocks finding writes on
-- populated installations. The drop clears any invalid leftover from a
-- previously interrupted concurrent build so the retry can succeed.
-- NOTE: statements are split on semicolons after comment stripping, so keep
-- semicolons out of comment text.
ALTER TABLE security_findings
    ADD COLUMN IF NOT EXISTS execution_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS task_name TEXT NOT NULL DEFAULT '';

DROP INDEX CONCURRENTLY IF EXISTS idx_security_findings_namespace_scan_execution;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_security_findings_namespace_scan_execution
    ON security_findings(namespace, scan_name, execution_id);
