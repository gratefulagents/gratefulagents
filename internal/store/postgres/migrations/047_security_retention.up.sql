-- 047_security_retention.up.sql
-- Retention purge support (SecurityPolicyPack spec.retention).
--
-- 1. Indexes backing the namespace-scoped, cutoff-bounded purge statements so
--    each bounded batch stays cheap on large tables.
-- 2. Allow the security report artifact kinds in agent_artifacts so scan
--    reports persist (and report retention has rows to purge): the kind CHECK
--    still listed only the original kinds.

CREATE INDEX IF NOT EXISTS idx_security_findings_namespace_last_seen
    ON security_findings(namespace, last_seen_at);

CREATE INDEX IF NOT EXISTS idx_security_scans_namespace_completed
    ON security_scans(namespace, (COALESCE(completed_at, created_at)));

CREATE INDEX IF NOT EXISTS idx_security_finding_observations_namespace_observed
    ON security_finding_observations(namespace, observed_at);

CREATE INDEX IF NOT EXISTS idx_security_finding_events_created_at
    ON security_finding_events(created_at);

DO $$
DECLARE
    conname text;
BEGIN
    FOR conname IN
        SELECT con.conname
        FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        WHERE rel.relname = 'agent_artifacts'
          AND con.contype = 'c'
    LOOP
        EXECUTE format('ALTER TABLE agent_artifacts DROP CONSTRAINT %I', conname);
    END LOOP;
END $$;

ALTER TABLE agent_artifacts ADD CONSTRAINT agent_artifacts_kind_check
    CHECK (kind IN ('plan', 'diff', 'activity_log', 'review', 'feasibility', 'slack_reply', 'security_report', 'security_sarif'));
