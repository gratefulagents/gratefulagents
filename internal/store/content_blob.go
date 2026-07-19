package store

import (
	"context"
	"errors"
)

var ErrContentBlobNotFound = errors.New("project content blob not found")

// MaxProjectContentVersionBytes is the largest individual project-content
// version accepted by the direct upload API and read back from object storage.
const MaxProjectContentVersionBytes = 25 << 20 // 25 MiB

// ProjectContentBlobStore persists immutable project-content version bytes.
// Metadata, version history, quotas, and audit records remain in PostgreSQL.
type ProjectContentBlobStore interface {
	Put(ctx context.Context, key string, content []byte, mediaType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}
