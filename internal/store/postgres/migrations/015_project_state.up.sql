-- Durable project state (SDK projectstate backbone): tasks, memories, and
-- session summaries scoped by project_id. The agent SDK provides the engine
-- and tools; the operator only supplies this persistence layer.
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS project_state_tasks (
    project_id  TEXT NOT NULL,
    id          TEXT NOT NULL,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL DEFAULT 'task',
    status      TEXT NOT NULL DEFAULT 'open',
    priority    INT  NOT NULL DEFAULT 2,
    assignee    TEXT NOT NULL DEFAULT '',
    depends_on  TEXT[] NOT NULL DEFAULT '{}',
    labels      TEXT[] NOT NULL DEFAULT '{}',
    comments    JSONB NOT NULL DEFAULT '[]',
    source_run  TEXT NOT NULL DEFAULT '',
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at   TIMESTAMPTZ,
    PRIMARY KEY (project_id, id)
);

CREATE INDEX IF NOT EXISTS idx_project_state_tasks_status
    ON project_state_tasks(project_id, status);

CREATE TABLE IF NOT EXISTS project_state_memories (
    project_id   TEXT NOT NULL,
    id           TEXT NOT NULL,
    kind         TEXT NOT NULL DEFAULT 'semantic',
    scope        TEXT NOT NULL DEFAULT 'project',
    content      TEXT NOT NULL,
    tags         TEXT[] NOT NULL DEFAULT '{}',
    task_ids     TEXT[] NOT NULL DEFAULT '{}',
    file_paths   TEXT[] NOT NULL DEFAULT '{}',
    source_run   TEXT NOT NULL DEFAULT '',
    metadata     JSONB,
    embedding    vector(1536),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_read_at TIMESTAMPTZ,
    PRIMARY KEY (project_id, id)
);

CREATE INDEX IF NOT EXISTS idx_project_state_memories_kind
    ON project_state_memories(project_id, kind);
CREATE INDEX IF NOT EXISTS idx_project_state_memories_tags
    ON project_state_memories USING GIN(tags);

CREATE TABLE IF NOT EXISTS project_state_session_summaries (
    project_id TEXT NOT NULL,
    id         TEXT NOT NULL,
    run_id     TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL,
    task_ids   TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, id)
);
