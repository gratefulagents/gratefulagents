-- 064_activity_commit_order.up.sql
-- Activity readers page by id ("id > cursor"): the dashboard's delta watch,
-- its shared activity memo, and the frontend all advance a cursor to the
-- highest id they have seen. BIGSERIAL hands ids out when a row is inserted,
-- not when its transaction commits, so two concurrent writers for one
-- session (the agent's batched event writer, the agent's direct notices, the
-- dashboard's stop notice) could commit out of id order. A reader that saw
-- the higher id first moved its cursor past the lower ids, and those events
-- never reached the live view.
--
-- The BEFORE INSERT trigger takes the session row lock first and only then
-- draws the id, so ids are allocated while the lock is held and the lock is
-- released at commit: for one session, id order is commit order. The column
-- default's id is discarded (gaps are harmless). The lock is the same one the
-- AFTER statement trigger from migration 056 already takes to bump change_seq.
--
-- client_event_id is an optional writer-chosen idempotency key so a batch
-- retried after an ambiguous failure (the commit landed but the client timed
-- out) does not insert the same events twice. Its unique index is built
-- concurrently by migration 065.

ALTER TABLE activity_events ADD COLUMN IF NOT EXISTS client_event_id UUID;

CREATE OR REPLACE FUNCTION activity_events_assign_commit_ordered_id()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM 1 FROM agent_sessions WHERE id = NEW.session_id FOR NO KEY UPDATE;
    NEW.id := nextval('activity_events_id_seq');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS activity_events_assign_commit_ordered_id ON activity_events;
CREATE TRIGGER activity_events_assign_commit_ordered_id
BEFORE INSERT ON activity_events
FOR EACH ROW EXECUTE FUNCTION activity_events_assign_commit_ordered_id();
