-- 020_slack_drafts_namespace.up.sql
-- Scope reply drafts to the owning namespace so the dashboard drafts inbox is
-- tenant-isolated: SlackAgent names are only unique within a namespace, so a
-- query by slack_agent alone could otherwise cross tenant boundaries.
ALTER TABLE slack_drafts ADD COLUMN IF NOT EXISTS namespace TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_slack_drafts_ns_agent_status
    ON slack_drafts(namespace, slack_agent, status, created_at DESC);
