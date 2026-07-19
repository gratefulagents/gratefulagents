-- 020_slack_drafts_namespace.down.sql
DROP INDEX IF EXISTS idx_slack_drafts_ns_agent_status;
ALTER TABLE slack_drafts DROP COLUMN IF EXISTS namespace;
