package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/gratefulagents/sdk/pkg/agentsdk"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

const maxSecurityResearchArtifactResultBytes = 1 << 20

func registerSecurityResearchArtifactTools(registry *Registry, state *securityScanState) {
	if registry == nil || state == nil || state.researchStore == nil || state.researchArtifactStore == nil {
		return
	}
	registry.Register(&createSecurityResearchArtifactTool{state: state})
	registry.Register(&getSecurityResearchArtifactsTool{state: state})
}

func (s *securityScanState) securityResearchArtifactScope() (string, string, error) {
	executionID := strings.TrimSpace(s.scanCtx.ExecutionID)
	if executionID == "" {
		executionID = strings.TrimSpace(s.scanCtx.RunName)
	}
	taskName := strings.TrimSpace(s.scanCtx.TaskName)
	if taskName == "" {
		taskName = strings.TrimSpace(s.scanCtx.RunName)
	}
	if executionID == "" || taskName == "" {
		return "", "", fmt.Errorf("trusted run context must provide execution and task identity")
	}
	return executionID, taskName, nil
}

type createSecurityResearchArtifactInput struct {
	Kind                  string                     `json:"kind"`
	Payload               json.RawMessage            `json:"payload"`
	CandidateFingerprints []string                   `json:"candidate_fingerprints"`
	CoverageIDs           map[string][]string        `json:"coverage_ids"`
	BlockerIDs            []string                   `json:"blocker_ids"`
	Conditions            map[string]json.RawMessage `json:"conditions"`
	IdempotencyKey        string                     `json:"idempotency_key"`
}

type createSecurityResearchArtifactTool struct{ state *securityScanState }

func (t *createSecurityResearchArtifactTool) Name() string {
	return "create_security_research_artifact"
}
func (t *createSecurityResearchArtifactTool) Description() string {
	return "Persist one immutable, bounded manifest, trace, harness summary, or blocker for this run's trusted revision, execution, and task. Returns a compact version-1 handoff; finding references are existing fingerprints, coverage references are existing row IDs grouped by verdict, and blocker references are existing blocker artifact IDs."
}
func (t *createSecurityResearchArtifactTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["manifest","trace","harness_summary","blocker"]},"payload":{"type":"object","description":"Artifact body; serialized JSON is limited to 262144 bytes."},"candidate_fingerprints":{"type":"array","maxItems":64,"items":{"type":"string","minLength":1,"maxLength":256}},"coverage_ids":{"type":"object","properties":{"disproved":{"type":"array","items":{"type":"string"}},"adequately_tested":{"type":"array","items":{"type":"string"}},"inadequately_tested":{"type":"array","items":{"type":"string"}},"not_tested":{"type":"array","items":{"type":"string"}}},"additionalProperties":false},"blocker_ids":{"type":"array","maxItems":64,"items":{"type":"string"}},"conditions":{"type":"object","maxProperties":16,"description":"Small JSON scalar conditions only."},"idempotency_key":{"type":"string","minLength":1,"maxLength":256}},"required":["kind","payload","idempotency_key"],"additionalProperties":false}`)
}
func (t *createSecurityResearchArtifactTool) IsReadOnly() bool                      { return true }
func (t *createSecurityResearchArtifactTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *createSecurityResearchArtifactTool) NeedsApproval() bool                   { return false }
func (t *createSecurityResearchArtifactTool) TimeoutSeconds() int                   { return 0 }
func (t *createSecurityResearchArtifactTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	if len(input) > store.MaxSecurityResearchArtifactPayloadBytes+(32<<10) {
		return securityResearchFailure(fmt.Errorf("artifact input exceeds the bounded request size"))
	}
	var in createSecurityResearchArtifactInput
	if err := json.Unmarshal(input, &in); err != nil {
		return securityResearchFailure(fmt.Errorf("invalid input: %w", err))
	}
	coverageIDs, err := parseSecurityResearchArtifactCoverageIDs(in.CoverageIDs)
	if err != nil {
		return securityResearchFailure(err)
	}
	blockerIDs, err := parseSecurityResearchArtifactIDs(in.BlockerIDs, "blocker_ids")
	if err != nil {
		return securityResearchFailure(err)
	}
	for i := range in.CandidateFingerprints {
		in.CandidateFingerprints[i] = strings.TrimSpace(in.CandidateFingerprints[i])
	}
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return securityResearchFailure(err)
	}
	executionID, taskName, err := t.state.securityResearchArtifactScope()
	if err != nil {
		return securityResearchFailure(err)
	}
	actor, err := t.state.securityResearchActor()
	if err != nil {
		return securityResearchFailure(err)
	}
	value := &store.SecurityResearchArtifact{
		RevisionID:            bound.revision.ID,
		ExecutionID:           executionID,
		TaskName:              taskName,
		Kind:                  strings.TrimSpace(in.Kind),
		SchemaVersion:         store.SecurityTaskHandoffVersion,
		Payload:               in.Payload,
		CandidateFingerprints: in.CandidateFingerprints,
		CoverageIDs:           coverageIDs,
		BlockerIDs:            blockerIDs,
		Conditions:            in.Conditions,
		Actor:                 actor,
		IdempotencyKey:        strings.TrimSpace(in.IdempotencyKey),
	}
	if err := store.ValidateSecurityResearchArtifact(value); err != nil {
		return securityResearchFailure(err)
	}
	maxArtifactReferences := store.MaxSecurityTaskHandoffReferences - 1 // every handoff adds the artifact ID
	if value.Kind == store.SecurityResearchArtifactBlocker {
		maxArtifactReferences-- // blocker handoffs also classify their own artifact ID
	}
	if count := securityResearchArtifactReferenceCount(value); count > maxArtifactReferences {
		return securityResearchFailure(fmt.Errorf("artifact has %d references; kind %q allows at most %d so its handoff stays bounded", count, value.Kind, maxArtifactReferences))
	}
	placeholderID := uuid.New()
	prospectiveBlockers := append([]uuid.UUID(nil), value.BlockerIDs...)
	if value.Kind == store.SecurityResearchArtifactBlocker {
		prospectiveBlockers = append(prospectiveBlockers, placeholderID)
	}
	if err := store.ValidateSecurityTaskHandoff(store.SecurityTaskHandoff{
		Version: store.SecurityTaskHandoffVersion, ArtifactIDs: []uuid.UUID{placeholderID},
		CandidateFingerprints: value.CandidateFingerprints, CoverageIDs: value.CoverageIDs,
		BlockerIDs: prospectiveBlockers, Conditions: value.Conditions,
	}); err != nil {
		return securityResearchFailure(err)
	}
	stored, created, err := t.state.researchArtifactStore.CreateSecurityResearchArtifact(ctx, t.state.scanCtx.Namespace, value)
	if err != nil {
		return securityResearchFailure(err)
	}
	blockerHandoffIDs := append([]uuid.UUID(nil), stored.BlockerIDs...)
	if stored.Kind == store.SecurityResearchArtifactBlocker {
		blockerHandoffIDs = append(blockerHandoffIDs, stored.ID)
	}
	handoff := store.SecurityTaskHandoff{
		Version:               store.SecurityTaskHandoffVersion,
		ArtifactIDs:           []uuid.UUID{stored.ID},
		CandidateFingerprints: stored.CandidateFingerprints,
		CoverageIDs:           stored.CoverageIDs,
		BlockerIDs:            blockerHandoffIDs,
		Conditions:            stored.Conditions,
	}
	if err := store.ValidateSecurityTaskHandoff(handoff); err != nil {
		return securityResearchFailure(err)
	}
	return securityResearchResult(map[string]any{
		"created": created,
		"artifact": map[string]any{
			"id": stored.ID, "kind": stored.Kind, "schema_version": stored.SchemaVersion,
			"task_name": stored.TaskName, "created_at": stored.CreatedAt,
		},
		"handoff": handoff,
	})
}

func securityResearchArtifactReferenceCount(value *store.SecurityResearchArtifact) int {
	count := len(value.CandidateFingerprints) + len(value.BlockerIDs)
	for _, ids := range value.CoverageIDs {
		count += len(ids)
	}
	return count
}

func parseSecurityResearchArtifactCoverageIDs(values map[string][]string) (map[string][]uuid.UUID, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string][]uuid.UUID, len(values))
	for verdict, rawIDs := range values {
		if !store.ValidSecurityCoverageVerdict(verdict) {
			return nil, fmt.Errorf("invalid coverage verdict %q", verdict)
		}
		ids, err := parseSecurityResearchArtifactIDs(rawIDs, "coverage_ids."+verdict)
		if err != nil {
			return nil, err
		}
		result[verdict] = ids
	}
	return result, nil
}

func parseSecurityResearchArtifactIDs(values []string, field string) ([]uuid.UUID, error) {
	result := make([]uuid.UUID, len(values))
	for i, value := range values {
		id, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil || id == uuid.Nil {
			return nil, fmt.Errorf("%s entries must be UUIDs", field)
		}
		result[i] = id
	}
	return result, nil
}

type getSecurityResearchArtifactsInput struct {
	ArtifactIDs    []string `json:"artifact_ids"`
	Kinds          []string `json:"kinds"`
	TaskNames      []string `json:"task_names"`
	Limit          int32    `json:"limit"`
	IncludePayload bool     `json:"include_payload"`
}

type getSecurityResearchArtifactsTool struct{ state *securityScanState }

func (t *getSecurityResearchArtifactsTool) Name() string { return "get_security_research_artifacts" }
func (t *getSecurityResearchArtifactsTool) Description() string {
	return "List or get bounded research artifacts from this run's trusted exact revision and execution. Pass artifact_ids from a version-1 handoff for exact retrieval. Payloads are omitted unless include_payload is true; at most 20 artifacts and 1 MiB of serialized results are returned."
}
func (t *getSecurityResearchArtifactsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"artifact_ids":{"type":"array","maxItems":20,"items":{"type":"string"}},"kinds":{"type":"array","maxItems":20,"items":{"type":"string","enum":["manifest","trace","harness_summary","blocker"]}},"task_names":{"type":"array","maxItems":20,"items":{"type":"string","minLength":1,"maxLength":256}},"limit":{"type":"integer","minimum":1,"maximum":20,"default":20},"include_payload":{"type":"boolean","default":false}},"additionalProperties":false}`)
}
func (t *getSecurityResearchArtifactsTool) IsReadOnly() bool                      { return true }
func (t *getSecurityResearchArtifactsTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *getSecurityResearchArtifactsTool) NeedsApproval() bool                   { return false }
func (t *getSecurityResearchArtifactsTool) TimeoutSeconds() int                   { return 0 }
func (t *getSecurityResearchArtifactsTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in getSecurityResearchArtifactsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return securityResearchFailure(fmt.Errorf("invalid input: %w", err))
	}
	ids, err := parseSecurityResearchArtifactIDs(in.ArtifactIDs, "artifact_ids")
	if err != nil {
		return securityResearchFailure(err)
	}
	if len(ids) > store.MaxSecurityResearchArtifactsPerList || len(in.Kinds) > store.MaxSecurityResearchArtifactsPerList || len(in.TaskNames) > store.MaxSecurityResearchArtifactsPerList {
		return securityResearchFailure(fmt.Errorf("artifact filters must not exceed %d values", store.MaxSecurityResearchArtifactsPerList))
	}
	for i := range in.Kinds {
		in.Kinds[i] = strings.TrimSpace(in.Kinds[i])
		if !store.ValidSecurityResearchArtifactKind(in.Kinds[i]) {
			return securityResearchFailure(fmt.Errorf("invalid security research artifact kind %q", in.Kinds[i]))
		}
	}
	for i := range in.TaskNames {
		in.TaskNames[i] = strings.TrimSpace(in.TaskNames[i])
		if in.TaskNames[i] == "" || len(in.TaskNames[i]) > 256 {
			return securityResearchFailure(fmt.Errorf("task_names entries must be non-empty and at most 256 bytes"))
		}
	}
	if in.Limit == 0 {
		in.Limit = store.MaxSecurityResearchArtifactsPerList
	}
	if in.Limit < 1 || in.Limit > store.MaxSecurityResearchArtifactsPerList {
		return securityResearchFailure(fmt.Errorf("limit must be between 1 and %d", store.MaxSecurityResearchArtifactsPerList))
	}
	if len(ids) > int(in.Limit) {
		return securityResearchFailure(fmt.Errorf("limit must be at least the number of artifact_ids"))
	}
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return securityResearchFailure(err)
	}
	executionID, _, err := t.state.securityResearchArtifactScope()
	if err != nil {
		return securityResearchFailure(err)
	}
	values, err := t.state.researchArtifactStore.ListSecurityResearchArtifacts(ctx, t.state.scanCtx.Namespace, store.SecurityResearchArtifactFilter{
		RevisionID: bound.revision.ID, ExecutionID: executionID, IDs: ids, Kinds: in.Kinds,
		TaskNames: in.TaskNames, Limit: in.Limit, IncludePayload: in.IncludePayload,
	})
	if err != nil {
		return securityResearchFailure(err)
	}
	if len(ids) > 0 && len(values) != len(ids) {
		return securityResearchFailure(store.ErrSecurityResearchArtifactNotFound)
	}
	encoded, err := json.Marshal(map[string]any{"artifacts": values, "count": len(values), "limit": in.Limit})
	if err != nil {
		return securityResearchFailure(fmt.Errorf("encoding security research artifacts: %w", err))
	}
	if len(encoded) > maxSecurityResearchArtifactResultBytes {
		return securityResearchFailure(fmt.Errorf("artifact result exceeds %d bytes; request fewer artifacts or omit payloads", maxSecurityResearchArtifactResultBytes))
	}
	return Result{Content: string(encoded)}, nil
}
