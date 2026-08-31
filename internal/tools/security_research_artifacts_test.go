package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func (s *fakeSecurityResearchStore) CreateSecurityResearchArtifact(_ context.Context, namespace string, value *store.SecurityResearchArtifact) (*store.SecurityResearchArtifact, bool, error) {
	s.lastNamespace, s.lastActor = namespace, value.Actor
	key := value.RevisionID.String() + "|" + value.ExecutionID + "|" + value.TaskName + "|" + value.IdempotencyKey
	if existing := s.researchArtifacts[key]; existing != nil {
		copy := *existing
		return &copy, false, nil
	}
	if err := store.ValidateSecurityResearchArtifact(value); err != nil {
		return nil, false, err
	}
	stored := *value
	stored.ID = uuid.New()
	stored.CreatedAt = time.Now().UTC()
	s.researchArtifacts[key] = &stored
	copy := stored
	return &copy, true, nil
}

func (s *fakeSecurityResearchStore) GetSecurityResearchArtifact(_ context.Context, namespace string, revisionID uuid.UUID, executionID string, id uuid.UUID) (*store.SecurityResearchArtifact, error) {
	s.lastNamespace = namespace
	for _, value := range s.researchArtifacts {
		if value.RevisionID == revisionID && value.ExecutionID == executionID && value.ID == id {
			copy := *value
			return &copy, nil
		}
	}
	return nil, store.ErrSecurityResearchArtifactNotFound
}

func (s *fakeSecurityResearchStore) ListSecurityResearchArtifacts(_ context.Context, namespace string, filter store.SecurityResearchArtifactFilter) ([]store.SecurityResearchArtifact, error) {
	s.lastNamespace, s.lastArtifactFilter = namespace, filter
	var values []store.SecurityResearchArtifact
	for _, value := range s.researchArtifacts {
		if value.RevisionID != filter.RevisionID || value.ExecutionID != filter.ExecutionID || !artifactUUIDSelected(value.ID, filter.IDs) || !artifactStringSelected(value.Kind, filter.Kinds) || !artifactStringSelected(value.TaskName, filter.TaskNames) {
			continue
		}
		copy := *value
		if !filter.IncludePayload {
			copy.Payload = nil
		}
		values = append(values, copy)
		if int32(len(values)) == filter.Limit {
			break
		}
	}
	return values, nil
}

func artifactUUIDSelected(value uuid.UUID, selected []uuid.UUID) bool {
	if len(selected) == 0 {
		return true
	}
	for _, candidate := range selected {
		if candidate == value {
			return true
		}
	}
	return false
}

func artifactStringSelected(value string, selected []string) bool {
	if len(selected) == 0 {
		return true
	}
	for _, candidate := range selected {
		if candidate == value {
			return true
		}
	}
	return false
}

func TestSecurityResearchArtifactToolsUseTrustedScopeAndCompactHandoff(t *testing.T) {
	researchStore := newFakeSecurityResearchStore()
	scanCtx := bountyLaneContext("investigator-run", "", bountyLaneProgram())
	scanCtx.TaskName = "consensus-investigator"
	registry := researchToolRegistry(t, researchStore, scanCtx)
	for _, name := range []string{"create_security_research_artifact", "get_security_research_artifacts"} {
		if registry.Get(name) == nil || !registry.Get(name).IsReadOnly() {
			t.Fatalf("tool %q was not registered as a read-only run-state tool", name)
		}
	}

	coverageResult := execTool(t, registry, "record_security_coverage", `{"dimension":"invariant","subject_key":"supply","verdict":"adequately_tested","bounds":{},"evidence":[],"idempotency_key":"coverage-1"}`)
	if coverageResult.IsError {
		t.Fatalf("coverage: %s", coverageResult.Content)
	}
	var coverageID uuid.UUID
	for _, value := range researchStore.coverage {
		coverageID = value.ID
	}
	input := `{"kind":"harness_summary","payload":{"command":"go test ./...","result":"bounded negative"},"candidate_fingerprints":["candidate-fp"],"coverage_ids":{"adequately_tested":["` + coverageID.String() + `"]},"conditions":{"ready":true,"risk":"low"},"idempotency_key":"harness-1","namespace":"attacker","execution_id":"attacker","task_name":"attacker"}`
	first := execTool(t, registry, "create_security_research_artifact", input)
	second := execTool(t, registry, "create_security_research_artifact", input)
	if first.IsError || second.IsError || len(researchStore.researchArtifacts) != 1 {
		t.Fatalf("idempotent artifact create: first=%+v second=%+v count=%d", first, second, len(researchStore.researchArtifacts))
	}
	var artifact *store.SecurityResearchArtifact
	for _, value := range researchStore.researchArtifacts {
		artifact = value
	}
	if artifact.ExecutionID != scanCtx.ExecutionID || artifact.TaskName != scanCtx.TaskName || artifact.RevisionID != researchStore.revision.ID || artifact.Actor != scanCtx.RunName || researchStore.lastNamespace != scanCtx.Namespace {
		t.Fatalf("artifact trusted scope = %+v namespace=%q, want execution=%q task=%q revision=%s actor=%q namespace=%q", artifact, researchStore.lastNamespace, scanCtx.ExecutionID, scanCtx.TaskName, researchStore.revision.ID, scanCtx.RunName, scanCtx.Namespace)
	}
	var response struct {
		Created bool                      `json:"created"`
		Handoff store.SecurityTaskHandoff `json:"handoff"`
	}
	if err := json.Unmarshal([]byte(first.Content), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Created || response.Handoff.Version != store.SecurityTaskHandoffVersion || len(response.Handoff.ArtifactIDs) != 1 || response.Handoff.ArtifactIDs[0] != artifact.ID || response.Handoff.CoverageIDs[store.SecurityCoverageAdequatelyTested][0] != coverageID {
		t.Fatalf("compact handoff = %+v", response.Handoff)
	}
	if !strings.Contains(second.Content, `"created":false`) {
		t.Fatalf("idempotent replay did not disclose reuse: %s", second.Content)
	}

	withoutPayload := execTool(t, registry, "get_security_research_artifacts", `{"artifact_ids":["`+artifact.ID.String()+`"]}`)
	withPayload := execTool(t, registry, "get_security_research_artifacts", `{"artifact_ids":["`+artifact.ID.String()+`"],"include_payload":true}`)
	if withoutPayload.IsError || withPayload.IsError || strings.Contains(withoutPayload.Content, `"payload"`) || !strings.Contains(withPayload.Content, `"payload":{"command"`) {
		t.Fatalf("payload retrieval bounds: without=%+v with=%+v", withoutPayload, withPayload)
	}

	otherTask := scanCtx
	otherTask.RunName, otherTask.TaskName = "validator-run", "independent-validation"
	otherRegistry := researchToolRegistry(t, researchStore, otherTask)
	fromSibling := execTool(t, otherRegistry, "get_security_research_artifacts", `{"artifact_ids":["`+artifact.ID.String()+`"],"include_payload":true}`)
	if fromSibling.IsError || researchStore.lastArtifactFilter.ExecutionID != scanCtx.ExecutionID {
		t.Fatalf("same-execution sibling could not retrieve artifact: %+v filter=%+v", fromSibling, researchStore.lastArtifactFilter)
	}

	otherExecution := otherTask
	otherExecution.ExecutionID = "different-execution"
	isolatedRegistry := researchToolRegistry(t, researchStore, otherExecution)
	isolated := execTool(t, isolatedRegistry, "get_security_research_artifacts", `{"artifact_ids":["`+artifact.ID.String()+`"],"include_payload":true}`)
	if !isolated.IsError || !strings.Contains(isolated.Content, store.ErrSecurityResearchArtifactNotFound.Error()) {
		t.Fatalf("cross-execution artifact leaked: %+v", isolated)
	}
}

func TestSecurityResearchArtifactToolEnforcesPayloadAndConditionBounds(t *testing.T) {
	researchStore := newFakeSecurityResearchStore()
	scanCtx := bountyLaneContext("investigator-run", "", bountyLaneProgram())
	scanCtx.TaskName = "investigator"
	registry := researchToolRegistry(t, researchStore, scanCtx)

	oversized := execTool(t, registry, "create_security_research_artifact", `{"kind":"trace","payload":{"body":"`+strings.Repeat("x", store.MaxSecurityResearchArtifactPayloadBytes)+`"},"idempotency_key":"too-big"}`)
	nonscalar := execTool(t, registry, "create_security_research_artifact", `{"kind":"trace","payload":{},"conditions":{"nested":{"not":"scalar"}},"idempotency_key":"bad-condition"}`)
	fingerprints := make([]string, 40)
	for i := range fingerprints {
		fingerprints[i] = uuid.NewString() + strings.Repeat("x", 220)
	}
	handoffInput, err := json.Marshal(map[string]any{
		"kind": "trace", "payload": map[string]any{}, "candidate_fingerprints": fingerprints,
		"idempotency_key": "oversized-handoff",
	})
	if err != nil {
		t.Fatal(err)
	}
	oversizedHandoff := execTool(t, registry, "create_security_research_artifact", string(handoffInput))
	if !oversized.IsError || !nonscalar.IsError || !oversizedHandoff.IsError || len(researchStore.researchArtifacts) != 0 {
		t.Fatalf("bounded creates: oversized=%+v nonscalar=%+v handoff=%+v artifacts=%d", oversized, nonscalar, oversizedHandoff, len(researchStore.researchArtifacts))
	}
}

func TestSecurityResearchArtifactToolsSupportLegacyRunScope(t *testing.T) {
	researchStore := newFakeSecurityResearchStore()
	scanCtx := bountyLaneContext("legacy-run", "", bountyLaneProgram())
	scanCtx.ExecutionID, scanCtx.TaskName = "", ""
	registry := researchToolRegistry(t, researchStore, scanCtx)
	result := execTool(t, registry, "create_security_research_artifact", `{"kind":"manifest","payload":{"legacy":true},"idempotency_key":"legacy"}`)
	if result.IsError {
		t.Fatalf("legacy fallback create: %s", result.Content)
	}
	for _, artifact := range researchStore.researchArtifacts {
		if artifact.ExecutionID != scanCtx.RunName || artifact.TaskName != scanCtx.RunName {
			t.Fatalf("legacy artifact scope = execution %q task %q, want run %q", artifact.ExecutionID, artifact.TaskName, scanCtx.RunName)
		}
	}
}
