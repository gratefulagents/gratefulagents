package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func setupSecurityResearchTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	s := NewFromPool(pool)
	namespace := "security-research-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM security_research_targets WHERE namespace = $1`, namespace)
		_ = s.Close()
	})
	return s, namespace
}

func createSecurityResearchFixture(t *testing.T, s *Store, namespace string) (*store.SecurityResearchTarget, *store.SecurityResearchRevision) {
	t.Helper()
	ctx := context.Background()
	target, err := s.UpsertSecurityResearchTarget(ctx, &store.SecurityResearchTarget{
		Namespace: namespace, TargetKey: "target", Kind: "repository", Locator: "org/repo",
	})
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	revision, created, err := s.BindSecurityResearchRevision(ctx, namespace, &store.SecurityResearchRevision{TargetID: target.ID, Revision: "deadbeef"})
	if err != nil || !created {
		t.Fatalf("revision: created=%v err=%v", created, err)
	}
	return target, revision
}

func createResearchHypothesis(t *testing.T, s *Store, namespace string, revisionID uuid.UUID, key string) *store.SecurityResearchHypothesis {
	t.Helper()
	value, created, err := s.CreateSecurityResearchHypothesis(context.Background(), namespace, &store.SecurityResearchHypothesis{
		RevisionID: revisionID, HypothesisKey: key, Title: "Hypothesis " + key,
		Invariant: "accounting remains balanced", Actor: "test-agent", IdempotencyKey: "create-" + key,
		Detail: json.RawMessage(`{"anchor":"src/ledger.rs:42","prior":"p3"}`),
	})
	if err != nil || !created {
		t.Fatalf("hypothesis %s: created=%v err=%v", key, created, err)
	}
	return value
}

func TestConfirmSecurityFindingRecordsSameStatusAsReview(t *testing.T) {
	s, namespace := setupSecurityResearchTestStore(t)
	ctx := context.Background()
	_, revision := createSecurityResearchFixture(t, s, namespace)
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM security_scans WHERE namespace = $1`, namespace)
	})

	scan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace: namespace, ScanName: "target", RunName: "target-run", Repository: "org/repo", Revision: revision.Revision,
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	finding, _, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
		ScanID: scan.ID, Namespace: namespace, ScanName: "target", RunName: "target-run",
		Repository: "org/repo", Revision: revision.Revision, Fingerprint: "confirm-review", Title: "Candidate", Severity: "high",
	})
	if err != nil {
		t.Fatalf("finding: %v", err)
	}
	if err := s.ConfirmSecurityFindingWithVariantSweep(ctx, namespace, finding.ID, "alice", "proved"); err != nil {
		t.Fatalf("initial confirmation: %v", err)
	}
	if err := s.ConfirmSecurityFindingWithVariantSweep(ctx, namespace, finding.ID, "bob", "rechecked"); err != nil {
		t.Fatalf("confirmation review: %v", err)
	}
	events, err := s.ListSecurityFindingEvents(ctx, namespace, finding.ID, 0)
	if err != nil || len(events) < 2 || events[0].EventType != "status_reviewed" || events[1].EventType != "status_changed" {
		t.Fatalf("confirmation events = %v, %v, want review then change", events, err)
	}
}

func TestSecurityResearchHypothesisIsolationTransitionsAndLineage(t *testing.T) {
	s, namespace := setupSecurityResearchTestStore(t)
	ctx := context.Background()
	_, revision := createSecurityResearchFixture(t, s, namespace)

	if got, err := s.GetSecurityResearchRevision(ctx, namespace+"-other", "target", "deadbeef"); err != nil || got != nil {
		t.Fatalf("cross-namespace revision = %#v, %v", got, err)
	}
	h1 := createResearchHypothesis(t, s, namespace, revision.ID, "h1")
	events, err := s.ListSecurityResearchHypothesisEvents(ctx, namespace, h1.ID)
	if err != nil || len(events) != 1 || events[0].Actor != "test-agent" {
		t.Fatalf("creation audit actor: events=%#v err=%v", events, err)
	}
	replayed, created, err := s.CreateSecurityResearchHypothesis(ctx, namespace, &store.SecurityResearchHypothesis{
		RevisionID: revision.ID, HypothesisKey: "h1", Title: "Hypothesis h1",
		Invariant: "accounting remains balanced", Actor: "test-agent", IdempotencyKey: "create-h1",
	})
	if err != nil || created || replayed.ID != h1.ID {
		t.Fatalf("create replay: value=%#v created=%v err=%v", replayed, created, err)
	}
	transition := store.SecurityHypothesisTransition{ExpectedVersion: 1, ToStatus: store.SecurityHypothesisInvestigating, Result: store.SecurityHypothesisResultPending, Rationale: "begin", IdempotencyKey: "investigate"}
	investigating, err := s.TransitionSecurityResearchHypothesis(ctx, namespace, h1.ID, transition)
	if err != nil || investigating.Version != 2 {
		t.Fatalf("transition: value=%#v err=%v", investigating, err)
	}
	if replay, err := s.TransitionSecurityResearchHypothesis(ctx, namespace, h1.ID, transition); err != nil || replay.Version != 2 {
		t.Fatalf("transition replay: value=%#v err=%v", replay, err)
	}
	_, err = s.TransitionSecurityResearchHypothesis(ctx, namespace, h1.ID, store.SecurityHypothesisTransition{ExpectedVersion: 1, ToStatus: store.SecurityHypothesisSupported, Result: store.SecurityHypothesisResultPositive, Rationale: "stale", IdempotencyKey: "stale"})
	if !errors.Is(err, store.ErrSecurityResearchVersionConflict) {
		t.Fatalf("stale transition error = %v", err)
	}
	_, err = s.TransitionSecurityResearchHypothesis(ctx, namespace, h1.ID, store.SecurityHypothesisTransition{ExpectedVersion: 2, ToStatus: store.SecurityHypothesisPromoted, Result: store.SecurityHypothesisResultPositive, Rationale: "skip", IdempotencyKey: "skip"})
	if !errors.Is(err, store.ErrSecurityResearchInvalidTransition) {
		t.Fatalf("invalid transition error = %v", err)
	}
	supported, err := s.TransitionSecurityResearchHypothesis(ctx, namespace, h1.ID, store.SecurityHypothesisTransition{ExpectedVersion: 2, ToStatus: store.SecurityHypothesisSupported, Result: store.SecurityHypothesisResultPositive, Rationale: "evidence", IdempotencyKey: "support", Detail: json.RawMessage(`{"guard_citation":"src/ledger.rs:50"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var merged map[string]any
	if err := json.Unmarshal(supported.Detail, &merged); err != nil {
		t.Fatalf("decode merged detail: %v", err)
	}
	if merged["anchor"] != "src/ledger.rs:42" || merged["prior"] != "p3" || merged["guard_citation"] != "src/ledger.rs:50" {
		t.Fatalf("transition must merge detail, keeping the creation anchor and prior: %v", merged)
	}
	reopened, err := s.ReopenSecurityResearchHypothesis(ctx, namespace, h1.ID, store.SecurityHypothesisTransition{ExpectedVersion: supported.Version, Rationale: "new evidence", IdempotencyKey: "reopen"})
	if err != nil || reopened.Status != store.SecurityHypothesisInvestigating || reopened.Result != store.SecurityHypothesisResultPending {
		t.Fatalf("reopen: value=%#v err=%v", reopened, err)
	}
	_, err = s.ReopenSecurityResearchHypothesis(ctx, namespace, h1.ID, store.SecurityHypothesisTransition{ExpectedVersion: reopened.Version, Rationale: "again", IdempotencyKey: "reopen-again"})
	if !errors.Is(err, store.ErrSecurityResearchInvalidTransition) {
		t.Fatalf("invalid reopen error = %v", err)
	}

	h2 := createResearchHypothesis(t, s, namespace, revision.ID, "h2")
	h3 := createResearchHypothesis(t, s, namespace, revision.ID, "h3")
	if err := s.AddSecurityResearchHypothesisLineage(ctx, namespace, store.SecurityHypothesisLineage{ChildID: h2.ID, ParentID: h1.ID, Relation: "derived_from"}, "lineage-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSecurityResearchHypothesisLineage(ctx, namespace, store.SecurityHypothesisLineage{ChildID: h3.ID, ParentID: h2.ID, Relation: "derived_from"}, "lineage-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSecurityResearchHypothesisLineage(ctx, namespace, store.SecurityHypothesisLineage{ChildID: h1.ID, ParentID: h3.ID, Relation: "derived_from"}, "lineage-cycle"); !errors.Is(err, store.ErrSecurityResearchLineageCycle) {
		t.Fatalf("cycle error = %v", err)
	}
}

//nolint:gocyclo // This integration test intentionally exercises the complete durable lifecycle.
func TestSecurityResearchSweepReservationsOutcomesAndPrecision(t *testing.T) {
	s, namespace := setupSecurityResearchTestStore(t)
	ctx := context.Background()
	target, revision := createSecurityResearchFixture(t, s, namespace)

	sweep, created, err := s.CreateSecurityResearchVariantSweep(ctx, namespace, &store.SecurityResearchVariantSweep{
		RevisionID: revision.ID, RootCause: "missing authorization", Status: store.SecurityVariantSweepRunning,
		Scope: json.RawMessage(`{"surface":"handlers"}`), Actor: "tester", IdempotencyKey: "sweep-1",
	})
	if err != nil || !created {
		t.Fatalf("create sweep: created=%v err=%v", created, err)
	}
	if replay, created, err := s.CreateSecurityResearchVariantSweep(ctx, namespace, &store.SecurityResearchVariantSweep{RevisionID: revision.ID, RootCause: "missing authorization", Scope: json.RawMessage(`{"surface":"handlers"}`), Status: store.SecurityVariantSweepRunning, Actor: "tester", IdempotencyKey: "sweep-1"}); err != nil || created || replay.ID != sweep.ID {
		t.Fatalf("sweep replay: value=%#v created=%v err=%v", replay, created, err)
	}
	completed, err := s.CompleteSecurityResearchVariantSweep(ctx, namespace, sweep.ID, store.SecurityVariantSweepCompleted, json.RawMessage(`{"searched_scope":["handlers"],"methods":["grep"],"evidence":["auth.go:10"],"summary":"no siblings found"}`), "agent", "finish-1")
	if err != nil || completed.CompletedAt == nil {
		t.Fatalf("complete sweep: value=%#v err=%v", completed, err)
	}
	if events, err := s.ListSecurityResearchVariantSweepEvents(ctx, namespace, sweep.ID); err != nil || len(events) != 2 {
		t.Fatalf("sweep events: %#v err=%v", events, err)
	}

	const workflow = "bounty"
	submissions := make([]*store.SecurityResearchSubmission, 8)
	for i := range submissions {
		value, made, err := s.CreateSecurityResearchSubmission(ctx, namespace, &store.SecurityResearchSubmission{RevisionID: revision.ID, Workflow: workflow, CandidateKey: fmt.Sprintf("candidate-%d", i), Rank: int32(i + 1)})
		if err != nil || !made {
			t.Fatalf("submission %d: created=%v err=%v", i, made, err)
		}
		submissions[i] = value
	}
	results := make(chan *store.SecuritySubmissionReservationResult, len(submissions))
	errs := make(chan error, len(submissions))
	var wg sync.WaitGroup
	for i, submission := range submissions {
		wg.Add(1)
		go func(i int, submission *store.SecurityResearchSubmission) {
			defer wg.Done()
			result, err := s.ReserveSecurityResearchSubmission(ctx, namespace, store.SecuritySubmissionReservationRequest{SubmissionID: submission.ID, Workflow: workflow, PeriodDays: 7, BudgetLimit: 3, IdempotencyKey: fmt.Sprintf("reserve-%d", i)})
			results <- result
			errs <- err
		}(i, submission)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent reserve: %v", err)
		}
	}
	reserved := 0
	var winner *store.SecuritySubmissionReservation
	for result := range results {
		if result.Reserved {
			reserved++
			winner = result.Reservation
		} else if result.Used != 3 || result.Limit != 3 {
			t.Fatalf("exhausted result = %#v", result)
		}
	}
	if reserved != 3 {
		t.Fatalf("reserved = %d, want 3", reserved)
	}

	if _, err := s.Pool().Exec(ctx, `UPDATE security_research_submission_reservations SET reserved_at = now() - interval '8 days', expires_at = now() - interval '1 day' WHERE id = $1`, winner.ID); err != nil {
		t.Fatal(err)
	}
	var replacementID uuid.UUID
	if err := s.Pool().QueryRow(ctx, `SELECT id FROM security_research_submissions WHERE revision_id = $1 AND workflow = $2 AND status = 'candidate' ORDER BY rank LIMIT 1`, revision.ID, workflow).Scan(&replacementID); err != nil {
		t.Fatal(err)
	}
	var replacement *store.SecurityResearchSubmission
	for _, submission := range submissions {
		if submission.ID == replacementID {
			replacement = submission
			break
		}
	}
	if replacement == nil {
		t.Fatal("candidate replacement not found")
	}
	windowResult, err := s.ReserveSecurityResearchSubmission(ctx, namespace, store.SecuritySubmissionReservationRequest{SubmissionID: replacement.ID, Workflow: workflow, PeriodDays: 7, BudgetLimit: 3, IdempotencyKey: "window-replacement"})
	if err != nil || !windowResult.Reserved {
		t.Fatalf("window replacement: result=%#v err=%v", windowResult, err)
	}
	var voided bool
	if err := s.Pool().QueryRow(ctx, `SELECT voided_at IS NOT NULL FROM security_research_submission_reservations WHERE id = $1`, winner.ID).Scan(&voided); err != nil || !voided {
		t.Fatalf("expired reservation voided=%v err=%v", voided, err)
	}

	if err := s.MarkSecurityResearchSubmissionPackaged(ctx, namespace, replacement.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordSecuritySubmissionOutcome(ctx, namespace, replacement.ID, store.SecuritySubmissionOutcomeInput{RevisionID: revision.ID, Outcome: store.SecuritySubmissionOutcomeAccepted, IdempotencyKey: "outcome-0"}); err == nil {
		t.Fatal("packaged bundle accepted an outcome before a human filed it")
	}
	if precision, err := s.GetSecuritySubmissionPrecision(ctx, namespace, target.ID, workflow, nil); err != nil || precision.Submitted != 0 {
		t.Fatalf("packaged precision: value=%#v err=%v, want packaged rows outside the denominator", precision, err)
	}
	filed, err := s.MarkSecurityResearchSubmissionSubmitted(ctx, namespace, replacement.ID, store.SecuritySubmissionHandoff{Program: "immunefi", ExternalReference: "IMM-1", Actor: "alice"})
	if err != nil || filed.Status != store.SecuritySubmissionStatusSubmitted || filed.Program != "immunefi" || filed.SubmittedBy != "alice" || filed.SubmittedAt == nil || filed.PackagedAt == nil || filed.TargetKey != "target" {
		t.Fatalf("handoff: value=%+v err=%v", filed, err)
	}
	accepted, made, err := s.RecordSecuritySubmissionOutcome(ctx, namespace, replacement.ID, store.SecuritySubmissionOutcomeInput{RevisionID: revision.ID, Outcome: store.SecuritySubmissionOutcomeAccepted, ExternalReference: "report-1", IdempotencyKey: "outcome-1"})
	if err != nil || !made {
		t.Fatalf("outcome: value=%#v created=%v err=%v", accepted, made, err)
	}
	corrected, made, err := s.RecordSecuritySubmissionOutcome(ctx, namespace, replacement.ID, store.SecuritySubmissionOutcomeInput{RevisionID: revision.ID, Outcome: store.SecuritySubmissionOutcomeDuplicate, ExternalReference: "report-2", CorrectionOf: &accepted.EventID, IdempotencyKey: "outcome-2"})
	if err != nil || !made || corrected.Outcome != store.SecuritySubmissionOutcomeDuplicate {
		t.Fatalf("correction: value=%#v created=%v err=%v", corrected, made, err)
	}
	precision, err := s.GetSecuritySubmissionPrecision(ctx, namespace, target.ID, workflow, nil)
	if err != nil || precision.Submitted != 1 || precision.Accepted != 0 || precision.Duplicate != 1 {
		t.Fatalf("precision: value=%#v err=%v", precision, err)
	}
	snapshot, made, err := s.CreateSecurityResearchDecisionSnapshot(ctx, namespace, &store.SecurityResearchDecisionSnapshot{RevisionID: revision.ID, SubmissionID: &replacement.ID, Workflow: workflow, CandidateKey: replacement.CandidateKey, Decision: "submit", Reason: "highest confidence", Rank: 1, IdempotencyKey: "decision-1"})
	if err != nil || !made {
		t.Fatalf("decision snapshot: value=%#v created=%v err=%v", snapshot, made, err)
	}
	if replay, made, err := s.CreateSecurityResearchDecisionSnapshot(ctx, namespace, &store.SecurityResearchDecisionSnapshot{RevisionID: revision.ID, SubmissionID: &replacement.ID, Workflow: workflow, CandidateKey: replacement.CandidateKey, Decision: "submit", Reason: "highest confidence", Rank: 1, IdempotencyKey: "decision-1"}); err != nil || made || replay.ID != snapshot.ID {
		t.Fatalf("decision replay: value=%#v created=%v err=%v", replay, made, err)
	}
}

func TestAmendSecurityResearchDossierExpectedVersionIsCurrentVersion(t *testing.T) {
	s, namespace := setupSecurityResearchTestStore(t)
	_, revision := createSecurityResearchFixture(t, s, namespace)
	ctx := context.Background()
	first, created, err := s.AmendSecurityResearchDossier(ctx, namespace, &store.SecurityResearchDossier{
		RevisionID: revision.ID, Content: json.RawMessage(`{"scope":"initial"}`), IdempotencyKey: "dossier-v1",
	})
	if err != nil || !created || first.Version != 1 {
		t.Fatalf("first amendment = %+v, created=%v, err=%v", first, created, err)
	}
	second, created, err := s.AmendSecurityResearchDossier(ctx, namespace, &store.SecurityResearchDossier{
		RevisionID: revision.ID, Version: 1, ParentID: &first.ID, Content: json.RawMessage(`{"scope":"expanded"}`), IdempotencyKey: "dossier-v2",
	})
	if err != nil || !created || second.Version != 2 {
		t.Fatalf("second amendment = %+v, created=%v, err=%v", second, created, err)
	}
	_, _, err = s.AmendSecurityResearchDossier(ctx, namespace, &store.SecurityResearchDossier{
		RevisionID: revision.ID, Version: 1, ParentID: &second.ID, Content: json.RawMessage(`{"scope":"stale"}`), IdempotencyKey: "dossier-stale",
	})
	if !errors.Is(err, store.ErrSecurityResearchVersionConflict) {
		t.Fatalf("stale expected version error = %v", err)
	}
}

func TestListSecurityResearchSubmissionsAttachesLatestOutcomeAndPackagesUnreservedCandidates(t *testing.T) {
	s, namespace := setupSecurityResearchTestStore(t)
	_, revision := createSecurityResearchFixture(t, s, namespace)
	ctx := context.Background()
	first, _, err := s.CreateSecurityResearchSubmission(ctx, namespace, &store.SecurityResearchSubmission{RevisionID: revision.ID, Workflow: "bounty", CandidateKey: "fp-first", Rank: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.CreateSecurityResearchSubmission(ctx, namespace, &store.SecurityResearchSubmission{RevisionID: revision.ID, Workflow: "bounty", CandidateKey: "fp-second", Rank: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSecurityResearchSubmissionPackaged(ctx, namespace, first.ID, time.Now()); err != nil {
		t.Fatalf("never-reserved candidate could not be packaged: %v", err)
	}
	if _, err := s.MarkSecurityResearchSubmissionSubmitted(ctx, namespace, first.ID, store.SecuritySubmissionHandoff{Program: "hackerone", Actor: "alice"}); err != nil {
		t.Fatalf("packaged candidate could not be handed over: %v", err)
	}
	if _, _, err := s.RecordSecuritySubmissionOutcome(ctx, namespace, first.ID, store.SecuritySubmissionOutcomeInput{RevisionID: revision.ID, Outcome: store.SecuritySubmissionOutcomeAccepted, IdempotencyKey: "outcome-1"}); err != nil {
		t.Fatal(err)
	}
	reserved, err := s.ReserveSecurityResearchSubmission(ctx, namespace, store.SecuritySubmissionReservationRequest{SubmissionID: second.ID, Workflow: "bounty", PeriodDays: 7, BudgetLimit: 3, IdempotencyKey: "reserve-second"})
	if err != nil || !reserved.Reserved {
		t.Fatalf("reserve: result=%#v err=%v", reserved, err)
	}
	if err := s.VoidSecurityResearchSubmissionReservation(ctx, namespace, second.ID, "reserve-second"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSecurityResearchSubmissionPackaged(ctx, namespace, second.ID, time.Now()); err == nil {
		t.Fatal("candidate with a voided reservation bypassed the rolling budget")
	}
	values, err := s.ListSecurityResearchSubmissions(ctx, namespace, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].ID != first.ID || values[1].ID != second.ID {
		t.Fatalf("submissions = %+v", values)
	}
	if values[0].Status != "submitted" || values[0].Program != "hackerone" || values[0].Outcome != store.SecuritySubmissionOutcomeAccepted || values[0].OutcomeRecordedAt == nil {
		t.Fatalf("first submission = %+v", values[0])
	}
	if values[1].Status != "candidate" || values[1].Outcome != "" || values[1].OutcomeRecordedAt != nil {
		t.Fatalf("second submission = %+v", values[1])
	}
}

func TestSecuritySubmissionQueueAndPrecisionRollup(t *testing.T) {
	s, namespace := setupSecurityResearchTestStore(t)
	ctx := context.Background()
	target, revision := createSecurityResearchFixture(t, s, namespace)
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM security_scans WHERE namespace = $1`, namespace)
	})
	scan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{Namespace: namespace, ScanName: "target", RunName: "target-run", Repository: "org/repo", Revision: revision.Revision})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	otherScan, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{Namespace: namespace, ScanName: "other", RunName: "other-run", Repository: "org/other", Revision: "cafe"})
	if err != nil {
		t.Fatalf("other scan: %v", err)
	}
	newFinding := func(scan *store.SecurityScanRecord, fingerprint, severity, status string) *store.SecurityFindingRecord {
		finding, _, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
			ScanID: scan.ID, Namespace: namespace, ScanName: scan.ScanName, RunName: scan.RunName,
			Repository: scan.Repository, Revision: scan.Revision, Fingerprint: fingerprint, Title: "Finding " + fingerprint, Severity: severity,
		})
		if err != nil {
			t.Fatalf("finding %s: %v", fingerprint, err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE security_findings SET status = $2 WHERE id = $1`, finding.ID, status); err != nil {
			t.Fatal(err)
		}
		return finding
	}
	// The updated_at trigger stamps ready_at, so readiness order follows call order.
	readyBundle := func(finding *store.SecurityFindingRecord, execution string) {
		if _, err := s.UpsertSecurityFindingArtifact(ctx, namespace, &store.SecurityFindingArtifact{FindingID: finding.ID, ExecutionID: execution, Kind: "submission_bundle", Status: "generating"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpsertSecurityFindingArtifact(ctx, namespace, &store.SecurityFindingArtifact{FindingID: finding.ID, ExecutionID: execution, Kind: "submission_bundle", Status: "ready", Filename: finding.Fingerprint + ".zip"}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	high := newFinding(scan, "high-fp", "high", store.SecurityFindingStatusConfirmed)
	criticalLate := newFinding(scan, "critical-late", "critical", store.SecurityFindingStatusTriaged)
	criticalEarly := newFinding(scan, "critical-early", "critical", store.SecurityFindingStatusConfirmed)
	open := newFinding(scan, "open-fp", "critical", store.SecurityFindingStatusOpen)
	noBundle := newFinding(scan, "no-bundle", "critical", store.SecurityFindingStatusConfirmed)
	hidden := newFinding(otherScan, "hidden-fp", "critical", store.SecurityFindingStatusConfirmed)
	readyBundle(open, "exec-1")
	readyBundle(hidden, "exec-1")
	readyBundle(high, "exec-1")
	readyBundle(criticalEarly, "exec-1")
	readyBundle(criticalLate, "exec-1")
	if _, err := s.UpsertSecurityFindingArtifact(ctx, namespace, &store.SecurityFindingArtifact{FindingID: noBundle.ID, ExecutionID: "exec-1", Kind: "submission_bundle", Status: "generating"}); err != nil {
		t.Fatal(err)
	}

	submission, _, err := s.CreateSecurityResearchSubmission(ctx, namespace, &store.SecurityResearchSubmission{RevisionID: revision.ID, FindingID: &criticalEarly.ID, Workflow: "bounty", CandidateKey: criticalEarly.Fingerprint, Rank: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSecurityResearchSubmissionPackaged(ctx, namespace, submission.ID, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSecurityResearchSubmissionPackaged(ctx, namespace, submission.ID, now); err != nil {
		t.Fatalf("packaging is not idempotent: %v", err)
	}
	filedSubmission, _, err := s.CreateSecurityResearchSubmission(ctx, namespace, &store.SecurityResearchSubmission{RevisionID: revision.ID, FindingID: &high.ID, Workflow: "bounty", CandidateKey: high.Fingerprint, Rank: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSecurityResearchSubmissionPackaged(ctx, namespace, filedSubmission.ID, now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkSecurityResearchSubmissionSubmitted(ctx, namespace, filedSubmission.ID, store.SecuritySubmissionHandoff{Program: "immunefi", ExternalReference: "IMM-7", Actor: "alice", SubmittedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.MarkSecurityResearchSubmissionSubmitted(ctx, namespace, filedSubmission.ID, store.SecuritySubmissionHandoff{Program: "other", Actor: "bob"}); err != nil || replay.Program != "immunefi" || replay.SubmittedBy != "alice" {
		t.Fatalf("replayed handoff rewrote the record: value=%+v err=%v", replay, err)
	}
	if _, _, err := s.RecordSecuritySubmissionOutcome(ctx, namespace, filedSubmission.ID, store.SecuritySubmissionOutcomeInput{RevisionID: revision.ID, Outcome: store.SecuritySubmissionOutcomeAccepted, IdempotencyKey: "outcome-1"}); err != nil {
		t.Fatal(err)
	}

	queue, err := s.ListSecuritySubmissionQueue(ctx, namespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 4 {
		t.Fatalf("queue = %d items, want 4: %+v", len(queue), queue)
	}
	if queue[0].FindingID != hidden.ID || queue[1].FindingID != criticalEarly.ID || queue[2].FindingID != criticalLate.ID || queue[3].FindingID != high.ID {
		t.Fatalf("queue order = %v %v %v %v, want severity desc then bundle readiness asc", queue[0].Fingerprint, queue[1].Fingerprint, queue[2].Fingerprint, queue[3].Fingerprint)
	}
	queue = queue[1:]
	if queue[0].SubmissionID == nil || *queue[0].SubmissionID != submission.ID || queue[0].SubmissionStatus != store.SecuritySubmissionStatusPackaged || queue[0].TargetKey != "target" || queue[0].Revision != revision.Revision || queue[0].BundleFilename != "critical-early.zip" || queue[0].BundleReadyAt.IsZero() {
		t.Fatalf("packaged queue item = %+v", queue[0])
	}
	if queue[1].SubmissionID != nil || queue[1].SubmissionStatus != "" {
		t.Fatalf("bundle without a durable row = %+v", queue[1])
	}
	if queue[2].SubmissionStatus != store.SecuritySubmissionStatusSubmitted || queue[2].Program != "immunefi" || queue[2].ExternalReference != "IMM-7" || queue[2].SubmittedBy != "alice" || queue[2].Outcome != store.SecuritySubmissionOutcomeAccepted {
		t.Fatalf("submitted queue item = %+v", queue[2])
	}
	visible, err := s.ListSecuritySubmissionQueue(ctx, namespace, []string{"other"})
	if err != nil || len(visible) != 3 {
		t.Fatalf("queue with hidden scan = %d items err=%v, want 3", len(visible), err)
	}
	for _, item := range visible {
		if item.ScanName == "other" {
			t.Fatalf("hidden scan leaked into the queue: %+v", item)
		}
	}

	rollup, err := s.GetSecuritySubmissionPrecisionRollup(ctx, namespace, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rollup.Total.Submitted != 1 || rollup.Total.Accepted != 1 || len(rollup.ByProgram) != 1 || rollup.ByProgram[0].Key != "immunefi" || len(rollup.ByWorkflow) != 1 || rollup.ByWorkflow[0].Key != "bounty" {
		t.Fatalf("rollup = %+v, want the single filed report and no packaged rows", rollup)
	}
	since := now.Add(-30 * time.Minute)
	if windowed, err := s.GetSecuritySubmissionPrecisionRollup(ctx, namespace, &since, nil); err != nil || windowed.Total.Submitted != 0 {
		t.Fatalf("windowed rollup = %+v err=%v", windowed, err)
	}
	if excluded, err := s.GetSecuritySubmissionPrecisionRollup(ctx, namespace, nil, []string{"target"}); err != nil || excluded.Total.Submitted != 0 {
		t.Fatalf("rollup with hidden target = %+v err=%v", excluded, err)
	}
	if precision, err := s.GetSecuritySubmissionPrecision(ctx, namespace, target.ID, "bounty", nil); err != nil || precision.Submitted != 1 || precision.Accepted != 1 {
		t.Fatalf("precision = %+v err=%v", precision, err)
	}
	if _, err := s.GetSecurityResearchSubmission(ctx, namespace, uuid.New()); !errors.Is(err, store.ErrSecurityResearchSubmissionNotFound) {
		t.Fatalf("missing submission error = %v", err)
	}
}
