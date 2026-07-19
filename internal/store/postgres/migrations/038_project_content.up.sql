CREATE TABLE IF NOT EXISTS project_content (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_namespace   TEXT NOT NULL,
    project_name        TEXT NOT NULL,
    kind                TEXT NOT NULL CHECK (kind IN ('file', 'folder', 'document', 'workbook', 'presentation', 'html')),
    path                TEXT NOT NULL,
    media_type          TEXT NOT NULL DEFAULT '',
    current_version     INT NOT NULL DEFAULT 1 CHECK (current_version > 0),
    metadata            JSONB NOT NULL DEFAULT '{}',
    provenance          JSONB NOT NULL DEFAULT '{}',
    scan_status         TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_project_content_active_path
    ON project_content(project_namespace, project_name, path)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_project_content_project
    ON project_content(project_namespace, project_name, path)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS project_content_versions (
    content_id  UUID NOT NULL REFERENCES project_content(id) ON DELETE CASCADE,
    version     INT NOT NULL CHECK (version > 0),
    content     BYTEA NOT NULL DEFAULT '',
    sha256      TEXT NOT NULL,
    size        BIGINT NOT NULL CHECK (size >= 0),
    creator     TEXT NOT NULL DEFAULT '',
    metadata    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (content_id, version)
);

CREATE TABLE IF NOT EXISTS project_content_audits (
    id          BIGSERIAL PRIMARY KEY,
    content_id  UUID NOT NULL REFERENCES project_content(id) ON DELETE CASCADE,
    action      TEXT NOT NULL,
    actor       TEXT NOT NULL DEFAULT '',
    details     JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_project_content_audits_content
    ON project_content_audits(content_id, created_at);
