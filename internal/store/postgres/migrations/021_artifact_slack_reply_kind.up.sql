-- 021_artifact_slack_reply_kind.up.sql
-- Allow the Slack connector's draft_reply tool to persist the proposed reply as
-- a session artifact. Without this, UpsertArtifact(kind='slack_reply') fails the
-- kind CHECK constraint. Drop whatever the existing kind CHECK is named (inline
-- column checks are auto-named), then recreate it as a superset.
DO $$
DECLARE conname text;
BEGIN
    FOR conname IN
        SELECT con.conname
        FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        WHERE rel.relname = 'agent_artifacts'
          AND con.contype = 'c'
          AND pg_get_constraintdef(con.oid) ILIKE '%kind%'
    LOOP
        EXECUTE format('ALTER TABLE agent_artifacts DROP CONSTRAINT %I', conname);
    END LOOP;
END $$;

ALTER TABLE agent_artifacts ADD CONSTRAINT agent_artifacts_kind_check
    CHECK (kind IN ('plan', 'diff', 'activity_log', 'review', 'feasibility', 'slack_reply'));
