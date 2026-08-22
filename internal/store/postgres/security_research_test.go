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
	})
	if err != nil || !created {
		t.Fatalf("hypothesis %s: created=%v err=%v", key, created, err)
	}
	return value
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
	supported, err := s.TransitionSecurityResearchHypothesis(ctx, namespace, h1.ID, store.SecurityHypothesisTransition{ExpectedVersion: 2, ToStatus: store.SecurityHypothesisSupported, Result: store.SecurityHypothesisResultPositive, Rationale: "evidence", IdempotencyKey: "support"})
	if err != nil {
		t.Fatal(err)
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

	if err := s.MarkSecurityResearchSubmissionSubmitted(ctx, namespace, replacement.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
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
