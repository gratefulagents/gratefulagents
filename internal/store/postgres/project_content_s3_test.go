package postgres_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/store"
	pgstore "github.com/gratefulagents/gratefulagents/internal/store/postgres"
)

type memoryContentBlobs struct {
	mu         sync.Mutex
	objects    map[string][]byte
	failDelete bool
}

func (m *memoryContentBlobs) Put(_ context.Context, key string, content []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = append([]byte(nil), content...)
	return nil
}
func (m *memoryContentBlobs) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	content, ok := m.objects[key]
	if !ok {
		return nil, store.ErrContentBlobNotFound
	}
	return append([]byte(nil), content...), nil
}
func (m *memoryContentBlobs) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failDelete {
		return fmt.Errorf("injected delete failure")
	}
	delete(m.objects, key)
	return nil
}

func TestProjectContentVersionBodiesUseBlobStore(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM project_content_blob_deletions; DELETE FROM project_content"); err != nil {
		t.Fatal(err)
	}

	blobs := &memoryContentBlobs{objects: map[string][]byte{}}
	contentStore := pgstore.NewFromPool(pool)
	contentStore.SetProjectContentBlobStore(blobs)
	item, err := contentStore.CreateContent(ctx, store.CreateContentOptions{
		ProjectNamespace: "team",
		ProjectName:      "briefs",
		Kind:             store.ProjectContentKindFile,
		Path:             "image.png",
		MediaType:        "image/png",
		Content:          []byte("image bytes"),
		ScanStatus:       store.ScanStatusClean,
		Actor:            "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	var databaseBytes int
	var objectKey string
	if err := pool.QueryRow(ctx, `SELECT octet_length(content), object_key FROM project_content_versions WHERE content_id = $1 AND version = 1`, item.ID).Scan(&databaseBytes, &objectKey); err != nil {
		t.Fatal(err)
	}
	if databaseBytes != len("image bytes") || objectKey == "" {
		t.Fatalf("database compatibility bytes = %d, object key = %q", databaseBytes, objectKey)
	}
	version, err := contentStore.GetContentVersion(ctx, item.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(version.Content); got != "image bytes" {
		t.Fatalf("version content = %q", got)
	}
	if version.ObjectKey != objectKey {
		t.Fatalf("version object key = %q, want %q", version.ObjectKey, objectKey)
	}
	blobs.failDelete = true
	if err := contentStore.SoftDeleteContent(ctx, item.ID, store.SoftDeleteContentOptions{ExpectedVersion: 1, Confirmed: true, Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_content_blob_deletions WHERE object_key = $1`, objectKey).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending deletion count = %d, want 1", pending)
	}
	blobs.failDelete = false
	if _, err := pool.Exec(ctx, `UPDATE project_content_blob_deletions SET next_attempt_at = now()`); err != nil {
		t.Fatal(err)
	}
	if err := contentStore.ReconcileProjectContentBlobDeletions(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Get(ctx, objectKey); err == nil {
		t.Fatal(fmt.Errorf("deleted content object %q still exists", objectKey))
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_content_blob_deletions WHERE object_key = $1`, objectKey).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending deletion count after reconcile = %d, want 0", pending)
	}
}
