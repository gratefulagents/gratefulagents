package dashboard

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

type mockSecurityResearchStore struct {
	*mockSecurityStore
	revision      store.SecurityResearchRevision
	dossier       *store.SecurityResearchDossier
	hypotheses    []store.SecurityResearchHypothesis
	coverage      []store.SecurityResearchCoverage
	sweeps        []store.SecurityResearchVariantSweep
	submissions   []store.SecurityResearchSubmission
	outcomes      map[uuid.UUID][]store.SecuritySubmissionOutcomeEvent
	precision     store.SecuritySubmissionPrecision
	revisionCalls int
}

func newMockSecurityResearchStore() *mockSecurityResearchStore {
	return &mockSecurityResearchStore{mockSecurityStore: newMockSecurityStore(), outcomes: map[uuid.UUID][]store.SecuritySubmissionOutcomeEvent{}}
}

func (m *mockSecurityResearchStore) UpsertSecurityResearchTarget(_ context.Context, value *store.SecurityResearchTarget) (*store.SecurityResearchTarget, error) {
	return value, nil
}
func (m *mockSecurityResearchStore) BindSecurityResearchRevision(_ context.Context, _ string, value *store.SecurityResearchRevision) (*store.SecurityResearchRevision, bool, error) {
	return value, true, nil
}
func (m *mockSecurityResearchStore) GetSecurityResearchRevision(_ context.Context, _, _, _ string) (*store.SecurityResearchRevision, error) {
	m.revisionCalls++
	return &m.revision, nil
}
func (m *mockSecurityResearchStore) AmendSecurityResearchDossier(_ context.Context, _ string, value *store.SecurityResearchDossier) (*store.SecurityResearchDossier, bool, error) {
	value.ID = uuid.New()
	value.CreatedAt = time.Now()
	m.dossier = value
	return value, true, nil
}
func (m *mockSecurityResearchStore) GetLatestSecurityResearchDossier(_ context.Context, _ string, _ uuid.UUID) (*store.SecurityResearchDossier, error) {
	return m.dossier, nil
}
func (m *mockSecurityResearchStore) CreateSecurityResearchHypothesis(_ context.Context, _ string, value *store.SecurityResearchHypothesis) (*store.SecurityResearchHypothesis, bool, error) {
	value.ID, value.Version, value.CreatedAt, value.UpdatedAt = uuid.New(), 1, time.Now(), time.Now()
	m.hypotheses = append(m.hypotheses, *value)
	return value, true, nil
}
func (m *mockSecurityResearchStore) TransitionSecurityResearchHypothesis(_ context.Context, _ string, id uuid.UUID, transition store.SecurityHypothesisTransition) (*store.SecurityResearchHypothesis, error) {
	return m.changeHypothesis(id, transition), nil
}
func (m *mockSecurityResearchStore) ReopenSecurityResearchHypothesis(_ context.Context, _ string, id uuid.UUID, transition store.SecurityHypothesisTransition) (*store.SecurityResearchHypothesis, error) {
	transition.ToStatus, transition.Result = store.SecurityHypothesisInvestigating, store.SecurityHypothesisResultPending
	return m.changeHypothesis(id, transition), nil
}
func (m *mockSecurityResearchStore) changeHypothesis(id uuid.UUID, transition store.SecurityHypothesisTransition) *store.SecurityResearchHypothesis {
	for i := range m.hypotheses {
		if m.hypotheses[i].ID == id {
			m.hypotheses[i].Status, m.hypotheses[i].Result = transition.ToStatus, transition.Result
			m.hypotheses[i].Version++
			return &m.hypotheses[i]
		}
	}
	return &store.SecurityResearchHypothesis{ID: id, RevisionID: m.revision.ID, Status: transition.ToStatus, Result: transition.Result, Version: transition.ExpectedVersion + 1}
}
func (m *mockSecurityResearchStore) AddSecurityResearchHypothesisLineage(context.Context, string, store.SecurityHypothesisLineage, string) error {
	return nil
}
func (m *mockSecurityResearchStore) ListSecurityResearchHypotheses(context.Context, string, uuid.UUID) ([]store.SecurityResearchHypothesis, error) {
	return m.hypotheses, nil
}
func (m *mockSecurityResearchStore) ListSecurityResearchHypothesisEvents(context.Context, string, uuid.UUID) ([]store.SecurityResearchHypothesisEvent, error) {
	return nil, nil
}
func (m *mockSecurityResearchStore) RecordSecurityResearchCoverage(_ context.Context, _ string, value *store.SecurityResearchCoverage) (*store.SecurityResearchCoverage, bool, error) {
	value.ID, value.CreatedAt = uuid.New(), time.Now()
	m.coverage = append(m.coverage, *value)
	return value, true, nil
}
func (m *mockSecurityResearchStore) ListSecurityResearchCoverage(context.Context, string, uuid.UUID) ([]store.SecurityResearchCoverage, error) {
	return m.coverage, nil
}
func (m *mockSecurityResearchStore) CreateSecurityResearchVariantSweep(_ context.Context, _ string, value *store.SecurityResearchVariantSweep) (*store.SecurityResearchVariantSweep, bool, error) {
	value.ID, value.CreatedAt = uuid.New(), time.Now()
	m.sweeps = append(m.sweeps, *value)
	return value, true, nil
}
func (m *mockSecurityResearchStore) CompleteSecurityResearchVariantSweep(_ context.Context, _ string, id uuid.UUID, status string, result json.RawMessage, _, _ string) (*store.SecurityResearchVariantSweep, error) {
	for i := range m.sweeps {
		if m.sweeps[i].ID == id {
			now := time.Now()
			m.sweeps[i].Status, m.sweeps[i].Result, m.sweeps[i].CompletedAt = status, result, &now
			return &m.sweeps[i], nil
		}
	}
	return nil, store.ErrSecurityResearchSweepNotFound
}
func (m *mockSecurityResearchStore) ListSecurityResearchVariantSweeps(context.Context, string, uuid.UUID) ([]store.SecurityResearchVariantSweep, error) {
	return m.sweeps, nil
}
func (m *mockSecurityResearchStore) ListSecurityResearchVariantSweepEvents(context.Context, string, uuid.UUID) ([]store.SecurityResearchVariantSweepEvent, error) {
	return nil, nil
}
func (m *mockSecurityResearchStore) CreateSecurityResearchSubmission(_ context.Context, _ string, value *store.SecurityResearchSubmission) (*store.SecurityResearchSubmission, bool, error) {
	return value, true, nil
}
func (m *mockSecurityResearchStore) ListSecurityResearchSubmissions(context.Context, string, uuid.UUID) ([]store.SecurityResearchSubmission, error) {
	return m.submissions, nil
}
func (m *mockSecurityResearchStore) ReserveSecurityResearchSubmission(context.Context, string, store.SecuritySubmissionReservationRequest) (*store.SecuritySubmissionReservationResult, error) {
	return nil, nil
}
func (m *mockSecurityResearchStore) MarkSecurityResearchSubmissionSubmitted(context.Context, string, uuid.UUID, time.Time) error {
	return nil
}
func (m *mockSecurityResearchStore) RecordSecuritySubmissionOutcome(_ context.Context, _ string, submissionID uuid.UUID, input store.SecuritySubmissionOutcomeInput) (*store.SecuritySubmissionOutcome, bool, error) {
	event := store.SecuritySubmissionOutcomeEvent{ID: int64(len(m.outcomes[submissionID]) + 1), SubmissionID: submissionID, Outcome: input.Outcome, ExternalReference: input.ExternalReference, Rationale: input.Rationale, Actor: input.Actor, CorrectionOf: input.CorrectionOf, IdempotencyKey: input.IdempotencyKey, CreatedAt: time.Now()}
	m.outcomes[submissionID] = append(m.outcomes[submissionID], event)
	return &store.SecuritySubmissionOutcome{SubmissionID: submissionID, EventID: event.ID, Outcome: event.Outcome, ExternalReference: event.ExternalReference, RecordedAt: event.CreatedAt}, true, nil
}
func (m *mockSecurityResearchStore) ListSecuritySubmissionOutcomeEvents(_ context.Context, _ string, _ uuid.UUID, submissionID uuid.UUID) ([]store.SecuritySubmissionOutcomeEvent, error) {
	return m.outcomes[submissionID], nil
}
func (m *mockSecurityResearchStore) GetSecuritySubmissionPrecision(context.Context, string, uuid.UUID, string, *time.Time) (*store.SecuritySubmissionPrecision, error) {
	return &m.precision, nil
}
func (m *mockSecurityResearchStore) CreateSecurityResearchDecisionSnapshot(_ context.Context, _ string, value *store.SecurityResearchDecisionSnapshot) (*store.SecurityResearchDecisionSnapshot, bool, error) {
	return value, true, nil
}

var _ store.SecurityResearchStore = (*mockSecurityResearchStore)(nil)

func researchTestScope() *platform.SecurityResearchScope {
	return &platform.SecurityResearchScope{Namespace: "default", TargetKey: "alice-scan", Revision: "abc123"}
}

func seedResearchStore(t *testing.T) *mockSecurityResearchStore {
	t.Helper()
	m := newMockSecurityResearchStore()
	m.revision = store.SecurityResearchRevision{ID: uuid.New(), TargetID: uuid.New(), Revision: "abc123", CreatedAt: time.Now()}
	if err := m.SetResourceOwner(context.Background(), securityScanResourceType, "alice-scan", "default", "alice"); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSecurityCampaignResearchStatusUsesScanVisibilityAndAggregates(t *testing.T) {
	m := seedResearchStore(t)
	m.dossier = &store.SecurityResearchDossier{ID: uuid.New(), RevisionID: m.revision.ID, Version: 3, CreatedAt: time.Now()}
	m.hypotheses = []store.SecurityResearchHypothesis{{Status: store.SecurityHypothesisFalsified, Result: store.SecurityHypothesisResultNegative}, {Status: store.SecurityHypothesisSupported, Result: store.SecurityHypothesisResultPositive}}
	m.coverage = []store.SecurityResearchCoverage{{Verdict: store.SecurityCoverageDisproved}, {Verdict: store.SecurityCoverageDisproved}}
	m.sweeps = []store.SecurityResearchVariantSweep{{Status: store.SecurityVariantSweepCompleted}}
	m.precision = store.SecuritySubmissionPrecision{Submitted: 4, Accepted: 2, Duplicate: 1, Rejected: 1}
	srv := newSecurityTestServer(t, m)
	req := &platform.GetSecurityCampaignResearchStatusRequest{Scope: researchTestScope(), Workflow: "bounty"}

	if _, err := srv.GetSecurityCampaignResearchStatus(actorContext("bob", "member", "", ""), req); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden target error = %v, want NotFound", err)
	}
	if m.revisionCalls != 0 {
		t.Fatalf("hidden target reached research store %d times", m.revisionCalls)
	}
	got, err := srv.GetSecurityCampaignResearchStatus(actorContext("alice", "member", "", ""), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetDossierVersion() != 3 || got.GetHypothesisStatusCounts()[store.SecurityHypothesisFalsified] != 1 || got.GetCoverageVerdictCounts()[store.SecurityCoverageDisproved] != 2 || got.GetPrecision().GetAccepted() != 2 {
		t.Fatalf("unexpected status: %+v", got)
	}
}

func TestRecordSecurityResearchCoverageRejectsInvalidEnums(t *testing.T) {
	m := seedResearchStore(t)
	srv := newSecurityTestServer(t, m)
	request := func(dimension, verdict string) *platform.RecordSecurityResearchCoverageRequest {
		return &platform.RecordSecurityResearchCoverageRequest{
			Scope: researchTestScope(), SubjectKey: "balances", Dimension: dimension,
			Verdict: verdict, BoundsJson: `{}`, EvidenceJson: `[]`, IdempotencyKey: "coverage-1",
		}
	}
	if _, err := srv.RecordSecurityResearchCoverage(actorContext("alice", "member", "", ""), request("unknown", store.SecurityCoverageAdequatelyTested)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid dimension error = %v, want InvalidArgument", err)
	}
	if _, err := srv.RecordSecurityResearchCoverage(actorContext("alice", "member", "", ""), request(store.SecurityCoverageInvariant, "unknown")); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid verdict error = %v, want InvalidArgument", err)
	}
	if len(m.coverage) != 0 {
		t.Fatalf("invalid requests persisted %d coverage records", len(m.coverage))
	}
}

func TestSecurityResearchListsAreBounded(t *testing.T) {
	m := seedResearchStore(t)
	for range 3 {
		m.hypotheses = append(m.hypotheses, store.SecurityResearchHypothesis{ID: uuid.New(), RevisionID: m.revision.ID, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	}
	srv := newSecurityTestServer(t, m)
	got, err := srv.ListSecurityResearchHypotheses(actorContext("alice", "member", "", ""), &platform.ListSecurityResearchHypothesesRequest{Scope: researchTestScope(), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GetHypotheses()) != 2 || !got.GetTruncated() {
		t.Fatalf("response = %d hypotheses, truncated=%v", len(got.GetHypotheses()), got.GetTruncated())
	}
}

func TestListSecurityResearchSubmissionsUsesScanVisibilityAndAttachesOutcome(t *testing.T) {
	m := seedResearchStore(t)
	findingID, outcomeAt, submittedAt := uuid.New(), time.Now().Add(-time.Minute), time.Now().Add(-time.Hour)
	m.submissions = []store.SecurityResearchSubmission{
		{ID: uuid.New(), RevisionID: m.revision.ID, FindingID: &findingID, FindingFingerprint: "fp-1", FindingTitle: "Reentrancy in withdraw", Workflow: "bounty", CandidateKey: "fp-1", Rank: 1, Status: "submitted", CreatedAt: time.Now(), SubmittedAt: &submittedAt, Outcome: store.SecuritySubmissionOutcomeAccepted, OutcomeRecordedAt: &outcomeAt},
		{ID: uuid.New(), RevisionID: m.revision.ID, Workflow: "bounty", CandidateKey: "fp-2", Rank: 2, Status: "candidate", CreatedAt: time.Now()},
		{ID: uuid.New(), RevisionID: m.revision.ID, Workflow: "bounty", CandidateKey: "fp-3", Rank: 3, Status: "candidate", CreatedAt: time.Now()},
	}
	srv := newSecurityTestServer(t, m)
	req := &platform.ListSecurityResearchSubmissionsRequest{Scope: researchTestScope(), Limit: 2}
	if _, err := srv.ListSecurityResearchSubmissions(actorContext("bob", "member", "", ""), req); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden target error = %v, want NotFound", err)
	}
	got, err := srv.ListSecurityResearchSubmissions(actorContext("alice", "member", "", ""), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GetSubmissions()) != 2 || !got.GetTruncated() {
		t.Fatalf("response = %d submissions, truncated=%v", len(got.GetSubmissions()), got.GetTruncated())
	}
	first := got.GetSubmissions()[0]
	if first.GetId() != m.submissions[0].ID.String() || first.GetFindingId() != findingID.String() || first.GetFindingTitle() != "Reentrancy in withdraw" || first.GetStatus() != "submitted" || first.GetLatestOutcome() != store.SecuritySubmissionOutcomeAccepted || first.GetLatestOutcomeAt() == nil || first.GetSubmittedAt() == nil || first.GetRank() != 1 {
		t.Fatalf("first submission = %+v", first)
	}
	second := got.GetSubmissions()[1]
	if second.GetFindingId() != "" || second.GetLatestOutcome() != "" || second.GetLatestOutcomeAt() != nil || second.GetSubmittedAt() != nil {
		t.Fatalf("second submission = %+v", second)
	}
}

func TestSecurityResearchMutationsUseActorAndCorrectionsAppend(t *testing.T) {
	m := seedResearchStore(t)
	srv := newSecurityTestServer(t, m)
	ctx := actorContext("alice", "member", "", "")

	dossier, err := srv.AmendSecurityResearchDossier(ctx, &platform.AmendSecurityResearchDossierRequest{Scope: researchTestScope(), ContentJson: `{}`, ChangeSummary: "mapped trust boundaries", IdempotencyKey: "dossier-1"})
	if err != nil {
		t.Fatal(err)
	}
	if dossier.GetDossier().GetActor() != "alice" {
		t.Fatalf("dossier actor = %q", dossier.GetDossier().GetActor())
	}
	coverage, err := srv.RecordSecurityResearchCoverage(ctx, &platform.RecordSecurityResearchCoverageRequest{Scope: researchTestScope(), Dimension: store.SecurityCoverageInvariant, SubjectKey: "solvent", Verdict: store.SecurityCoverageAdequatelyTested, BoundsJson: `{}`, EvidenceJson: `[]`, IdempotencyKey: "coverage-1"})
	if err != nil {
		t.Fatal(err)
	}
	if coverage.GetCoverage().GetActor() != "alice" {
		t.Fatalf("coverage actor = %q", coverage.GetCoverage().GetActor())
	}

	submissionID := uuid.New()
	first, err := srv.RecordSecuritySubmissionOutcome(ctx, &platform.RecordSecuritySubmissionOutcomeRequest{Scope: researchTestScope(), SubmissionId: submissionID.String(), Outcome: store.SecuritySubmissionOutcomeAccepted, ExternalReference: "report-1", IdempotencyKey: "outcome-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = srv.CorrectSecuritySubmissionOutcome(ctx, &platform.CorrectSecuritySubmissionOutcomeRequest{Scope: researchTestScope(), SubmissionId: submissionID.String(), CorrectionOf: first.GetOutcome().GetEventId(), Outcome: store.SecuritySubmissionOutcomeDuplicate, ExternalReference: "report-2", Rationale: "triager correction", IdempotencyKey: "outcome-2"})
	if err != nil {
		t.Fatal(err)
	}
	events := m.outcomes[submissionID]
	if len(events) != 2 || events[1].CorrectionOf == nil || *events[1].CorrectionOf != events[0].ID || events[1].Actor != "alice" {
		t.Fatalf("outcome events = %+v", events)
	}
	if _, err := srv.RecordSecuritySubmissionOutcome(context.Background(), &platform.RecordSecuritySubmissionOutcomeRequest{}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated mutation error = %v", err)
	}
}
