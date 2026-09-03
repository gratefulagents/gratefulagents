-- The SecurityScan controller reconciles each investigator's self-reported
-- handoff counters (hypotheses_examined, dynamic_experiments) against the
-- durable records the run actually wrote, keyed by actor (the AgentRun name).
-- Both lookups are actor-first scans over tables that only had revision or
-- hypothesis indexes before this migration.
--
-- Runs outside a transaction (noTxMigrations) so the builds are CONCURRENTLY
-- and never block hypothesis transitions or coverage writes from live scans.
-- The drops clear invalid leftovers from an interrupted build so the retry
-- can succeed.
-- NOTE: statements are split on semicolons after comment stripping, so keep
-- semicolons out of comment text.
DROP INDEX CONCURRENTLY IF EXISTS idx_security_research_hypothesis_events_actor;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_security_research_hypothesis_events_actor
    ON security_research_hypothesis_events (actor, hypothesis_id);

DROP INDEX CONCURRENTLY IF EXISTS idx_security_research_coverage_actor;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_security_research_coverage_actor
    ON security_research_coverage (actor, created_at DESC);
