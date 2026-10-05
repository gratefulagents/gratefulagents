DROP TABLE IF EXISTS
    security_scans,
    security_findings,
    security_finding_events,
    security_finding_observations,
    security_saved_filters,
    security_notification_markers,
    security_finding_artifacts,
    agent_bug_reports,
    security_research_targets,
    security_research_revisions,
    security_research_dossiers,
    security_research_hypotheses,
    security_research_hypothesis_events,
    security_research_hypothesis_lineage,
    security_research_coverage,
    security_research_variant_sweeps,
    security_research_variant_sweep_events,
    security_research_submissions,
    security_research_submission_reservations,
    security_research_submission_outcome_events,
    security_research_submission_outcomes,
    security_research_decision_snapshots,
    security_research_artifacts;

DELETE FROM agent_artifacts WHERE kind IN ('security_report', 'security_sarif');
ALTER TABLE agent_artifacts DROP CONSTRAINT agent_artifacts_kind_check;
ALTER TABLE agent_artifacts ADD CONSTRAINT agent_artifacts_kind_check
    CHECK (kind IN ('plan', 'diff', 'activity_log', 'review', 'feasibility', 'slack_reply'));

DELETE FROM resource_ownership WHERE resource_type IN ('securityscan', 'securityprogram');
DELETE FROM resource_shares WHERE resource_type IN ('securityscan', 'securityprogram');
DELETE FROM notifications WHERE resource_type IN ('securityscan', 'securityprogram');
