-- Best-effort rollback of memory v2. Converted rows keep their body as
-- content; titles, citations and usage bookkeeping are discarded. Dropped
-- session summaries and legacy agent memories are not restored (neither had a
-- reader); their empty tables are recreated so older binaries still start.
DROP INDEX IF EXISTS idx_project_state_memories_search;

ALTER TABLE project_state_memories
    DROP COLUMN IF EXISTS search_tsv,
    ADD COLUMN IF NOT EXISTS content      TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS scope        TEXT NOT NULL DEFAULT 'project',
    ADD COLUMN IF NOT EXISTS tags         TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS task_ids     TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS file_paths   TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS metadata     JSONB,
    ADD COLUMN IF NOT EXISTS last_read_at TIMESTAMPTZ;

UPDATE project_state_memories SET
    content = CASE WHEN title <> '' THEN title || E'\n' || body ELSE body END,
    kind = CASE kind
        WHEN 'decision' THEN 'pinned'
        WHEN 'preference' THEN 'pinned'
        WHEN 'procedure' THEN 'procedural'
        ELSE 'semantic'
    END,
    file_paths = COALESCE(ARRAY(
        SELECT c->>'path' FROM jsonb_array_elements(citations) AS c WHERE c->>'path' IS NOT NULL
    ), '{}'),
    last_read_at = last_used_at;

ALTER TABLE project_state_memories
    DROP COLUMN IF EXISTS title,
    DROP COLUMN IF EXISTS body,
    DROP COLUMN IF EXISTS citations,
    DROP COLUMN IF EXISTS commit_sha,
    DROP COLUMN IF EXISTS verified_at,
    DROP COLUMN IF EXISTS use_count,
    DROP COLUMN IF EXISTS last_used_at;

ALTER TABLE project_state_memories ALTER COLUMN kind SET DEFAULT 'semantic';

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

CREATE TABLE IF NOT EXISTS agent_memories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    namespace   TEXT NOT NULL,
    content     TEXT NOT NULL,
    embedding   vector(1536),
    tags        TEXT[] NOT NULL DEFAULT '{}',
    source_run  TEXT NOT NULL DEFAULT '',
    metadata    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
