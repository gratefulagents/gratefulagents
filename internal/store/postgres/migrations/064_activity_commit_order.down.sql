DROP TRIGGER IF EXISTS activity_events_assign_commit_ordered_id ON activity_events;
DROP FUNCTION IF EXISTS activity_events_assign_commit_ordered_id();
ALTER TABLE activity_events DROP COLUMN IF EXISTS client_event_id;
