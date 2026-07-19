DROP TABLE IF EXISTS project_content_blob_deletions;
DROP INDEX IF EXISTS idx_project_content_versions_object_key;
ALTER TABLE project_content_versions DROP COLUMN IF EXISTS object_key;
