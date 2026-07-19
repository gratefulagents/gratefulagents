-- Channel-reply approval: the agent's conversational replies to public surfaces
-- (channels, groups, group DMs) go through the same owner-approval card as inbox
-- triage drafts. kind tells the two apart: 'triage' replies to a third-party DM
-- as the owner; 'channel_reply' posts the agent's own reply into a channel
-- thread as the bot. origin_msg_ts is the triggering message (for reaction
-- bookkeeping) and run_name is the command run that produced the reply (so
-- Regenerate can wake it).
ALTER TABLE slack_drafts ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'triage';
ALTER TABLE slack_drafts ADD COLUMN IF NOT EXISTS origin_msg_ts TEXT NOT NULL DEFAULT '';
ALTER TABLE slack_drafts ADD COLUMN IF NOT EXISTS run_name TEXT NOT NULL DEFAULT '';
