CREATE TABLE security_research_targets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    namespace TEXT NOT NULL CHECK (btrim(namespace) <> ''),
    target_key TEXT NOT NULL CHECK (btrim(target_key) <> ''),
    kind TEXT NOT NULL CHECK (btrim(kind) <> ''),
    locator TEXT NOT NULL CHECK (btrim(locator) <> ''),
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (namespace, target_key),
    UNIQUE (id, namespace)
);

CREATE TABLE security_research_revisions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    target_id UUID NOT NULL REFERENCES security_research_targets(id) ON DELETE CASCADE,
    revision TEXT NOT NULL CHECK (btrim(revision) <> ''),
    source_uri TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (target_id, revision),
    UNIQUE (id, target_id)
);

CREATE TABLE security_research_dossiers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    revision_id UUID NOT NULL REFERENCES security_research_revisions(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version > 0),
    parent_id UUID,
    content JSONB NOT NULL,
    change_summary TEXT NOT NULL DEFAULT '',
    actor TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (revision_id, version),
    UNIQUE (revision_id, idempotency_key),
    UNIQUE (id, revision_id),
    FOREIGN KEY (parent_id, revision_id) REFERENCES security_research_dossiers(id, revision_id)
);

CREATE TABLE security_research_hypotheses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    revision_id UUID NOT NULL REFERENCES security_research_revisions(id) ON DELETE CASCADE,
    hypothesis_key TEXT NOT NULL CHECK (btrim(hypothesis_key) <> ''),
    title TEXT NOT NULL CHECK (btrim(title) <> ''),
    invariant TEXT NOT NULL CHECK (btrim(invariant) <> ''),
    status TEXT NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed', 'investigating', 'supported', 'weakened', 'falsified', 'blocked', 'superseded', 'promoted')),
    result TEXT NOT NULL DEFAULT 'pending' CHECK (result IN ('pending', 'positive', 'negative', 'failed', 'timed_out', 'inconclusive', 'abandoned')),
    detail JSONB NOT NULL DEFAULT '{}',
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (revision_id, hypothesis_key),
    UNIQUE (revision_id, idempotency_key),
    UNIQUE (id, revision_id)
);

CREATE TABLE security_research_hypothesis_events (
    id BIGSERIAL PRIMARY KEY,
    hypothesis_id UUID NOT NULL REFERENCES security_research_hypotheses(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK (event_type IN ('created', 'transitioned', 'reopened', 'lineage_added')),
    from_status TEXT NOT NULL DEFAULT '',
    to_status TEXT NOT NULL CHECK (btrim(to_status) <> ''),
    result TEXT NOT NULL CHECK (result IN ('pending', 'positive', 'negative', 'failed', 'timed_out', 'inconclusive', 'abandoned')),
    actor TEXT NOT NULL DEFAULT '',
    rationale TEXT NOT NULL DEFAULT '',
    detail JSONB NOT NULL DEFAULT '{}',
    hypothesis_version INTEGER NOT NULL CHECK (hypothesis_version > 0),
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (hypothesis_id, idempotency_key)
);

CREATE TABLE security_research_hypothesis_lineage (
    child_id UUID NOT NULL REFERENCES security_research_hypotheses(id) ON DELETE CASCADE,
    parent_id UUID NOT NULL REFERENCES security_research_hypotheses(id) ON DELETE CASCADE,
    relation TEXT NOT NULL CHECK (relation IN ('split_from', 'merged_from', 'derived_from')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (child_id, parent_id),
    CHECK (child_id <> parent_id)
);

CREATE TABLE security_research_coverage (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    revision_id UUID NOT NULL REFERENCES security_research_revisions(id) ON DELETE CASCADE,
    hypothesis_id UUID,
    dimension TEXT NOT NULL CHECK (dimension IN ('invariant', 'actor', 'state', 'transition')),
    subject_key TEXT NOT NULL CHECK (btrim(subject_key) <> ''),
    verdict TEXT NOT NULL CHECK (verdict IN ('disproved', 'adequately_tested', 'inadequately_tested', 'not_tested')),
    bounds JSONB NOT NULL DEFAULT '{}',
    evidence JSONB NOT NULL DEFAULT '[]',
    actor TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (revision_id, idempotency_key),
    FOREIGN KEY (hypothesis_id, revision_id) REFERENCES security_research_hypotheses(id, revision_id)
);

CREATE TABLE security_research_variant_sweeps (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    revision_id UUID NOT NULL REFERENCES security_research_revisions(id) ON DELETE CASCADE,
    finding_id UUID REFERENCES security_findings(id) ON DELETE SET NULL,
    root_hypothesis_id UUID,
    root_cause TEXT NOT NULL CHECK (btrim(root_cause) <> ''),
    scope JSONB NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'blocked')),
    result JSONB NOT NULL DEFAULT '{}',
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    UNIQUE (revision_id, idempotency_key),
    UNIQUE (id, revision_id),
    FOREIGN KEY (root_hypothesis_id, revision_id) REFERENCES security_research_hypotheses(id, revision_id),
    CHECK ((status IN ('completed', 'blocked') AND completed_at IS NOT NULL) OR (status IN ('pending', 'running') AND completed_at IS NULL))
);

CREATE TABLE security_research_variant_sweep_events (
    id BIGSERIAL PRIMARY KEY,
    sweep_id UUID NOT NULL REFERENCES security_research_variant_sweeps(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK (event_type IN ('created', 'completed', 'blocked')),
    actor TEXT NOT NULL DEFAULT '',
    detail JSONB NOT NULL DEFAULT '{}',
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (sweep_id, idempotency_key)
);

CREATE TABLE security_research_submissions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    revision_id UUID NOT NULL,
    target_id UUID NOT NULL,
    finding_id UUID REFERENCES security_findings(id) ON DELETE SET NULL,
    workflow TEXT NOT NULL CHECK (btrim(workflow) <> ''),
    candidate_key TEXT NOT NULL CHECK (btrim(candidate_key) <> ''),
    rank INTEGER NOT NULL CHECK (rank > 0),
    payload JSONB NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'candidate' CHECK (status IN ('candidate', 'reserved', 'submitted', 'resolved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    submitted_at TIMESTAMPTZ,
    UNIQUE (revision_id, workflow, candidate_key),
    UNIQUE (id, target_id, workflow),
    FOREIGN KEY (revision_id, target_id) REFERENCES security_research_revisions(id, target_id),
    CHECK ((status IN ('submitted', 'resolved') AND submitted_at IS NOT NULL) OR (status IN ('candidate', 'reserved') AND submitted_at IS NULL))
);

CREATE TABLE security_research_submission_reservations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    submission_id UUID NOT NULL,
    target_id UUID NOT NULL,
    workflow TEXT NOT NULL CHECK (btrim(workflow) <> ''),
    period_days INTEGER NOT NULL CHECK (period_days > 0),
    budget_limit INTEGER NOT NULL CHECK (budget_limit > 0),
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    reserved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    voided_at TIMESTAMPTZ,
    UNIQUE (target_id, workflow, idempotency_key),
    CHECK (expires_at > reserved_at),
    CHECK (voided_at IS NULL OR voided_at >= reserved_at),
    FOREIGN KEY (submission_id, target_id, workflow) REFERENCES security_research_submissions(id, target_id, workflow) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_security_research_reservations_active_submission
    ON security_research_submission_reservations(submission_id) WHERE voided_at IS NULL;

CREATE TABLE security_research_submission_outcome_events (
    id BIGSERIAL PRIMARY KEY,
    submission_id UUID NOT NULL REFERENCES security_research_submissions(id) ON DELETE CASCADE,
    outcome TEXT NOT NULL CHECK (outcome IN ('accepted', 'duplicate', 'informative', 'rejected', 'resolved')),
    external_reference TEXT NOT NULL DEFAULT '',
    rationale TEXT NOT NULL DEFAULT '',
    actor TEXT NOT NULL DEFAULT '',
    correction_of BIGINT,
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (submission_id, idempotency_key),
    UNIQUE (id, submission_id),
    FOREIGN KEY (correction_of, submission_id) REFERENCES security_research_submission_outcome_events(id, submission_id),
    CHECK ((correction_of IS NULL) OR (correction_of <> id))
);

CREATE TABLE security_research_submission_outcomes (
    submission_id UUID PRIMARY KEY REFERENCES security_research_submissions(id) ON DELETE CASCADE,
    event_id BIGINT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('accepted', 'duplicate', 'informative', 'rejected', 'resolved')),
    external_reference TEXT NOT NULL DEFAULT '',
    recorded_at TIMESTAMPTZ NOT NULL,
    UNIQUE (event_id, submission_id),
    FOREIGN KEY (event_id, submission_id) REFERENCES security_research_submission_outcome_events(id, submission_id)
);

CREATE TABLE security_research_decision_snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    revision_id UUID NOT NULL REFERENCES security_research_revisions(id) ON DELETE CASCADE,
    submission_id UUID REFERENCES security_research_submissions(id) ON DELETE SET NULL,
    workflow TEXT NOT NULL CHECK (btrim(workflow) <> ''),
    candidate_key TEXT NOT NULL CHECK (btrim(candidate_key) <> ''),
    decision TEXT NOT NULL CHECK (decision IN ('submit', 'retain', 'reject')),
    reason TEXT NOT NULL CHECK (btrim(reason) <> ''),
    rank INTEGER NOT NULL CHECK (rank > 0),
    inputs JSONB NOT NULL DEFAULT '{}',
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (revision_id, workflow, idempotency_key)
);

CREATE INDEX idx_security_research_revisions_target_created ON security_research_revisions(target_id, created_at DESC);
CREATE INDEX idx_security_research_dossiers_revision_version ON security_research_dossiers(revision_id, version DESC);
CREATE INDEX idx_security_research_hypotheses_revision_status ON security_research_hypotheses(revision_id, status, updated_at DESC);
CREATE INDEX idx_security_research_hypothesis_events_hypothesis ON security_research_hypothesis_events(hypothesis_id, id);
CREATE INDEX idx_security_research_coverage_revision_dimension ON security_research_coverage(revision_id, dimension, created_at DESC);
CREATE INDEX idx_security_research_sweeps_revision_status ON security_research_variant_sweeps(revision_id, status, created_at DESC);
CREATE INDEX idx_security_research_sweep_events_sweep ON security_research_variant_sweep_events(sweep_id, id);
CREATE INDEX idx_security_research_reservations_window ON security_research_submission_reservations(target_id, workflow, reserved_at DESC) WHERE voided_at IS NULL;
CREATE INDEX idx_security_research_submissions_precision ON security_research_submissions(target_id, workflow, submitted_at) WHERE submitted_at IS NOT NULL;
CREATE INDEX idx_security_research_outcome_events_submission ON security_research_submission_outcome_events(submission_id, id);
CREATE INDEX idx_security_research_decisions_revision ON security_research_decision_snapshots(revision_id, workflow, created_at DESC);

CREATE TRIGGER update_security_research_targets_updated_at BEFORE UPDATE ON security_research_targets FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE TRIGGER update_security_research_hypotheses_updated_at BEFORE UPDATE ON security_research_hypotheses FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
