-- 022_slack_drafts_edit.down.sql
ALTER TABLE slack_drafts DROP COLUMN IF EXISTS edited_text;
