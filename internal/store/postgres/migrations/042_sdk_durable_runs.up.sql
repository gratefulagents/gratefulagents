CREATE TABLE IF NOT EXISTS durable_runs (
    tenant_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    revision BIGINT NOT NULL,
    event_sequence BIGINT NOT NULL,
    snapshot BYTEA NOT NULL,
    retain_until TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    lease_owner TEXT NULL,
    lease_token TEXT NULL,
    lease_until TIMESTAMPTZ NULL,
    PRIMARY KEY (tenant_id, run_id)
);

CREATE TABLE IF NOT EXISTS durable_events (
    tenant_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    sequence BIGINT NOT NULL,
    body BYTEA NOT NULL,
    PRIMARY KEY (tenant_id, run_id, sequence),
    FOREIGN KEY (tenant_id, run_id) REFERENCES durable_runs (tenant_id, run_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS durable_runs_retention_idx
    ON durable_runs (retain_until)
    WHERE retain_until IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_conversation_messages_durable_pass_unique
    ON conversation_messages (session_id, (metadata ->> 'durable_pass_key'))
    WHERE metadata ? 'durable_pass_key';
