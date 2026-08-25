-- Route session_interrupts through the migration-056 change machinery.
--
-- The 056 triggers cover the session row, conversation_messages,
-- activity_events, and agent_artifacts — but not session_interrupts, so a
-- user stop request emitted no session_change wake-up hint. The agent's
-- in-turn interrupt watcher subscribes to those hints to cancel a running
-- turn within milliseconds instead of its 1s poll backstop, so interrupt
-- inserts must bump the session (which both advances change_seq and emits
-- pg_notify('session_change', <session-uuid>) via the 056 update triggers).
--
-- Statement-level with a transition table, matching 056's lock-order
-- rationale: fire once after the statement, locking interrupts-then-session
-- consistently with every other child-table writer.

DROP TRIGGER IF EXISTS session_interrupts_insert_bump_change_seq ON session_interrupts;
CREATE TRIGGER session_interrupts_insert_bump_change_seq
AFTER INSERT ON session_interrupts
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT EXECUTE FUNCTION session_children_bump_change_seq();
