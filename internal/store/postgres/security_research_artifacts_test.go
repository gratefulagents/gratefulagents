package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestSecurityResearchArtifactsAreImmutableIdempotentAndIsolated(t *testing.T) {
	s, namespace := setupSecurityResearchTestStore(t)
	ctx := context.Background()
	_, revision := createSecurityResearchFixture(t, s, namespace)
	coverage, created, err := s.RecordSecurityResearchCoverage(ctx, namespace, &store.SecurityResearchCoverage{
		RevisionID: revision.ID, Dimension: store.SecurityCoverageInvariant, SubjectKey: "supply",
		Verdict: store.SecurityCoverageAdequatelyTested, Bounds: json.RawMessage(`{}`), Evidence: json.RawMessage(`[]`),
		Actor: "investigator", IdempotencyKey: "coverage-artifact-test",
	})
	if err != nil || !created {
		t.Fatalf("coverage: created=%v err=%v", created, err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM security_scans WHERE namespace = $1`, namespace)
	})
	snapshot, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: namespace, ScanName: "target", RunName: "scan-run", Repository: "org/repo", Revision: revision.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	finding, _, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: snapshot.ID, Namespace: namespace, ScanName: "target", RunName: "investigator-run", ExecutionID: "execution-1",
		Repository: "org/repo", Revision: revision.Revision, Fingerprint: "candidate-fingerprint", Title: "Candidate", Severity: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	value := &store.SecurityResearchArtifact{
		RevisionID: revision.ID, ExecutionID: "execution-1", TaskName: "investigator", Actor: "investigator-run",
		Kind: store.SecurityResearchArtifactHarnessSummary, SchemaVersion: store.SecurityTaskHandoffVersion,
		Payload:               json.RawMessage(`{"command":"go test ./...","result":"bounded negative"}`),
		CandidateFingerprints: []string{finding.Fingerprint},
		CoverageIDs:           map[string][]uuid.UUID{store.SecurityCoverageAdequatelyTested: {coverage.ID}},
		Conditions:            map[string]json.RawMessage{"ready": json.RawMessage(`true`)}, IdempotencyKey: "harness-1",
	}
	artifact, made, err := s.CreateSecurityResearchArtifact(ctx, namespace, value)
	if err != nil || !made {
		t.Fatalf("create artifact: made=%v value=%+v err=%v", made, artifact, err)
	}
	replayed := *value
	replayed.Payload = json.RawMessage(`{"changed":true}`)
	again, made, err := s.CreateSecurityResearchArtifact(ctx, namespace, &replayed)
	if err != nil || made || again.ID != artifact.ID || string(again.Payload) != string(artifact.Payload) {
		t.Fatalf("immutable replay: made=%v value=%+v err=%v", made, again, err)
	}

	got, err := s.GetSecurityResearchArtifact(ctx, namespace, revision.ID, value.ExecutionID, artifact.ID)
	if err != nil || got.ID != artifact.ID {
		t.Fatalf("get artifact: value=%+v err=%v", got, err)
	}
	for _, lookup := range []struct {
		namespace, execution string
		revision             uuid.UUID
	}{
		{namespace + "-other", value.ExecutionID, revision.ID},
		{namespace, "execution-2", revision.ID},
		{namespace, value.ExecutionID, uuid.New()},
	} {
		if _, err := s.GetSecurityResearchArtifact(ctx, lookup.namespace, lookup.revision, lookup.execution, artifact.ID); !errors.Is(err, store.ErrSecurityResearchArtifactNotFound) {
			t.Fatalf("cross-scope get error = %v, want not found", err)
		}
	}

	listed, err := s.ListSecurityResearchArtifacts(ctx, namespace, store.SecurityResearchArtifactFilter{
		RevisionID: revision.ID, ExecutionID: value.ExecutionID, Limit: 20,
	})
	if err != nil || len(listed) != 1 || listed[0].Payload != nil {
		t.Fatalf("metadata list: values=%+v err=%v", listed, err)
	}
	listed, err = s.ListSecurityResearchArtifacts(ctx, namespace, store.SecurityResearchArtifactFilter{
		RevisionID: revision.ID, ExecutionID: value.ExecutionID, IDs: []uuid.UUID{artifact.ID}, Limit: 1, IncludePayload: true,
	})
	if err != nil || len(listed) != 1 || len(listed[0].Payload) == 0 {
		t.Fatalf("payload list: values=%+v err=%v", listed, err)
	}

	blocker, made, err := s.CreateSecurityResearchArtifact(ctx, namespace, &store.SecurityResearchArtifact{
		RevisionID: revision.ID, ExecutionID: value.ExecutionID, TaskName: "readiness", Actor: "readiness-run",
		Kind: store.SecurityResearchArtifactBlocker, SchemaVersion: store.SecurityTaskHandoffVersion,
		Payload: json.RawMessage(`{"reason":"toolchain unavailable"}`), IdempotencyKey: "blocker-1",
	})
	if err != nil || !made {
		t.Fatalf("create blocker: made=%v value=%+v err=%v", made, blocker, err)
	}
	linked, made, err := s.CreateSecurityResearchArtifact(ctx, namespace, &store.SecurityResearchArtifact{
		RevisionID: revision.ID, ExecutionID: value.ExecutionID, TaskName: "triage", Actor: "triage-run",
		Kind: store.SecurityResearchArtifactManifest, SchemaVersion: store.SecurityTaskHandoffVersion,
		Payload: json.RawMessage(`{"status":"partial"}`), BlockerIDs: []uuid.UUID{blocker.ID}, IdempotencyKey: "manifest-1",
	})
	if err != nil || !made || len(linked.BlockerIDs) != 1 || linked.BlockerIDs[0] != blocker.ID {
		t.Fatalf("linked blocker: made=%v value=%+v err=%v", made, linked, err)
	}
}

func TestSecurityResearchArtifactRejectsCrossRevisionCoverageAndExecutionBlocker(t *testing.T) {
	s, namespace := setupSecurityResearchTestStore(t)
	ctx := context.Background()
	_, revision := createSecurityResearchFixture(t, s, namespace)
	otherTarget, err := s.UpsertSecurityResearchTarget(ctx, &store.SecurityResearchTarget{Namespace: namespace, TargetKey: "other", Kind: "repository", Locator: "org/other"})
	if err != nil {
		t.Fatal(err)
	}
	otherRevision, _, err := s.BindSecurityResearchRevision(ctx, namespace, &store.SecurityResearchRevision{TargetID: otherTarget.ID, Revision: "feedface"})
	if err != nil {
		t.Fatal(err)
	}
	otherCoverage, _, err := s.RecordSecurityResearchCoverage(ctx, namespace, &store.SecurityResearchCoverage{
		RevisionID: otherRevision.ID, Dimension: store.SecurityCoverageInvariant, SubjectKey: "other",
		Verdict: store.SecurityCoverageNotTested, IdempotencyKey: "other-coverage",
	})
	if err != nil {
		t.Fatal(err)
	}
	blocker, _, err := s.CreateSecurityResearchArtifact(ctx, namespace, &store.SecurityResearchArtifact{
		RevisionID: revision.ID, ExecutionID: "other-execution", TaskName: "readiness", Actor: "run",
		Kind: store.SecurityResearchArtifactBlocker, SchemaVersion: store.SecurityTaskHandoffVersion,
		Payload: json.RawMessage(`{}`), IdempotencyKey: "other-blocker",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []*store.SecurityResearchArtifact{
		{
			RevisionID: revision.ID, ExecutionID: "execution", TaskName: "task", Actor: "run",
			Kind: store.SecurityResearchArtifactTrace, SchemaVersion: store.SecurityTaskHandoffVersion,
			Payload: json.RawMessage(`{}`), CoverageIDs: map[string][]uuid.UUID{store.SecurityCoverageNotTested: {otherCoverage.ID}}, IdempotencyKey: "bad-coverage",
		},
		{
			RevisionID: revision.ID, ExecutionID: "execution", TaskName: "task", Actor: "run",
			Kind: store.SecurityResearchArtifactTrace, SchemaVersion: store.SecurityTaskHandoffVersion,
			Payload: json.RawMessage(`{}`), BlockerIDs: []uuid.UUID{blocker.ID}, IdempotencyKey: "bad-blocker",
		},
	} {
		if _, _, err := s.CreateSecurityResearchArtifact(ctx, namespace, value); !errors.Is(err, store.ErrSecurityResearchArtifactReferenceNotFound) {
			t.Fatalf("cross-scope reference error = %v", err)
		}
	}
}
