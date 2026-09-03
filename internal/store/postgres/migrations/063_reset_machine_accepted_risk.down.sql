-- Intentionally a no-op: the reset findings can be identified by their
-- status_changed event with actor 'migration-063', but restoring accepted_risk
-- would re-create machine-made risk acceptances that no human ever approved.
SELECT 1;
