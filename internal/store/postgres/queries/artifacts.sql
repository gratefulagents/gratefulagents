-- name: UpsertArtifact :one
INSERT INTO agent_artifacts (session_id, kind, content, s3_url, content_hash, metadata)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (session_id, kind) DO UPDATE
SET content = EXCLUDED.content,
    s3_url = EXCLUDED.s3_url,
    content_hash = EXCLUDED.content_hash,
    metadata = EXCLUDED.metadata
RETURNING *;

-- name: GetArtifact :one
SELECT * FROM agent_artifacts
WHERE session_id = $1 AND kind = $2;

-- name: GetArtifacts :many
SELECT * FROM agent_artifacts
WHERE session_id = $1
ORDER BY created_at ASC;
