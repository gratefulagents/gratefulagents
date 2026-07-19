-- 022_slack_drafts_edit.up.sql
-- Draft UX v2: the owner can edit a proposed reply before sending. edited_text
-- records the final text actually sent when it differs from the generated
-- draft (draft_text keeps the original for the audit trail).
ALTER TABLE slack_drafts ADD COLUMN IF NOT EXISTS edited_text TEXT NOT NULL DEFAULT '';
