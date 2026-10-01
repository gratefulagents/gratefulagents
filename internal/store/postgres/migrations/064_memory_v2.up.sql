-- Memory v2: short, typed, citable project memories with full-text recall.
--
-- * project_state_memories moves from an untyped content blob with free tags
--   and scopes to title/body/kind/citations plus verification and usage
--   bookkeeping. Existing rows are converted in place: content becomes body,
--   the first sentence becomes the title, workspace-relative file_paths
--   become citations, and legacy kinds map onto
--   preference/decision/fact/procedure. Existing embeddings are kept (the
--   body is the old content); missing ones are backfilled lazily on recall.
-- * A weighted tsvector (title A, body B) backs ranked recall.
-- * Session summaries were written on every run but never read; the legacy
--   agent_memories table has had no reader or writer since project state
--   replaced it. Both are dropped.

ALTER TABLE project_state_memories
    ADD COLUMN IF NOT EXISTS title        TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS body         TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS citations    JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN IF NOT EXISTS commit_sha   TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS verified_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS use_count    INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ;

UPDATE project_state_memories SET
    body = content,
    title = CASE
        WHEN length(t.first_sentence) > 100 THEN left(t.first_sentence, 97) || '...'
        ELSE t.first_sentence
    END,
    kind = CASE
        WHEN scope = 'user' OR content ILIKE 'user preference%' OR content ILIKE 'user preferences%' THEN 'preference'
        WHEN kind = 'pinned' OR content ILIKE 'user decision%' OR content ILIKE 'decision%' THEN 'decision'
        WHEN kind = 'procedural' THEN 'procedure'
        ELSE 'fact'
    END,
    citations = COALESCE((
        SELECT jsonb_agg(jsonb_build_object('path', p))
        FROM unnest(file_paths) AS p
        WHERE btrim(p) <> '' AND p NOT LIKE '/%' AND p NOT LIKE '%..%'
    ), '[]'::jsonb),
    verified_at = updated_at,
    last_used_at = last_read_at
FROM (
    SELECT project_id AS pid, id AS mid,
           btrim(regexp_replace(
               split_part(split_part(btrim(content), E'\n', 1), '. ', 1),
               '\s+', ' ', 'g')) AS first_sentence
    FROM project_state_memories
) AS t
WHERE project_state_memories.project_id = t.pid AND project_state_memories.id = t.mid;

DROP INDEX IF EXISTS idx_project_state_memories_tags;

ALTER TABLE project_state_memories
    DROP COLUMN IF EXISTS content,
    DROP COLUMN IF EXISTS scope,
    DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS task_ids,
    DROP COLUMN IF EXISTS file_paths,
    DROP COLUMN IF EXISTS metadata,
    DROP COLUMN IF EXISTS last_read_at;

ALTER TABLE project_state_memories ALTER COLUMN kind SET DEFAULT 'fact';

ALTER TABLE project_state_memories
    ADD COLUMN IF NOT EXISTS search_tsv tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(body, '')), 'B')
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_project_state_memories_search
    ON project_state_memories USING GIN(search_tsv);

DROP TABLE IF EXISTS project_state_session_summaries;
DROP TABLE IF EXISTS agent_memories;
