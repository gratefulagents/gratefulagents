-- Deterministic scanner ingestion provenance (issue #152 part 3).
-- source_kind distinguishes agent-authored findings ('agent') from findings
-- ingested from deterministic tools ('scanner'); tool/tool_version/rule_id
-- identify the scanner rule; correlated_fingerprints cross-references
-- findings from the other source kind that describe the same issue without
-- merging or rewriting either row.
ALTER TABLE security_findings
    ADD COLUMN IF NOT EXISTS source_kind TEXT NOT NULL DEFAULT 'agent',
    ADD COLUMN IF NOT EXISTS tool TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tool_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS rule_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS correlated_fingerprints TEXT[] NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_security_findings_namespace_source_kind
    ON security_findings(namespace, source_kind);
