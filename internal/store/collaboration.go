package store

import "time"

// ResourceOwnership tracks which user owns a given resource.
type ResourceOwnership struct {
	ID                string
	ResourceType      string // "agent_run" or "project"
	ResourceID        string
	ResourceNamespace string
	OwnerID           string
	CreatedAt         time.Time
}

// ResourceShare tracks explicit sharing of a resource with another user.
type ResourceShare struct {
	ID                string
	ResourceType      string
	ResourceID        string
	ResourceNamespace string
	SharedWithUserID  string
	SharedByUserID    string
	Permission        string // "viewer" or "collaborator"
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Notification represents an in-app notification for a user.
type Notification struct {
	ID                string
	UserID            string
	Type              string // "resource_shared", "share_updated", "share_revoked"
	Title             string
	Body              string
	ResourceType      string
	ResourceID        string
	ResourceNamespace string
	ActorID           string
	ActorName         string
	Read              bool
	CreatedAt         time.Time
}
