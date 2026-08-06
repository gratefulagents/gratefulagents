-- 047_security_retention.down.sql
DROP INDEX IF EXISTS idx_security_findings_namespace_last_seen;
DROP INDEX IF EXISTS idx_security_scans_namespace_completed;
DROP INDEX IF EXISTS idx_security_finding_observations_namespace_observed;
DROP INDEX IF EXISTS idx_security_finding_events_created_at;

ALTER TABLE agent_artifacts DROP CONSTRAINT IF EXISTS agent_artifacts_kind_check;
ALTER TABLE agent_artifacts ADD CONSTRAINT agent_artifacts_kind_check
    CHECK (kind IN ('plan', 'diff', 'activity_log', 'review', 'feasibility', 'slack_reply'));
