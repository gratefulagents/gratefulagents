ALTER TABLE slack_drafts DROP COLUMN IF EXISTS run_name;
ALTER TABLE slack_drafts DROP COLUMN IF EXISTS origin_msg_ts;
ALTER TABLE slack_drafts DROP COLUMN IF EXISTS kind;
