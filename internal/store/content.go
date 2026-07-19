package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrContentNotFound             = errors.New("project content not found")
	ErrContentConflict             = errors.New("project content conflict")
	ErrContentConfirmationRequired = errors.New("project content confirmation required")
	ErrContentQuotaExceeded        = errors.New("project content storage quota exceeded")
)

// MaxProjectContentTotalBytes caps the total stored bytes (across all items
// and versions) per project so runaway uploads cannot bloat the database.
const MaxProjectContentTotalBytes = 2 << 30 // 2 GiB

// Scan status values persisted on project content items.
const (
	ScanStatusPending  = "pending"
	ScanStatusClean    = "clean"
	ScanStatusRejected = "rejected"
)

type ProjectContentKind string

const (
	ProjectContentKindFile         ProjectContentKind = "file"
	ProjectContentKindFolder       ProjectContentKind = "folder"
	ProjectContentKindDocument     ProjectContentKind = "document"
	ProjectContentKindWorkbook     ProjectContentKind = "workbook"
	ProjectContentKindPresentation ProjectContentKind = "presentation"
	ProjectContentKindHTML         ProjectContentKind = "html"
)

type ProjectContent struct {
	ID               uuid.UUID
	ProjectNamespace string
	ProjectName      string
	Kind             ProjectContentKind
	Path             string
	MediaType        string
	CurrentVersion   int
	Metadata         json.RawMessage
	Provenance       json.RawMessage
	ScanStatus       string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
	// Current-version metadata, populated by the store via a join so callers
	// never need to load version bytes to display listings.
	SizeBytes   int64
	ContentHash string
	CreatedBy   string
}

type ProjectContentVersion struct {
	ContentID uuid.UUID
	Version   int
	Content   []byte
	ObjectKey string
	SHA256    string
	Size      int64
	Creator   string
	Metadata  json.RawMessage
	CreatedAt time.Time
}

type CreateContentOptions struct {
	ProjectNamespace string
	ProjectName      string
	Kind             ProjectContentKind
	Path             string
	MediaType        string
	Content          []byte
	Metadata         json.RawMessage
	Provenance       json.RawMessage
	ScanStatus       string
	Actor            string
}

type UpdateContentOptions struct {
	Path               *string
	MediaType          *string
	Content            *[]byte
	Metadata           *json.RawMessage
	Provenance         *json.RawMessage
	ScanStatus         *string
	ExpectedVersion    int
	OverwriteConfirmed bool
	Actor              string
}

type DuplicateContentOptions struct {
	ExpectedVersion  int
	ProjectNamespace string
	ProjectName      string
	Kind             *ProjectContentKind
	Path             string
	MediaType        *string
	Metadata         *json.RawMessage
	Provenance       *json.RawMessage
	ScanStatus       *string
	Actor            string
}

type RestoreContentOptions struct {
	ExpectedVersion int
	Actor           string
}

type SoftDeleteContentOptions struct {
	ExpectedVersion int
	Confirmed       bool
	Actor           string
}

// ProjectContentStore persists durable files and rich project artifacts.
// ListContent and ListContentVersions return metadata only; version bytes are
// loaded exclusively through GetContentVersion.
type ProjectContentStore interface {
	ListContent(ctx context.Context, projectNamespace, projectName string, includeDeleted bool) ([]ProjectContent, error)
	GetContent(ctx context.Context, id uuid.UUID) (*ProjectContent, error)
	GetContentVersion(ctx context.Context, id uuid.UUID, version int) (*ProjectContentVersion, error)
	CreateContent(ctx context.Context, opts CreateContentOptions) (*ProjectContent, error)
	UpdateContent(ctx context.Context, id uuid.UUID, opts UpdateContentOptions) (*ProjectContent, error)
	DuplicateContent(ctx context.Context, id uuid.UUID, opts DuplicateContentOptions) (*ProjectContent, error)
	RestoreContent(ctx context.Context, id uuid.UUID, version int, opts RestoreContentOptions) (*ProjectContent, error)
	// SoftDeleteContent hides the item and purges its stored version bytes so
	// the project's storage quota is recovered; the item row is retained for
	// auditing and include-deleted listings.
	SoftDeleteContent(ctx context.Context, id uuid.UUID, opts SoftDeleteContentOptions) error
	ListContentVersions(ctx context.Context, id uuid.UUID) ([]ProjectContentVersion, error)
	RecordContentAudit(ctx context.Context, id uuid.UUID, action, actor string, details json.RawMessage) error
}
