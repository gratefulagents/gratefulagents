package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

const contentColumns = `c.id, c.project_namespace, c.project_name, c.kind, c.path, c.media_type, c.current_version,
	c.metadata, c.provenance, c.scan_status, c.created_at, c.updated_at, c.deleted_at,
	COALESCE(v.size, 0), COALESCE(v.sha256, ''), COALESCE(v.creator, '')`

// contentSelect joins each item with its current version's metadata (never the
// bytes) so listings and lookups stay cheap regardless of file sizes. The join
// is LEFT because confirmed deletion purges version rows to recover quota
// while keeping the item row for auditing and include_deleted listings.
const contentSelect = `SELECT ` + contentColumns + ` FROM project_content c
	LEFT JOIN project_content_versions v ON v.content_id = c.id AND v.version = c.current_version`

// contentRowColumns lists the project_content columns available on INSERT and
// UPDATE ... RETURNING, where the current-version join is not available.
const contentRowColumns = `id, project_namespace, project_name, kind, path, media_type, current_version,
	metadata, provenance, scan_status, created_at, updated_at, deleted_at`

func (s *Store) ListContent(ctx context.Context, projectNamespace, projectName string, includeDeleted bool) ([]store.ProjectContent, error) {
	query := contentSelect + `
		WHERE c.project_namespace = $1 AND c.project_name = $2`
	if !includeDeleted {
		query += ` AND c.deleted_at IS NULL`
	}
	query += ` ORDER BY c.path`

	rows, err := s.pool.Query(ctx, query, projectNamespace, projectName)
	if err != nil {
		return nil, fmt.Errorf("listing project content: %w", err)
	}
	defer rows.Close()

	content := make([]store.ProjectContent, 0)
	for rows.Next() {
		item, err := scanContent(rows)
		if err != nil {
			return nil, err
		}
		content = append(content, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing project content: %w", err)
	}
	return content, nil
}

func (s *Store) GetContent(ctx context.Context, id uuid.UUID) (*store.ProjectContent, error) {
	item, err := getContent(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) GetContentVersion(ctx context.Context, id uuid.UUID, version int) (*store.ProjectContentVersion, error) {
	query := `SELECT v.content_id, v.version, v.content, v.object_key, v.sha256, v.size, v.creator, v.metadata, v.created_at
		FROM project_content_versions v
		JOIN project_content c ON c.id = v.content_id
		WHERE v.content_id = $1 AND v.version = CASE WHEN $2 = 0 THEN c.current_version ELSE $2 END`
	item, err := scanContentVersion(s.pool.QueryRow(ctx, query, id, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrContentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting project content version: %w", err)
	}
	if err := s.loadProjectContentVersion(ctx, &item, true); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) CreateContent(ctx context.Context, opts store.CreateContentOptions) (*store.ProjectContent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning project content creation: %w", err)
	}
	defer tx.Rollback(ctx)

	item, err := insertContent(ctx, tx, opts)
	if err != nil {
		return nil, err
	}
	if err := s.insertContentVersion(ctx, tx, &item, 1, opts.Content, opts.Actor, item.Metadata); err != nil {
		return nil, err
	}
	stagedObjectKey := s.stagedProjectContentObjectKey(item.ID, 1, item.ContentHash)
	defer s.cleanupStagedProjectContentObject(&stagedObjectKey)
	if err := insertContentAudit(ctx, tx, item.ID, "create", opts.Actor, map[string]any{"version": 1}); err != nil {
		return nil, err
	}
	// A commit error is ambiguous: the transaction may have committed. Never
	// delete its object after crossing the commit boundary.
	stagedObjectKey = ""
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing project content creation: %w", err)
	}
	stagedObjectKey = ""
	return &item, nil
}

func (s *Store) UpdateContent(ctx context.Context, id uuid.UUID, opts store.UpdateContentOptions) (*store.ProjectContent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning project content update: %w", err)
	}
	defer tx.Rollback(ctx)

	item, err := getContent(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if item.CurrentVersion != opts.ExpectedVersion {
		return nil, store.ErrContentConflict
	}
	if opts.Content != nil && !opts.OverwriteConfirmed {
		return nil, store.ErrContentConfirmationRequired
	}
	if opts.Path != nil {
		item.Path = *opts.Path
	}
	if opts.MediaType != nil {
		item.MediaType = *opts.MediaType
	}
	if opts.Metadata != nil {
		item.Metadata = jsonOrEmpty(*opts.Metadata)
	}
	if opts.Provenance != nil {
		item.Provenance = jsonOrEmpty(*opts.Provenance)
	}
	if opts.ScanStatus != nil {
		item.ScanStatus = *opts.ScanStatus
	}
	if opts.Content != nil {
		item.CurrentVersion++
	}

	updated, err := updateContent(ctx, tx, item)
	if err != nil {
		return nil, err
	}
	stagedObjectKey := ""
	defer s.cleanupStagedProjectContentObject(&stagedObjectKey)
	if opts.Content != nil {
		if err := s.insertContentVersion(ctx, tx, &updated, updated.CurrentVersion, *opts.Content, opts.Actor, updated.Metadata); err != nil {
			return nil, err
		}
		stagedObjectKey = s.stagedProjectContentObjectKey(updated.ID, updated.CurrentVersion, updated.ContentHash)
	}
	details := map[string]any{"version": updated.CurrentVersion}
	if opts.Content != nil {
		details["content_changed"] = true
	}
	if err := insertContentAudit(ctx, tx, id, "update", opts.Actor, details); err != nil {
		return nil, err
	}
	stagedObjectKey = ""
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing project content update: %w", err)
	}
	stagedObjectKey = ""
	return &updated, nil
}

func (s *Store) DuplicateContent(ctx context.Context, id uuid.UUID, opts store.DuplicateContentOptions) (*store.ProjectContent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning project content duplication: %w", err)
	}
	defer tx.Rollback(ctx)

	source, err := getContent(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if source.CurrentVersion != opts.ExpectedVersion {
		return nil, store.ErrContentConflict
	}
	version, err := getContentVersion(ctx, tx, id, source.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if err := s.loadProjectContentVersion(ctx, &version, false); err != nil {
		return nil, err
	}
	create := store.CreateContentOptions{
		ProjectNamespace: source.ProjectNamespace,
		ProjectName:      source.ProjectName,
		Kind:             source.Kind,
		Path:             opts.Path,
		MediaType:        source.MediaType,
		Content:          version.Content,
		Metadata:         source.Metadata,
		Provenance:       source.Provenance,
		ScanStatus:       source.ScanStatus,
		Actor:            opts.Actor,
	}
	if opts.ProjectNamespace != "" {
		create.ProjectNamespace = opts.ProjectNamespace
	}
	if opts.ProjectName != "" {
		create.ProjectName = opts.ProjectName
	}
	if opts.Kind != nil {
		create.Kind = *opts.Kind
	}
	if opts.MediaType != nil {
		create.MediaType = *opts.MediaType
	}
	if opts.Metadata != nil {
		create.Metadata = *opts.Metadata
	}
	if opts.Provenance != nil {
		create.Provenance = *opts.Provenance
	}
	if opts.ScanStatus != nil {
		create.ScanStatus = *opts.ScanStatus
	}

	item, err := insertContent(ctx, tx, create)
	if err != nil {
		return nil, err
	}
	if err := s.insertContentVersion(ctx, tx, &item, 1, version.Content, opts.Actor, item.Metadata); err != nil {
		return nil, err
	}
	stagedObjectKey := s.stagedProjectContentObjectKey(item.ID, 1, item.ContentHash)
	defer s.cleanupStagedProjectContentObject(&stagedObjectKey)
	if err := insertContentAudit(ctx, tx, item.ID, "duplicate", opts.Actor, map[string]any{"source_content_id": id.String(), "source_version": source.CurrentVersion}); err != nil {
		return nil, err
	}
	stagedObjectKey = ""
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing project content duplication: %w", err)
	}
	stagedObjectKey = ""
	return &item, nil
}

func (s *Store) RestoreContent(ctx context.Context, id uuid.UUID, version int, opts store.RestoreContentOptions) (*store.ProjectContent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning project content restoration: %w", err)
	}
	defer tx.Rollback(ctx)

	item, err := getContent(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if item.CurrentVersion != opts.ExpectedVersion {
		return nil, store.ErrContentConflict
	}
	restored, err := getContentVersion(ctx, tx, id, version)
	if err != nil {
		return nil, err
	}
	if err := s.loadProjectContentVersion(ctx, &restored, false); err != nil {
		return nil, err
	}
	item.CurrentVersion++
	updated, err := updateContent(ctx, tx, item)
	if err != nil {
		return nil, err
	}
	if err := s.insertContentVersion(ctx, tx, &updated, updated.CurrentVersion, restored.Content, opts.Actor, restored.Metadata); err != nil {
		return nil, err
	}
	stagedObjectKey := s.stagedProjectContentObjectKey(updated.ID, updated.CurrentVersion, updated.ContentHash)
	defer s.cleanupStagedProjectContentObject(&stagedObjectKey)
	if err := insertContentAudit(ctx, tx, id, "restore", opts.Actor, map[string]any{"version": updated.CurrentVersion, "restored_version": restored.Version}); err != nil {
		return nil, err
	}
	stagedObjectKey = ""
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing project content restoration: %w", err)
	}
	stagedObjectKey = ""
	return &updated, nil
}

func (s *Store) SoftDeleteContent(ctx context.Context, id uuid.UUID, opts store.SoftDeleteContentOptions) error {
	if !opts.Confirmed {
		return store.ErrContentConfirmationRequired
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning project content deletion: %w", err)
	}
	defer tx.Rollback(ctx)

	item, err := getContent(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if item.CurrentVersion != opts.ExpectedVersion {
		return store.ErrContentConflict
	}
	var objectKeys []string
	if item.DeletedAt == nil {
		rows, queryErr := tx.Query(ctx, `SELECT object_key FROM project_content_versions WHERE content_id = $1 AND object_key <> ''`, id)
		if queryErr != nil {
			return fmt.Errorf("listing project content objects for deletion: %w", queryErr)
		}
		for rows.Next() {
			var key string
			if scanErr := rows.Scan(&key); scanErr != nil {
				rows.Close()
				return fmt.Errorf("reading project content object for deletion: %w", scanErr)
			}
			objectKeys = append(objectKeys, key)
		}
		rows.Close()
		if rows.Err() != nil {
			return fmt.Errorf("listing project content objects for deletion: %w", rows.Err())
		}
		if len(objectKeys) > 0 && s.contentBlobs == nil {
			return fmt.Errorf("cannot delete S3-backed project content without a configured blob store")
		}
		for _, key := range objectKeys {
			if _, err := tx.Exec(ctx, `INSERT INTO project_content_blob_deletions (object_key) VALUES ($1)
				ON CONFLICT (object_key) DO NOTHING`, key); err != nil {
				return fmt.Errorf("recording project content object deletion %q: %w", key, err)
			}
		}
	}
	if item.DeletedAt == nil {
		if _, err := tx.Exec(ctx, `UPDATE project_content SET deleted_at = now(), updated_at = now() WHERE id = $1`, id); err != nil {
			return fmt.Errorf("soft deleting project content: %w", err)
		}
		// Deletion is confirmed as permanent, so purge the version bytes to
		// recover the project storage quota. The item row survives for
		// auditing and include_deleted listings.
		if _, err := tx.Exec(ctx, `DELETE FROM project_content_versions WHERE content_id = $1`, id); err != nil {
			return fmt.Errorf("purging project content versions: %w", err)
		}
		if err := insertContentAudit(ctx, tx, id, "soft_delete", opts.Actor, map[string]any{"version": item.CurrentVersion, "versions_purged": true}); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing project content deletion: %w", err)
	}
	for _, key := range objectKeys {
		if err := s.deletePendingProjectContentBlob(ctx, key); err != nil {
			// The durable outbox retains the key for the background reconciler.
			log.Printf("WARN: project content object %q remains pending deletion: %v", key, err)
		}
	}
	return nil
}

// ReconcileProjectContentBlobDeletions retries durable S3 deletion outbox
// entries. It is safe to call concurrently; object deletion is idempotent.
func (s *Store) ReconcileProjectContentBlobDeletions(ctx context.Context, limit int) error {
	if s.contentBlobs == nil {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT object_key FROM project_content_blob_deletions
		WHERE next_attempt_at <= now() ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return fmt.Errorf("listing pending project content blob deletions: %w", err)
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return fmt.Errorf("reading pending project content blob deletion: %w", err)
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("listing pending project content blob deletions: %w", err)
	}
	for _, key := range keys {
		if err := s.deletePendingProjectContentBlob(ctx, key); err != nil {
			log.Printf("WARN: retrying project content object deletion %q: %v", key, err)
		}
	}
	return nil
}

func (s *Store) deletePendingProjectContentBlob(ctx context.Context, key string) error {
	if s.contentBlobs == nil {
		return fmt.Errorf("project content blob store is not configured")
	}
	if err := s.contentBlobs.Delete(ctx, key); err != nil {
		_, recordErr := s.pool.Exec(ctx, `UPDATE project_content_blob_deletions
			SET attempts = attempts + 1, last_error = $2,
				next_attempt_at = now() + LEAST(attempts + 1, 60) * interval '1 minute'
			WHERE object_key = $1`, key, err.Error())
		if recordErr != nil {
			return fmt.Errorf("deleting S3 object: %v (also failed to record retry: %w)", err, recordErr)
		}
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM project_content_blob_deletions WHERE object_key = $1`, key); err != nil {
		return fmt.Errorf("clearing completed project content blob deletion %q: %w", key, err)
	}
	return nil
}

func (s *Store) RecordContentAudit(ctx context.Context, id uuid.UUID, action, actor string, details json.RawMessage) error {
	if len(details) == 0 {
		details = json.RawMessage(`{}`)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO project_content_audits (content_id, action, actor, details) VALUES ($1, $2, $3, $4)`, id, action, actor, details); err != nil {
		return fmt.Errorf("recording project content access: %w", err)
	}
	return nil
}

func (s *Store) ListContentVersions(ctx context.Context, id uuid.UUID) ([]store.ProjectContentVersion, error) {
	rows, err := s.pool.Query(ctx, `SELECT content_id, version, ''::bytea, object_key, sha256, size, creator, metadata, created_at
		FROM project_content_versions WHERE content_id = $1 ORDER BY version DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("listing project content versions: %w", err)
	}
	defer rows.Close()

	versions := make([]store.ProjectContentVersion, 0)
	for rows.Next() {
		version, err := scanContentVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing project content versions: %w", err)
	}
	if len(versions) == 0 {
		return nil, store.ErrContentNotFound
	}
	return versions, nil
}

type contentQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type contentExecutor interface {
	contentQuerier
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func getContent(ctx context.Context, q contentQuerier, id uuid.UUID, forUpdate bool) (store.ProjectContent, error) {
	query := contentSelect + ` WHERE c.id = $1`
	if forUpdate {
		query += ` FOR UPDATE OF c`
	}
	item, err := scanContent(q.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ProjectContent{}, store.ErrContentNotFound
	}
	if err != nil {
		return store.ProjectContent{}, fmt.Errorf("getting project content: %w", err)
	}
	return item, nil
}

func getContentVersion(ctx context.Context, q contentQuerier, id uuid.UUID, version int) (store.ProjectContentVersion, error) {
	item, err := scanContentVersion(q.QueryRow(ctx, `SELECT content_id, version, content, object_key, sha256, size, creator, metadata, created_at
		FROM project_content_versions WHERE content_id = $1 AND version = $2`, id, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ProjectContentVersion{}, store.ErrContentNotFound
	}
	if err != nil {
		return store.ProjectContentVersion{}, fmt.Errorf("getting project content version: %w", err)
	}
	return item, nil
}

func insertContent(ctx context.Context, tx contentExecutor, opts store.CreateContentOptions) (store.ProjectContent, error) {
	item, err := scanContentRow(tx.QueryRow(ctx, `INSERT INTO project_content
		(project_namespace, project_name, kind, path, media_type, metadata, provenance, scan_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+contentRowColumns,
		opts.ProjectNamespace, opts.ProjectName, opts.Kind, opts.Path, opts.MediaType,
		jsonOrEmpty(opts.Metadata), jsonOrEmpty(opts.Provenance), opts.ScanStatus))
	if isContentPathConflict(err) {
		return store.ProjectContent{}, store.ErrContentConflict
	}
	if err != nil {
		return store.ProjectContent{}, fmt.Errorf("creating project content: %w", err)
	}
	return item, nil
}

func updateContent(ctx context.Context, tx contentExecutor, item store.ProjectContent) (store.ProjectContent, error) {
	updated, err := scanContentRow(tx.QueryRow(ctx, `UPDATE project_content
		SET path = $2, media_type = $3, current_version = $4, metadata = $5, provenance = $6, scan_status = $7, updated_at = now()
		WHERE id = $1 RETURNING `+contentRowColumns,
		item.ID, item.Path, item.MediaType, item.CurrentVersion, jsonOrEmpty(item.Metadata), jsonOrEmpty(item.Provenance), item.ScanStatus))
	if isContentPathConflict(err) {
		return store.ProjectContent{}, store.ErrContentConflict
	}
	if err != nil {
		return store.ProjectContent{}, fmt.Errorf("updating project content: %w", err)
	}
	// Carry over current-version metadata; callers overwrite it when a new
	// version is inserted in the same transaction.
	updated.SizeBytes, updated.ContentHash, updated.CreatedBy = item.SizeBytes, item.ContentHash, item.CreatedBy
	return updated, nil
}

func (s *Store) insertContentVersion(ctx context.Context, tx contentExecutor, item *store.ProjectContent, version int, content []byte, creator string, metadata json.RawMessage) error {
	if err := checkContentQuota(ctx, tx, item.ProjectNamespace, item.ProjectName, int64(len(content))); err != nil {
		return err
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	// Keep the BYTEA compatibility copy during the rolling S3 cutover. New
	// readers prefer object_key; old replicas and rollback binaries can still
	// serve the exact bytes until a later migration removes the compatibility
	// copy after the fleet and backfill have been verified.
	storedContent := content
	objectKey := ""
	if s.contentBlobs != nil {
		objectKey = projectContentObjectKey(item.ID, version, hash)
		if err := s.contentBlobs.Put(ctx, objectKey, content, item.MediaType); err != nil {
			return fmt.Errorf("storing project content version body: %w", err)
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO project_content_versions
		(content_id, version, content, object_key, sha256, size, creator, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		item.ID, version, storedContent, objectKey, hash, int64(len(content)), creator, jsonOrEmpty(metadata))
	if err != nil {
		if objectKey != "" {
			s.cleanupStagedProjectContentObject(&objectKey)
		}
		return fmt.Errorf("creating project content version: %w", err)
	}
	item.SizeBytes, item.ContentHash, item.CreatedBy = int64(len(content)), hash, creator
	return nil
}

func projectContentObjectKey(id uuid.UUID, version int, hash string) string {
	return fmt.Sprintf("project-content/v1/%s/%d-%s", id.String(), version, hash)
}

func (s *Store) stagedProjectContentObjectKey(id uuid.UUID, version int, hash string) string {
	if s.contentBlobs == nil {
		return ""
	}
	return projectContentObjectKey(id, version, hash)
}

func (s *Store) cleanupStagedProjectContentObject(key *string) {
	if key == nil || *key == "" || s.contentBlobs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.contentBlobs.Delete(ctx, *key); err != nil {
		if recordErr := s.recordProjectContentBlobDeletion(context.Background(), *key, err); recordErr != nil {
			log.Printf("WARN: cleaning up uncommitted project content object %q: %v (also failed to record retry: %v)", *key, err, recordErr)
		}
	}
}

func (s *Store) recordProjectContentBlobDeletion(ctx context.Context, key string, cause error) error {
	lastError := ""
	if cause != nil {
		lastError = cause.Error()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO project_content_blob_deletions (object_key, attempts, last_error)
		VALUES ($1, 1, $2)
		ON CONFLICT (object_key) DO UPDATE SET
			attempts = project_content_blob_deletions.attempts + 1,
			last_error = EXCLUDED.last_error,
			next_attempt_at = now() + interval '1 minute'`, key, lastError)
	return err
}

func (s *Store) loadProjectContentVersion(ctx context.Context, version *store.ProjectContentVersion, promoteLegacy bool) error {
	if version.ObjectKey == "" {
		if s.contentBlobs == nil || !promoteLegacy {
			return nil
		}
		key := projectContentObjectKey(version.ContentID, version.Version, version.SHA256)
		if err := s.contentBlobs.Put(ctx, key, version.Content, "application/octet-stream"); err != nil {
			// Legacy BYTEA content remains a valid, available fallback during an
			// S3 outage; promotion can be retried on the next read.
			log.Printf("WARN: lazy S3 promotion failed for project content %s version %d: %v", version.ContentID, version.Version, err)
			return nil
		}
		tag, err := s.pool.Exec(ctx, `UPDATE project_content_versions SET object_key = $3
			WHERE content_id = $1 AND version = $2 AND object_key = ''`, version.ContentID, version.Version, key)
		if err != nil {
			log.Printf("WARN: recording lazy S3 promotion for project content %s version %d: %v", version.ContentID, version.Version, err)
			return nil
		}
		if tag.RowsAffected() > 0 {
			version.ObjectKey = key
			return nil
		}
		// The row may have been deleted or concurrently promoted. Only remove
		// this upload after proving no surviving row references it.
		var recordedKey string
		queryErr := s.pool.QueryRow(ctx, `SELECT object_key FROM project_content_versions
			WHERE content_id = $1 AND version = $2`, version.ContentID, version.Version).Scan(&recordedKey)
		if (errors.Is(queryErr, pgx.ErrNoRows) || queryErr == nil && recordedKey != key) && s.contentBlobs != nil {
			if deleteErr := s.contentBlobs.Delete(ctx, key); deleteErr != nil {
				if recordErr := s.recordProjectContentBlobDeletion(context.Background(), key, deleteErr); recordErr != nil {
					log.Printf("WARN: cleaning unreferenced promoted project content object %q: %v (also failed to record retry: %v)", key, deleteErr, recordErr)
				}
			}
		}
		if queryErr == nil && recordedKey == key {
			version.ObjectKey = key
		}
		return nil
	}
	if s.contentBlobs == nil {
		return fmt.Errorf("project content body %q is in S3 but no blob store is configured", version.ObjectKey)
	}
	content, err := s.contentBlobs.Get(ctx, version.ObjectKey)
	if err != nil {
		return fmt.Errorf("loading project content version body: %w", err)
	}
	if int64(len(content)) != version.Size {
		return fmt.Errorf("project content body %q size mismatch: got %d, want %d", version.ObjectKey, len(content), version.Size)
	}
	digest := sha256.Sum256(content)
	if hash := hex.EncodeToString(digest[:]); hash != version.SHA256 {
		return fmt.Errorf("project content body %q hash mismatch", version.ObjectKey)
	}
	version.Content = content
	return nil
}

// checkContentQuota enforces the per-project cap on total stored bytes before
// a new version is written inside the caller's transaction.
func checkContentQuota(ctx context.Context, q contentQuerier, projectNamespace, projectName string, additional int64) error {
	var total int64
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(v.size), 0)
		FROM project_content_versions v
		JOIN project_content c ON c.id = v.content_id
		WHERE c.project_namespace = $1 AND c.project_name = $2 AND c.deleted_at IS NULL`, projectNamespace, projectName).Scan(&total)
	if err != nil {
		return fmt.Errorf("checking project content quota: %w", err)
	}
	if total+additional > store.MaxProjectContentTotalBytes {
		return store.ErrContentQuotaExceeded
	}
	return nil
}

func insertContentAudit(ctx context.Context, tx contentExecutor, id uuid.UUID, action, actor string, details any) error {
	data, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshalling project content audit details: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_content_audits (content_id, action, actor, details) VALUES ($1, $2, $3, $4)`, id, action, actor, data); err != nil {
		return fmt.Errorf("creating project content audit: %w", err)
	}
	return nil
}

func scanContent(row pgx.Row) (store.ProjectContent, error) {
	var item store.ProjectContent
	err := row.Scan(&item.ID, &item.ProjectNamespace, &item.ProjectName, &item.Kind, &item.Path, &item.MediaType,
		&item.CurrentVersion, &item.Metadata, &item.Provenance, &item.ScanStatus, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt,
		&item.SizeBytes, &item.ContentHash, &item.CreatedBy)
	if err != nil {
		return store.ProjectContent{}, err
	}
	return item, nil
}

func scanContentVersion(row pgx.Row) (store.ProjectContentVersion, error) {
	var item store.ProjectContentVersion
	err := row.Scan(&item.ContentID, &item.Version, &item.Content, &item.ObjectKey, &item.SHA256, &item.Size, &item.Creator, &item.Metadata, &item.CreatedAt)
	if err != nil {
		return store.ProjectContentVersion{}, err
	}
	return item, nil
}

func jsonOrEmpty(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func isContentPathConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

var _ store.ProjectContentStore = (*Store)(nil)

// scanContentRow scans a bare project_content row (no version-metadata join).
func scanContentRow(row pgx.Row) (store.ProjectContent, error) {
	var item store.ProjectContent
	err := row.Scan(&item.ID, &item.ProjectNamespace, &item.ProjectName, &item.Kind, &item.Path, &item.MediaType,
		&item.CurrentVersion, &item.Metadata, &item.Provenance, &item.ScanStatus, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt)
	if err != nil {
		return store.ProjectContent{}, err
	}
	return item, nil
}
