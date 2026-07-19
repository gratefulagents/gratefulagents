package sessionclient

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

func newTranscriptTestClient() (*Client, *metadataTestStore) {
	testStore := &metadataTestStore{session: &store.Session{ID: uuid.New()}}
	return &Client{store: testStore, sessionID: testStore.session.ID}, testStore
}

func TestSaveTranscriptBlobRoundTrip(t *testing.T) {
	ctx := context.Background()
	client, testStore := newTranscriptTestClient()

	if err := client.SaveTranscriptBlob(ctx, []byte("blob"), 7); err != nil {
		t.Fatalf("SaveTranscriptBlob: %v", err)
	}
	if testStore.transcriptItemCount != 7 {
		t.Fatalf("itemCount = %d, want 7", testStore.transcriptItemCount)
	}
	data, err := client.LoadTranscriptBlob(ctx)
	if err != nil {
		t.Fatalf("LoadTranscriptBlob: %v", err)
	}
	if string(data) != "blob" {
		t.Fatalf("loaded %q, want %q", data, "blob")
	}

	if err := client.ClearTranscriptBlob(ctx); err != nil {
		t.Fatalf("ClearTranscriptBlob: %v", err)
	}
	data, err = client.LoadTranscriptBlob(ctx)
	if err != nil {
		t.Fatalf("LoadTranscriptBlob after clear: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil after clear, got %q", data)
	}
}

func TestSaveTranscriptBlobEmptyDeletes(t *testing.T) {
	ctx := context.Background()
	client, testStore := newTranscriptTestClient()

	if err := client.SaveTranscriptBlob(ctx, []byte("blob"), 1); err != nil {
		t.Fatalf("SaveTranscriptBlob: %v", err)
	}
	if err := client.SaveTranscriptBlob(ctx, nil, 0); err != nil {
		t.Fatalf("SaveTranscriptBlob(nil): %v", err)
	}
	if testStore.transcript != nil {
		t.Fatal("empty save should delete the stored blob")
	}
}
