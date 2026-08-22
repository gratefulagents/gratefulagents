DROP TRIGGER IF EXISTS update_security_research_hypotheses_updated_at ON security_research_hypotheses;
DROP TRIGGER IF EXISTS update_security_research_targets_updated_at ON security_research_targets;

DROP TABLE IF EXISTS security_research_decision_snapshots;
DROP TABLE IF EXISTS security_research_submission_outcomes;
DROP TABLE IF EXISTS security_research_submission_outcome_events;
DROP TABLE IF EXISTS security_research_submission_reservations;
DROP TABLE IF EXISTS security_research_submissions;
DROP TABLE IF EXISTS security_research_variant_sweep_events;
DROP TABLE IF EXISTS security_research_variant_sweeps;
DROP TABLE IF EXISTS security_research_coverage;
DROP TABLE IF EXISTS security_research_hypothesis_lineage;
DROP TABLE IF EXISTS security_research_hypothesis_events;
DROP TABLE IF EXISTS security_research_hypotheses;
DROP TABLE IF EXISTS security_research_dossiers;
DROP TABLE IF EXISTS security_research_revisions;
DROP TABLE IF EXISTS security_research_targets;
