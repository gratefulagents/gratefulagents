package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

type fakeSecurityResearchStore struct {
	*bountyLaneStore
	store.SecurityResearchStore

	target       *store.SecurityResearchTarget
	revision     *store.SecurityResearchRevision
	dossiers     map[string]*store.SecurityResearchDossier
	hypotheses   map[string]*store.SecurityResearchHypothesis
	transitions  map[string]*store.SecurityResearchHypothesis
	coverage     map[string]*store.SecurityResearchCoverage
	sweeps       map[string]*store.SecurityResearchVariantSweep
	completions  map[string]*store.SecurityResearchVariantSweep
	submissions  map[string]*store.SecurityResearchSubmission
	reservations map[string]*store.SecuritySubmissionReservationResult
	decisions    map[string]*store.SecurityResearchDecisionSnapshot
	precision    store.SecuritySubmissionPrecision
	exhausted    bool

	lastNamespace string
	lastActor     string
}

func newFakeSecurityResearchStore() *fakeSecurityResearchStore {
	return &fakeSecurityResearchStore{
		bountyLaneStore: newBountyLaneStore(),
		dossiers:        map[string]*store.SecurityResearchDossier{},
		hypotheses:      map[string]*store.SecurityResearchHypothesis{},
		transitions:     map[string]*store.SecurityResearchHypothesis{},
		coverage:        map[string]*store.SecurityResearchCoverage{},
		sweeps:          map[string]*store.SecurityResearchVariantSweep{},
		completions:     map[string]*store.SecurityResearchVariantSweep{},
		submissions:     map[string]*store.SecurityResearchSubmission{},
		reservations:    map[string]*store.SecuritySubmissionReservationResult{},
		decisions:       map[string]*store.SecurityResearchDecisionSnapshot{},
	}
}

func (s *fakeSecurityResearchStore) UpsertSecurityResearchTarget(_ context.Context, value *store.SecurityResearchTarget) (*store.SecurityResearchTarget, error) {
	s.lastNamespace = value.Namespace
	if s.target == nil {
		stored := *value
		stored.ID = uuid.New()
		s.target = &stored
	}
	copy := *s.target
	return &copy, nil
}

func (s *fakeSecurityResearchStore) BindSecurityResearchRevision(_ context.Context, namespace string, value *store.SecurityResearchRevision) (*store.SecurityResearchRevision, bool, error) {
	s.lastNamespace = namespace
	created := s.revision == nil
	if created {
		stored := *value
		stored.ID = uuid.New()
		s.revision = &stored
	}
	copy := *s.revision
	return &copy, created, nil
}

func (s *fakeSecurityResearchStore) GetLatestSecurityResearchDossier(_ context.Context, namespace string, revisionID uuid.UUID) (*store.SecurityResearchDossier, error) {
	s.lastNamespace = namespace
	var latest *store.SecurityResearchDossier
	for _, value := range s.dossiers {
		if value.RevisionID == revisionID && (latest == nil || value.Version > latest.Version) {
			copy := *value
			latest = &copy
		}
	}
	return latest, nil
}

func (s *fakeSecurityResearchStore) AmendSecurityResearchDossier(_ context.Context, namespace string, value *store.SecurityResearchDossier) (*store.SecurityResearchDossier, bool, error) {
	s.lastNamespace, s.lastActor = namespace, value.Actor
	if existing := s.dossiers[value.IdempotencyKey]; existing != nil {
		copy := *existing
		return &copy, false, nil
	}
	stored := *value
	stored.ID = uuid.New()
	stored.Version = int32(len(s.dossiers) + 1)
	s.dossiers[value.IdempotencyKey] = &stored
	copy := stored
	return &copy, true, nil
}

func (s *fakeSecurityResearchStore) CreateSecurityResearchHypothesis(_ context.Context, namespace string, value *store.SecurityResearchHypothesis) (*store.SecurityResearchHypothesis, bool, error) {
	s.lastNamespace = namespace
	if existing := s.hypotheses[value.IdempotencyKey]; existing != nil {
		copy := *existing
		return &copy, false, nil
	}
	stored := *value
	stored.ID = uuid.New()
	stored.Version = 1
	s.hypotheses[value.IdempotencyKey] = &stored
	copy := stored
	return &copy, true, nil
}

func (s *fakeSecurityResearchStore) ListSecurityResearchHypotheses(_ context.Context, namespace string, revisionID uuid.UUID) ([]store.SecurityResearchHypothesis, error) {
	s.lastNamespace = namespace
	var values []store.SecurityResearchHypothesis
	for _, value := range s.hypotheses {
		if value.RevisionID == revisionID {
			values = append(values, *value)
		}
	}
	return values, nil
}

func (s *fakeSecurityResearchStore) TransitionSecurityResearchHypothesis(_ context.Context, namespace string, id uuid.UUID, transition store.SecurityHypothesisTransition) (*store.SecurityResearchHypothesis, error) {
	s.lastNamespace, s.lastActor = namespace, transition.Actor
	if existing := s.transitions[transition.IdempotencyKey]; existing != nil {
		copy := *existing
		return &copy, nil
	}
	for _, value := range s.hypotheses {
		if value.ID == id {
			value.Status, value.Result, value.Version = transition.ToStatus, transition.Result, value.Version+1
			copy := *value
			s.transitions[transition.IdempotencyKey] = &copy
			return &copy, nil
		}
	}
	return nil, store.ErrSecurityResearchHypothesisNotFound
}

func (s *fakeSecurityResearchStore) ReopenSecurityResearchHypothesis(ctx context.Context, namespace string, id uuid.UUID, transition store.SecurityHypothesisTransition) (*store.SecurityResearchHypothesis, error) {
	transition.ToStatus = store.SecurityHypothesisInvestigating
	transition.Result = store.SecurityHypothesisResultPending
	return s.TransitionSecurityResearchHypothesis(ctx, namespace, id, transition)
}

func (s *fakeSecurityResearchStore) RecordSecurityResearchCoverage(_ context.Context, namespace string, value *store.SecurityResearchCoverage) (*store.SecurityResearchCoverage, bool, error) {
	s.lastNamespace, s.lastActor = namespace, value.Actor
	if existing := s.coverage[value.IdempotencyKey]; existing != nil {
		copy := *existing
		return &copy, false, nil
	}
	stored := *value
	stored.ID = uuid.New()
	s.coverage[value.IdempotencyKey] = &stored
	copy := stored
	return &copy, true, nil
}

func (s *fakeSecurityResearchStore) ListSecurityResearchCoverage(_ context.Context, namespace string, revisionID uuid.UUID) ([]store.SecurityResearchCoverage, error) {
	s.lastNamespace = namespace
	var values []store.SecurityResearchCoverage
	for _, value := range s.coverage {
		if value.RevisionID == revisionID {
			values = append(values, *value)
		}
	}
	return values, nil
}

func (s *fakeSecurityResearchStore) CreateSecurityResearchVariantSweep(_ context.Context, namespace string, value *store.SecurityResearchVariantSweep) (*store.SecurityResearchVariantSweep, bool, error) {
	s.lastNamespace = namespace
	if existing := s.sweeps[value.IdempotencyKey]; existing != nil {
		copy := *existing
		return &copy, false, nil
	}
	stored := *value
	stored.ID = uuid.New()
	s.sweeps[value.IdempotencyKey] = &stored
	copy := stored
	return &copy, true, nil
}

func (s *fakeSecurityResearchStore) CompleteSecurityResearchVariantSweep(_ context.Context, namespace string, id uuid.UUID, status string, result json.RawMessage, actor, idempotencyKey string) (*store.SecurityResearchVariantSweep, error) {
	s.lastNamespace, s.lastActor = namespace, actor
	if existing := s.completions[idempotencyKey]; existing != nil {
		copy := *existing
		return &copy, nil
	}
	for _, value := range s.sweeps {
		if value.ID == id {
			now := time.Now().UTC()
			value.Status, value.Result, value.CompletedAt = status, result, &now
			copy := *value
			s.completions[idempotencyKey] = &copy
			return &copy, nil
		}
	}
	return nil, store.ErrSecurityResearchSweepNotFound
}

func (s *fakeSecurityResearchStore) ListSecurityResearchVariantSweeps(_ context.Context, namespace string, revisionID uuid.UUID) ([]store.SecurityResearchVariantSweep, error) {
	s.lastNamespace = namespace
	var values []store.SecurityResearchVariantSweep
	for _, value := range s.sweeps {
		if value.RevisionID == revisionID {
			values = append(values, *value)
		}
	}
	return values, nil
}

func (s *fakeSecurityResearchStore) CreateSecurityResearchSubmission(_ context.Context, namespace string, value *store.SecurityResearchSubmission) (*store.SecurityResearchSubmission, bool, error) {
	s.lastNamespace = namespace
	key := value.Workflow + "|" + value.CandidateKey
	if existing := s.submissions[key]; existing != nil {
		copy := *existing
		return &copy, false, nil
	}
	stored := *value
	stored.ID = uuid.New()
	s.submissions[key] = &stored
	copy := stored
	return &copy, true, nil
}

func (s *fakeSecurityResearchStore) ReserveSecurityResearchSubmission(_ context.Context, namespace string, request store.SecuritySubmissionReservationRequest) (*store.SecuritySubmissionReservationResult, error) {
	s.lastNamespace = namespace
	if existing := s.reservations[request.IdempotencyKey]; existing != nil {
		copy := *existing
		return &copy, nil
	}
	result := &store.SecuritySubmissionReservationResult{Reserved: !s.exhausted, Limit: request.BudgetLimit}
	if s.exhausted {
		result.Used = request.BudgetLimit
	} else {
		result.Used = 1
		result.Reservation = &store.SecuritySubmissionReservation{ID: uuid.New(), SubmissionID: request.SubmissionID, Workflow: request.Workflow, PeriodDays: request.PeriodDays, BudgetLimit: request.BudgetLimit, IdempotencyKey: request.IdempotencyKey}
	}
	s.reservations[request.IdempotencyKey] = result
	copy := *result
	return &copy, nil
}

func (s *fakeSecurityResearchStore) MarkSecurityResearchSubmissionSubmitted(_ context.Context, namespace string, submissionID uuid.UUID, submittedAt time.Time) error {
	s.lastNamespace = namespace
	for _, submission := range s.submissions {
		if submission.ID == submissionID {
			submission.Status = "submitted"
			submission.SubmittedAt = &submittedAt
			return nil
		}
	}
	return store.ErrSecurityResearchSubmissionNotFound
}

func (s *fakeSecurityResearchStore) GetSecuritySubmissionPrecision(_ context.Context, namespace string, _ uuid.UUID, _ string, _ *time.Time) (*store.SecuritySubmissionPrecision, error) {
	s.lastNamespace = namespace
	copy := s.precision
	return &copy, nil
}

func (s *fakeSecurityResearchStore) CreateSecurityResearchDecisionSnapshot(_ context.Context, namespace string, value *store.SecurityResearchDecisionSnapshot) (*store.SecurityResearchDecisionSnapshot, bool, error) {
	s.lastNamespace = namespace
	if existing := s.decisions[value.IdempotencyKey]; existing != nil {
		copy := *existing
		return &copy, false, nil
	}
	stored := *value
	stored.ID = uuid.New()
	s.decisions[value.IdempotencyKey] = &stored
	copy := stored
	return &copy, true, nil
}

func researchToolRegistry(t *testing.T, researchStore *fakeSecurityResearchStore, scanCtx SecurityScanContext) *Registry {
	t.Helper()
	registry := newSecurityTestRegistryWithCtx(t, researchStore, nil, scanCtx)
	return registry
}

func TestSecurityResearchToolsUseTrustedContextAndAreIdempotent(t *testing.T) {
	researchStore := newFakeSecurityResearchStore()
	scanCtx := bountyLaneContext("trusted-actor", "", bountyLaneProgram())
	registry := researchToolRegistry(t, researchStore, scanCtx)
	for _, name := range []string{
		"get_security_research_context", "amend_security_dossier", "create_security_hypothesis",
		"transition_security_hypothesis", "record_security_coverage", "create_security_variant_sweep",
		"complete_security_variant_sweep", "get_security_campaign_status",
	} {
		if registry.Get(name) == nil {
			t.Fatalf("tool %q was not registered", name)
		}
	}

	input := `{"hypothesis_key":"auth","title":"Authorization","invariant":"only owner withdraws","detail":{"falsifier":"non-owner withdraws"},"idempotency_key":"hyp-1","namespace":"attacker","actor":"attacker"}`
	first := execTool(t, registry, "create_security_hypothesis", input)
	second := execTool(t, registry, "create_security_hypothesis", input)
	if first.IsError || second.IsError || len(researchStore.hypotheses) != 1 {
		t.Fatalf("idempotent hypothesis creation failed: first=%+v second=%+v count=%d", first, second, len(researchStore.hypotheses))
	}
	if researchStore.lastNamespace != scanCtx.Namespace || researchStore.target.TargetKey != scanCtx.ScanName || researchStore.revision.Revision != scanCtx.Revision {
		t.Fatalf("store scope = namespace %q target %q revision %q; want trusted context %+v", researchStore.lastNamespace, researchStore.target.TargetKey, researchStore.revision.Revision, scanCtx)
	}
	var hypothesis *store.SecurityResearchHypothesis
	for _, value := range researchStore.hypotheses {
		hypothesis = value
	}
	transition := `{"hypothesis_id":"` + hypothesis.ID.String() + `","expected_version":1,"to_status":"investigating","result":"pending","rationale":"begin test","detail":{},"idempotency_key":"transition-1","actor":"attacker"}`
	result := execTool(t, registry, "transition_security_hypothesis", transition)
	if result.IsError || researchStore.lastActor != scanCtx.RunName {
		t.Fatalf("transition = %+v, actor = %q; want trusted actor %q", result, researchStore.lastActor, scanCtx.RunName)
	}

	status := execTool(t, registry, "get_security_campaign_status", `{}`)
	if status.IsError || !strings.Contains(status.Content, `"submitted":0`) || !strings.Contains(status.Content, `"investigating":1`) {
		t.Fatalf("campaign status did not expose store summaries: %+v", status)
	}
}

func seedResearchBounty(t *testing.T, researchStore *fakeSecurityResearchStore, scanCtx SecurityScanContext) store.SecurityFindingRecord {
	t.Helper()
	finding := store.SecurityFindingRecord{
		ID: uuid.New(), ScanID: uuid.New(), Namespace: scanCtx.Namespace, ScanName: scanCtx.ScanName,
		RunName: bountyLaneScannerRun, ExecutionID: scanCtx.ExecutionID, Repository: scanCtx.Repository,
		Revision: scanCtx.Revision, Fingerprint: "research-bounty-fp", Title: "Confirmed issue",
		Severity: "critical", Score: 9.8, Status: store.SecurityFindingStatusConfirmed,
	}
	researchStore.findings = append(researchStore.findings, &finding)
	candidate := securityPoCCandidate{Setup: "setup", Command: "go test ./...", ExpectedOutput: "fail", ObservedOutput: "fail", Teardown: "none", Environment: "test", Files: []securityPoCFile{{Path: "repro_test.go", Content: "package repro"}}}
	candidateRaw, _ := json.Marshal(candidate)
	digest := sha256.Sum256(candidateRaw)
	digestHex := hex.EncodeToString(digest[:])
	validationRaw, _ := json.Marshal(securityPoCValidation{Confirmed: true, CandidateSHA256: digestHex, Command: candidate.Command, ObservedOutput: candidate.ObservedOutput, Reason: "confirmed"})
	researchStore.artifacts[bountyArtifactKey(finding.ID, scanCtx.ExecutionID, store.SecurityFindingArtifactPoCCandidate)] = store.SecurityFindingArtifact{FindingID: finding.ID, ExecutionID: scanCtx.ExecutionID, Kind: store.SecurityFindingArtifactPoCCandidate, Content: candidateRaw, SHA256: digestHex, ActorRun: bountyLaneBuilderRun}
	researchStore.artifacts[bountyArtifactKey(finding.ID, scanCtx.ExecutionID, store.SecurityFindingArtifactPoCValidation)] = store.SecurityFindingArtifact{FindingID: finding.ID, ExecutionID: scanCtx.ExecutionID, Kind: store.SecurityFindingArtifactPoCValidation, Content: validationRaw, Status: "confirmed", ActorRun: bountyLaneValidatorRn}
	return finding
}

func TestExactRevisionFindingAllowsAssignedPostScriptFindingAcrossExecutionRows(t *testing.T) {
	researchStore := newFakeSecurityResearchStore()
	scanCtx := bountyLaneContext("post-script", "assigned-fingerprint", bountyLaneProgram())
	finding := store.SecurityFindingRecord{
		ID: uuid.New(), Namespace: scanCtx.Namespace, ScanName: scanCtx.ScanName,
		ExecutionID: "original-execution-row", Repository: scanCtx.Repository, Revision: scanCtx.Revision,
		Fingerprint: scanCtx.PostScriptFingerprint,
	}
	researchStore.findings = append(researchStore.findings, &finding)
	registry := researchToolRegistry(t, researchStore, scanCtx)
	state := securityTestState(t, registry)
	if _, err := state.exactRevisionFinding(context.Background(), finding.ID); err != nil {
		t.Fatalf("assigned post-script finding was rejected: %v", err)
	}
	finding.Fingerprint = "different-fingerprint"
	if _, err := state.exactRevisionFinding(context.Background(), finding.ID); err == nil {
		t.Fatal("unassigned finding from a different execution row was accepted")
	}

	scanCtx.ExecutionID, scanCtx.PostScriptFingerprint = "", ""
	finding.ExecutionID, finding.Fingerprint, finding.RunName = "", "", "different-run"
	registry = researchToolRegistry(t, researchStore, scanCtx)
	state = securityTestState(t, registry)
	if _, err := state.exactRevisionFinding(context.Background(), finding.ID); err == nil {
		t.Fatal("finding from another fallback run was accepted with empty fingerprints")
	}
	finding.RunName = scanCtx.RunName
	if _, err := state.exactRevisionFinding(context.Background(), finding.ID); err != nil {
		t.Fatalf("finding from the same fallback run was rejected: %v", err)
	}
}

func researchBountyRegistry(t *testing.T, findingStore store.SecurityFindingStore, blobs SecurityBountyBlobStore, scanCtx SecurityScanContext) *Registry {
	t.Helper()
	registry := newSecurityTestRegistryWithCtx(t, findingStore, nil, scanCtx)
	RegisterSecurityBountyArtifactTools(registry, securityTestState(t, registry), blobs, nil)
	return registry
}

func addCompletedResearchSweep(t *testing.T, researchStore *fakeSecurityResearchStore, registry *Registry, findingID uuid.UUID) {
	t.Helper()
	created := execTool(t, registry, "create_security_variant_sweep", `{"finding_id":"`+findingID.String()+`","root_cause":"unsafe state ordering","scope":{"repository":true},"status":"running","idempotency_key":"sweep-1"}`)
	if created.IsError {
		t.Fatalf("creating sweep: %s", created.Content)
	}
	var sweep *store.SecurityResearchVariantSweep
	for _, value := range researchStore.sweeps {
		sweep = value
	}
	completed := execTool(t, registry, "complete_security_variant_sweep", `{"sweep_id":"`+sweep.ID.String()+`","status":"completed","result":{"searched_scope":["handlers"],"methods":["grep"],"evidence":["auth.go:10"],"summary":"no siblings found"},"idempotency_key":"complete-1"}`)
	if completed.IsError {
		t.Fatalf("completing sweep: %s", completed.Content)
	}
}

func TestSecurityBountySubmissionRequiresSweepAndReservesPeriodIdempotently(t *testing.T) {
	researchStore := newFakeSecurityResearchStore()
	scanCtx := bountyLaneContext(bountyLaneReportRun, "research-bounty-fp", bountyLaneProgram())
	scanCtx.SubmissionBudget, scanCtx.SubmissionBudgetPeriodDays = 1, 30
	finding := seedResearchBounty(t, researchStore, scanCtx)
	blobs := &bountyLaneBlobs{}
	registry := researchBountyRegistry(t, researchStore, blobs, scanCtx)

	blocked := execTool(t, registry, "save_security_bounty_submission", bountyLaneSubmissionInput(t, bountyLaneCriticalImp))
	if !blocked.IsError || !strings.Contains(blocked.Content, "completed variant sweep") || len(researchStore.submissions) != 0 {
		t.Fatalf("submission bypassed sweep gate: %+v submissions=%d", blocked, len(researchStore.submissions))
	}
	addCompletedResearchSweep(t, researchStore, registry, finding.ID)
	first := execTool(t, registry, "save_security_bounty_submission", bountyLaneSubmissionInput(t, bountyLaneCriticalImp))
	second := execTool(t, registry, "save_security_bounty_submission", bountyLaneSubmissionInput(t, bountyLaneCriticalImp))
	if first.IsError || second.IsError {
		t.Fatalf("durable packaging failed: first=%+v second=%+v", first, second)
	}
	if len(researchStore.submissions) != 1 || len(researchStore.reservations) != 1 || len(researchStore.decisions) != 1 {
		t.Fatalf("repeat duplicated durable state: submissions=%d reservations=%d decisions=%d", len(researchStore.submissions), len(researchStore.reservations), len(researchStore.decisions))
	}
	if !strings.Contains(first.Content, "uploaded") || len(blobs.puts) != 2 {
		t.Fatalf("expected deterministic repeat packaging, result=%+v uploads=%d", first, len(blobs.puts))
	}
}

func TestSecurityBountyPeriodExhaustionRetainsCandidateWithoutToolError(t *testing.T) {
	researchStore := newFakeSecurityResearchStore()
	researchStore.exhausted = true
	scanCtx := bountyLaneContext(bountyLaneReportRun, "research-bounty-fp", bountyLaneProgram())
	scanCtx.SubmissionBudget, scanCtx.SubmissionBudgetPeriodDays = 1, 30
	finding := seedResearchBounty(t, researchStore, scanCtx)
	blobs := &bountyLaneBlobs{}
	registry := researchBountyRegistry(t, researchStore, blobs, scanCtx)
	addCompletedResearchSweep(t, researchStore, registry, finding.ID)

	result := execTool(t, registry, "save_security_bounty_submission", bountyLaneSubmissionInput(t, bountyLaneCriticalImp))
	if result.IsError || !strings.Contains(result.Content, "retained as candidate") {
		t.Fatalf("budget exhaustion was not a normal candidate result: %+v", result)
	}
	if len(researchStore.submissions) != 1 || len(researchStore.decisions) != 1 || len(blobs.puts) != 0 {
		t.Fatalf("exhausted result state: submissions=%d decisions=%d uploads=%d", len(researchStore.submissions), len(researchStore.decisions), len(blobs.puts))
	}
	for _, decision := range researchStore.decisions {
		if decision.Decision != "retain" {
			t.Fatalf("decision = %q, want retain", decision.Decision)
		}
	}
}

func TestSecurityBountyLegacyFallbackAndPeriodFailClosed(t *testing.T) {
	legacyStore := newBountyLaneStore()
	scanCtx := bountyLaneContext(bountyLaneReportRun, "legacy-fp", bountyLaneProgram())
	finding := store.SecurityFindingRecord{ID: uuid.New(), ScanID: uuid.New(), Namespace: scanCtx.Namespace, ScanName: scanCtx.ScanName, ExecutionID: scanCtx.ExecutionID, Repository: scanCtx.Repository, Revision: scanCtx.Revision, Fingerprint: "legacy-fp", Severity: "critical", Score: 9, Status: store.SecurityFindingStatusTriaged}
	legacyStore.findings = append(legacyStore.findings, &finding)
	blobs := &bountyLaneBlobs{}
	registry := researchBountyRegistry(t, legacyStore, blobs, scanCtx)
	result := execTool(t, registry, "save_security_bounty_submission", bountyLaneSubmissionInput(t, bountyLaneCriticalImp))
	if result.IsError || len(blobs.puts) != 1 {
		t.Fatalf("legacy scan-wide fallback failed: %+v uploads=%d", result, len(blobs.puts))
	}
	if strings.Contains(strings.ToLower(result.Content), "period") {
		t.Fatalf("legacy fallback claimed period enforcement: %s", result.Content)
	}
	if registry.Get("get_security_research_context") != nil {
		t.Fatal("research tools registered without a research store")
	}

	scanCtx.SubmissionBudgetPeriodDays = 30
	registry = researchBountyRegistry(t, legacyStore, blobs, scanCtx)
	closed := execTool(t, registry, "save_security_bounty_submission", bountyLaneSubmissionInput(t, bountyLaneCriticalImp))
	if !closed.IsError || !strings.Contains(closed.Content, "durable security research storage") {
		t.Fatalf("configured rolling period did not fail closed without durable storage: %+v", closed)
	}
}
