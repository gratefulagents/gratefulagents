-- 021_artifact_slack_reply_kind.down.sql
ALTER TABLE agent_artifacts DROP CONSTRAINT IF EXISTS agent_artifacts_kind_check;
ALTER TABLE agent_artifacts ADD CONSTRAINT agent_artifacts_kind_check
    CHECK (kind IN ('plan', 'diff', 'activity_log', 'review', 'feasibility'));
