ALTER TABLE project_content_versions
    ADD COLUMN IF NOT EXISTS object_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_project_content_versions_object_key
    ON project_content_versions(object_key)
    WHERE object_key <> '';

-- Object locators survive project/version deletion until S3 confirms removal.
-- This is a durable deletion outbox that the controller reconciles on startup
-- and after every project-content deletion.
CREATE TABLE IF NOT EXISTS project_content_blob_deletions (
    object_key TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- During the compatibility window, existing BYTEA-backed rows remain readable
-- and are promoted to S3 lazily without removing their rollback copy. New rows
-- are dual-written to S3 and BYTEA; readers prefer S3 whenever object_key is set.
