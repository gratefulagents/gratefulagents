-- Plan is now a first-class ModeTemplate driven by the run's mode snapshot
-- (status.modeName == "plan"); the parallel per-session mode state is gone.
ALTER TABLE agent_sessions DROP COLUMN IF EXISTS session_mode;
