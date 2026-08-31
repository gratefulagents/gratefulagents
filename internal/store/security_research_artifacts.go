package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	SecurityTaskHandoffVersion = 1

	SecurityResearchArtifactManifest       = "manifest"
	SecurityResearchArtifactTrace          = "trace"
	SecurityResearchArtifactHarnessSummary = "harness_summary"
	SecurityResearchArtifactBlocker        = "blocker"

	MaxSecurityResearchArtifactPayloadBytes = 256 << 10
	MaxSecurityResearchArtifactReferences   = 64
	MaxSecurityResearchArtifactConditions   = 16
	MaxSecurityResearchConditionBytes       = 4 << 10
	MaxSecurityResearchArtifactsPerList     = 20
	MaxSecurityTaskHandoffReferences        = 64
	MaxSecurityTaskHandoffBytes             = 8 << 10
)

var (
	ErrSecurityResearchArtifactNotFound          = errors.New("security research artifact not found")
	ErrSecurityResearchArtifactReferenceNotFound = errors.New("security research artifact reference not found in trusted scope")
)

// SecurityTaskHandoff is the compact value passed between security workflow tasks.
type SecurityTaskHandoff struct {
	Version               int32                      `json:"version"`
	ArtifactIDs           []uuid.UUID                `json:"artifact_ids,omitempty"`
	CandidateFingerprints []string                   `json:"candidate_fingerprints,omitempty"`
	CoverageIDs           map[string][]uuid.UUID     `json:"coverage_ids,omitempty"`
	BlockerIDs            []uuid.UUID                `json:"blocker_ids,omitempty"`
	Conditions            map[string]json.RawMessage `json:"conditions,omitempty"`
}

// SecurityResearchArtifact is immutable evidence produced by one task in one execution.
type SecurityResearchArtifact struct {
	ID                    uuid.UUID                  `json:"id"`
	RevisionID            uuid.UUID                  `json:"revision_id"`
	ExecutionID           string                     `json:"execution_id"`
	TaskName              string                     `json:"task_name"`
	Kind                  string                     `json:"kind"`
	SchemaVersion         int32                      `json:"schema_version"`
	Payload               json.RawMessage            `json:"payload,omitempty"`
	CandidateFingerprints []string                   `json:"candidate_fingerprints,omitempty"`
	CoverageIDs           map[string][]uuid.UUID     `json:"coverage_ids,omitempty"`
	BlockerIDs            []uuid.UUID                `json:"blocker_ids,omitempty"`
	Conditions            map[string]json.RawMessage `json:"conditions,omitempty"`
	Actor                 string                     `json:"actor"`
	IdempotencyKey        string                     `json:"-"`
	CreatedAt             time.Time                  `json:"created_at"`
}

type SecurityResearchArtifactFilter struct {
	RevisionID     uuid.UUID
	ExecutionID    string
	IDs            []uuid.UUID
	Kinds          []string
	TaskNames      []string
	Limit          int32
	IncludePayload bool
}

// SecurityResearchArtifactStore is separate from SecurityResearchStore so existing
// research consumers do not need to implement an artifact capability they do not use.
type SecurityResearchArtifactStore interface {
	CreateSecurityResearchArtifact(context.Context, string, *SecurityResearchArtifact) (*SecurityResearchArtifact, bool, error)
	GetSecurityResearchArtifact(context.Context, string, uuid.UUID, string, uuid.UUID) (*SecurityResearchArtifact, error)
	ListSecurityResearchArtifacts(context.Context, string, SecurityResearchArtifactFilter) ([]SecurityResearchArtifact, error)
}

func ValidSecurityResearchArtifactKind(kind string) bool {
	switch kind {
	case SecurityResearchArtifactManifest, SecurityResearchArtifactTrace, SecurityResearchArtifactHarnessSummary, SecurityResearchArtifactBlocker:
		return true
	default:
		return false
	}
}

func ValidateSecurityTaskHandoff(value SecurityTaskHandoff) error {
	if value.Version != SecurityTaskHandoffVersion {
		return fmt.Errorf("handoff version must be %d", SecurityTaskHandoffVersion)
	}
	references := len(value.ArtifactIDs) + len(value.CandidateFingerprints) + len(value.BlockerIDs)
	seenArtifacts := make(map[uuid.UUID]struct{}, len(value.ArtifactIDs))
	for _, id := range value.ArtifactIDs {
		if id == uuid.Nil {
			return errors.New("artifact IDs must be UUIDs")
		}
		if _, exists := seenArtifacts[id]; exists {
			return errors.New("artifact IDs must be unique")
		}
		seenArtifacts[id] = struct{}{}
	}
	seenCandidates := make(map[string]struct{}, len(value.CandidateFingerprints))
	for _, fingerprint := range value.CandidateFingerprints {
		fingerprint = strings.TrimSpace(fingerprint)
		if fingerprint == "" || len(fingerprint) > 256 {
			return errors.New("candidate fingerprints must be non-empty and at most 256 bytes")
		}
		if _, exists := seenCandidates[fingerprint]; exists {
			return errors.New("candidate fingerprints must be unique")
		}
		seenCandidates[fingerprint] = struct{}{}
	}
	seenCoverage := map[uuid.UUID]struct{}{}
	for verdict, ids := range value.CoverageIDs {
		if !ValidSecurityCoverageVerdict(verdict) {
			return fmt.Errorf("invalid coverage verdict %q", verdict)
		}
		references += len(ids)
		for _, id := range ids {
			if id == uuid.Nil {
				return errors.New("coverage IDs must be UUIDs")
			}
			if _, exists := seenCoverage[id]; exists {
				return errors.New("coverage IDs must be unique across verdicts")
			}
			seenCoverage[id] = struct{}{}
		}
	}
	seenBlockers := map[uuid.UUID]struct{}{}
	for _, id := range value.BlockerIDs {
		if id == uuid.Nil {
			return errors.New("blocker IDs must be UUIDs")
		}
		if _, exists := seenBlockers[id]; exists {
			return errors.New("blocker IDs must be unique")
		}
		seenBlockers[id] = struct{}{}
	}
	if references > MaxSecurityTaskHandoffReferences {
		return fmt.Errorf("handoff references must not exceed %d", MaxSecurityTaskHandoffReferences)
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaxSecurityTaskHandoffBytes {
		return fmt.Errorf("handoff must not exceed %d bytes", MaxSecurityTaskHandoffBytes)
	}
	return nil
}

//nolint:gocyclo // Keeping the complete artifact validation contract in one function makes its bounds auditable.
func ValidateSecurityResearchArtifact(value *SecurityResearchArtifact) error {
	if value == nil {
		return errors.New("security research artifact is required")
	}
	if value.RevisionID == uuid.Nil || strings.TrimSpace(value.ExecutionID) == "" || strings.TrimSpace(value.TaskName) == "" || strings.TrimSpace(value.Actor) == "" {
		return errors.New("revision, execution, task, and actor are required")
	}
	if len(value.ExecutionID) > 256 || len(value.TaskName) > 256 || len(value.Actor) > 256 {
		return errors.New("execution, task, and actor must not exceed 256 bytes")
	}
	if !ValidSecurityResearchArtifactKind(strings.TrimSpace(value.Kind)) {
		return fmt.Errorf("invalid security research artifact kind %q", value.Kind)
	}
	if value.SchemaVersion != SecurityTaskHandoffVersion {
		return fmt.Errorf("schema_version must be %d", SecurityTaskHandoffVersion)
	}
	if key := strings.TrimSpace(value.IdempotencyKey); key == "" || len(key) > 256 {
		return errors.New("idempotency_key is required and must not exceed 256 bytes")
	}
	if len(value.Payload) == 0 || !json.Valid(value.Payload) {
		return errors.New("payload must be a valid JSON object")
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(value.Payload, &payload) != nil || payload == nil {
		return errors.New("payload must be a valid JSON object")
	}
	compactPayload, err := json.Marshal(payload)
	if err != nil || len(compactPayload) > MaxSecurityResearchArtifactPayloadBytes {
		return fmt.Errorf("payload must not exceed %d bytes", MaxSecurityResearchArtifactPayloadBytes)
	}

	references := len(value.CandidateFingerprints) + len(value.BlockerIDs)
	seenFingerprints := make(map[string]struct{}, len(value.CandidateFingerprints))
	for _, fingerprint := range value.CandidateFingerprints {
		if fingerprint = strings.TrimSpace(fingerprint); fingerprint == "" || len(fingerprint) > 256 {
			return errors.New("candidate fingerprints must be non-empty and at most 256 bytes")
		}
		if _, exists := seenFingerprints[fingerprint]; exists {
			return errors.New("candidate fingerprints must be unique")
		}
		seenFingerprints[fingerprint] = struct{}{}
	}
	seenCoverage := make(map[uuid.UUID]struct{})
	for verdict, ids := range value.CoverageIDs {
		if !ValidSecurityCoverageVerdict(verdict) {
			return fmt.Errorf("invalid coverage verdict %q", verdict)
		}
		references += len(ids)
		for _, id := range ids {
			if id == uuid.Nil {
				return errors.New("coverage IDs must be UUIDs")
			}
			if _, exists := seenCoverage[id]; exists {
				return errors.New("coverage IDs must be unique across verdicts")
			}
			seenCoverage[id] = struct{}{}
		}
	}
	seenBlockers := make(map[uuid.UUID]struct{}, len(value.BlockerIDs))
	for _, id := range value.BlockerIDs {
		if id == uuid.Nil {
			return errors.New("blocker IDs must be UUIDs")
		}
		if _, exists := seenBlockers[id]; exists {
			return errors.New("blocker IDs must be unique")
		}
		seenBlockers[id] = struct{}{}
	}
	if references > MaxSecurityResearchArtifactReferences {
		return fmt.Errorf("handoff references must not exceed %d", MaxSecurityResearchArtifactReferences)
	}
	if len(value.Conditions) > MaxSecurityResearchArtifactConditions {
		return fmt.Errorf("conditions must not exceed %d entries", MaxSecurityResearchArtifactConditions)
	}
	conditionsJSON, err := json.Marshal(value.Conditions)
	if err != nil || len(conditionsJSON) > MaxSecurityResearchConditionBytes {
		return fmt.Errorf("conditions must not exceed %d bytes", MaxSecurityResearchConditionBytes)
	}
	for key, raw := range value.Conditions {
		if strings.TrimSpace(key) == "" || len(key) > 64 {
			return errors.New("condition keys must be non-empty and at most 64 bytes")
		}
		var scalar any
		if len(raw) == 0 || json.Unmarshal(raw, &scalar) != nil {
			return fmt.Errorf("condition %q must be a JSON scalar", key)
		}
		switch typed := scalar.(type) {
		case nil, bool, float64:
		case string:
			if !utf8.ValidString(typed) || len(typed) > 256 {
				return fmt.Errorf("condition %q string must not exceed 256 bytes", key)
			}
		default:
			return fmt.Errorf("condition %q must be a JSON scalar", key)
		}
	}
	return nil
}
