-- accepted_risk is a human decision. Scan runs and post-scripts (actors named
-- secscan-*) used to be able to record it, and in practice did so for
-- "already terminal, skipping" no-ops and for environments that could not run
-- a PoC. Findings whose most recent status_changed event to accepted_risk was
-- written by such an actor are reset to triaged so they stay actionable. The
-- original events are kept; the reset itself is audited as a status_changed
-- event by actor migration-063.
WITH latest AS (
    SELECT DISTINCT ON (e.finding_id) e.finding_id, e.actor
    FROM security_finding_events e
    JOIN security_findings f ON f.id = e.finding_id
    WHERE f.status = 'accepted_risk'
      AND e.event_type = 'status_changed'
      AND e.detail->>'to' = 'accepted_risk'
    ORDER BY e.finding_id, e.created_at DESC, e.id DESC
),
reset AS (
    UPDATE security_findings f
    SET status = 'triaged',
        accepted_risk_expires_at = NULL
    FROM latest
    WHERE f.id = latest.finding_id
      AND f.status = 'accepted_risk'
      AND latest.actor LIKE 'secscan-%'
    RETURNING f.id
)
INSERT INTO security_finding_events (finding_id, event_type, actor, note, detail)
SELECT id, 'status_changed', 'migration-063',
       'reset: machine-set accepted_risk is not a risk acceptance; see policy_disposition events',
       jsonb_build_object('from', 'accepted_risk', 'to', 'triaged')
FROM reset;
