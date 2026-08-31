CREATE TABLE security_research_artifacts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    revision_id UUID NOT NULL REFERENCES security_research_revisions(id) ON DELETE CASCADE,
    execution_id TEXT NOT NULL CHECK (btrim(execution_id) <> '' AND octet_length(execution_id) <= 256),
    task_name TEXT NOT NULL CHECK (btrim(task_name) <> '' AND octet_length(task_name) <= 256),
    kind TEXT NOT NULL CHECK (kind IN ('manifest', 'trace', 'harness_summary', 'blocker')),
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 262144),
    candidate_fingerprints TEXT[] NOT NULL DEFAULT '{}' CHECK (cardinality(candidate_fingerprints) <= 64),
    coverage_ids JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(coverage_ids) = 'object' AND octet_length(coverage_ids::text) <= 8192),
    blocker_ids UUID[] NOT NULL DEFAULT '{}' CHECK (cardinality(blocker_ids) <= 64),
    conditions JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(conditions) = 'object' AND octet_length(conditions::text) <= 4096),
    actor TEXT NOT NULL CHECK (btrim(actor) <> '' AND octet_length(actor) <= 256),
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 256),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (revision_id, execution_id, task_name, idempotency_key)
);

CREATE INDEX idx_security_research_artifacts_scope
    ON security_research_artifacts(revision_id, execution_id, created_at DESC, id);
