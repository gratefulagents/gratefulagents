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

	target            *store.SecurityResearchTarget
	revision          *store.SecurityResearchRevision
	dossiers          map[string]*store.SecurityResearchDossier
	hypotheses        map[string]*store.SecurityResearchHypothesis
	transitions       map[string]*store.SecurityResearchHypothesis
	coverage          map[string]*store.SecurityResearchCoverage
	sweeps            map[string]*store.SecurityResearchVariantSweep
	completions       map[string]*store.SecurityResearchVariantSweep
	submissions       map[string]*store.SecurityResearchSubmission
	reservations      map[string]*store.SecuritySubmissionReservationResult
	decisions         map[string]*store.SecurityResearchDecisionSnapshot
	researchArtifacts map[string]*store.SecurityResearchArtifact
	precision         store.SecuritySubmissionPrecision
	exhausted         bool

	lastNamespace      string
	lastActor          string
	lastArtifactFilter store.SecurityResearchArtifactFilter
}

func newFakeSecurityResearchStore() *fakeSecurityResearchStore {
	return &fakeSecurityResearchStore{
		bountyLaneStore:   newBountyLaneStore(),
		dossiers:          map[string]*store.SecurityResearchDossier{},
		hypotheses:        map[string]*store.SecurityResearchHypothesis{},
		transitions:       map[string]*store.SecurityResearchHypothesis{},
		coverage:          map[string]*store.SecurityResearchCoverage{},
		sweeps:            map[string]*store.SecurityResearchVariantSweep{},
		completions:       map[string]*store.SecurityResearchVariantSweep{},
		submissions:       map[string]*store.SecurityResearchSubmission{},
		reservations:      map[string]*store.SecuritySubmissionReservationResult{},
		decisions:         map[string]*store.SecurityResearchDecisionSnapshot{},
		researchArtifacts: map[string]*store.SecurityResearchArtifact{},
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
		"create_security_research_artifact", "get_security_research_artifacts",
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

func createResearchHypothesis(t *testing.T, researchStore *fakeSecurityResearchStore, registry *Registry, key string) *store.SecurityResearchHypothesis {
	t.Helper()
	input := `{"hypothesis_key":"` + key + `","title":"` + key + `","invariant":"inv","detail":{},"idempotency_key":"hyp-` + key + `"}`
	if result := execTool(t, registry, "create_security_hypothesis", input); result.IsError {
		t.Fatalf("create hypothesis: %s", result.Content)
	}
	for _, value := range researchStore.hypotheses {
		if value.HypothesisKey == key {
			return value
		}
	}
	t.Fatalf("hypothesis %q not stored", key)
	return nil
}

func TestRecordSecurityCoverageRequiresDynamicExperimentForStrongVerdicts(t *testing.T) {
	researchStore := newFakeSecurityResearchStore()
	registry := researchToolRegistry(t, researchStore, bountyLaneContext("trusted-actor", "", bountyLaneProgram()))

	rejected := execTool(t, registry, "record_security_coverage", `{"dimension":"invariant","subject_key":"supply","verdict":"adequately_tested","experiment_kind":"existing_suite","command":"forge test","bounds":{},"evidence":[],"idempotency_key":"c-existing"}`)
	if !rejected.IsError || !strings.Contains(rejected.Content, "existing_suite and static_trace may only support inadequately_tested or not_tested") {
		t.Fatalf("adequately_tested+existing_suite must be rejected: %+v", rejected)
	}
	rejected = execTool(t, registry, "record_security_coverage", `{"dimension":"invariant","subject_key":"supply","verdict":"disproved","experiment_kind":"new_test","bounds":{},"evidence":[],"idempotency_key":"c-nocmd"}`)
	if !rejected.IsError || !strings.Contains(rejected.Content, "requires command") {
		t.Fatalf("disproved without command must be rejected: %+v", rejected)
	}
	rejected = execTool(t, registry, "record_security_coverage", `{"dimension":"invariant","subject_key":"supply","verdict":"not_tested","bounds":{},"evidence":[],"idempotency_key":"c-nokind"}`)
	if !rejected.IsError || !strings.Contains(rejected.Content, "experiment_kind") {
		t.Fatalf("missing experiment_kind must be rejected: %+v", rejected)
	}
	if len(researchStore.coverage) != 0 {
		t.Fatalf("rejected coverage must not be stored: %d", len(researchStore.coverage))
	}

	static := execTool(t, registry, "record_security_coverage", `{"dimension":"invariant","subject_key":"supply","verdict":"inadequately_tested","experiment_kind":"static_trace","bounds":{"scope":"mint"},"evidence":[],"idempotency_key":"c-static"}`)
	if static.IsError {
		t.Fatalf("static_trace + inadequately_tested must be accepted: %s", static.Content)
	}
	dynamic := execTool(t, registry, "record_security_coverage", `{"dimension":"invariant","subject_key":"supply","verdict":"adequately_tested","experiment_kind":"mutant","command":"cd contracts && forge test --match-test testMint","exit_code":1,"observed":"FAIL testMint","bounds":{"scope":"mint"},"evidence":[],"idempotency_key":"c-dynamic"}`)
	if dynamic.IsError {
		t.Fatalf("dynamic kind with command must be accepted: %s", dynamic.Content)
	}
	stored := researchStore.coverage["c-dynamic"]
	if stored == nil {
		t.Fatal("dynamic coverage not stored")
	}
	var bounds struct {
		Scope      string `json:"scope"`
		Experiment struct {
			Kind     string `json:"kind"`
			Command  string `json:"command"`
			ExitCode int    `json:"exit_code"`
			Observed string `json:"observed"`
		} `json:"experiment"`
	}
	if err := json.Unmarshal(stored.Bounds, &bounds); err != nil {
		t.Fatal(err)
	}
	if bounds.Scope != "mint" || bounds.Experiment.Kind != "mutant" || bounds.Experiment.Command != "cd contracts && forge test --match-test testMint" || bounds.Experiment.ExitCode != 1 || bounds.Experiment.Observed != "FAIL testMint" {
		t.Fatalf("experiment not merged into bounds: %s", stored.Bounds)
	}
	if kind := securityCoverageExperimentKind(researchStore.coverage["c-static"].Bounds); kind != "static_trace" {
		t.Fatalf("static coverage kind = %q", kind)
	}

	merged, err := mergeSecurityCoverageExperiment(json.RawMessage(`["a","b"]`), map[string]any{"kind": "fuzz"})
	if err != nil || !strings.Contains(string(merged), `"declared_bounds":["a","b"]`) || !strings.Contains(string(merged), `"experiment":{"kind":"fuzz"}`) {
		t.Fatalf("non-object bounds must be preserved under declared_bounds: %s %v", merged, err)
	}
}

func TestTransitionSecurityHypothesisFalsifiedRequiresExperimentalEvidence(t *testing.T) {
	researchStore := newFakeSecurityResearchStore()
	registry := researchToolRegistry(t, researchStore, bountyLaneContext("trusted-actor", "", bountyLaneProgram()))
	hypothesis := createResearchHypothesis(t, researchStore, registry, "reading-only")

	falsify := func(id, version, detail, key string) Result {
		return execTool(t, registry, "transition_security_hypothesis", `{"hypothesis_id":"`+id+`","expected_version":`+version+`,"to_status":"falsified","result":"negative","rationale":"guard exists","detail":`+detail+`,"idempotency_key":"`+key+`"}`)
	}
	rejected := falsify(hypothesis.ID.String(), "1", `{}`, "f-1")
	if !rejected.IsError || !strings.Contains(rejected.Content, "weakened") || !strings.Contains(rejected.Content, "blocked") {
		t.Fatalf("falsified without evidence must be rejected with guidance: %+v", rejected)
	}
	rejected = falsify(hypothesis.ID.String(), "1", `{"guard_citation":"the onlyOwner modifier"}`, "f-2")
	if !rejected.IsError {
		t.Fatalf("guard_citation without file:line must be rejected: %+v", rejected)
	}
	if hypothesis.Status == store.SecurityHypothesisFalsified {
		t.Fatal("hypothesis was falsified without evidence")
	}
	weakened := execTool(t, registry, "transition_security_hypothesis", `{"hypothesis_id":"`+hypothesis.ID.String()+`","expected_version":1,"to_status":"weakened","result":"negative","rationale":"reading only","detail":{},"idempotency_key":"w-1"}`)
	if weakened.IsError {
		t.Fatalf("weakened must remain available for reading-only refutations: %s", weakened.Content)
	}

	cited := createResearchHypothesis(t, researchStore, registry, "cited")
	if result := falsify(cited.ID.String(), "1", `{"guard_citation":"contracts/Vault.sol:142"}`, "f-3"); result.IsError {
		t.Fatalf("guard_citation with file:line must be accepted: %s", result.Content)
	}

	experimented := createResearchHypothesis(t, researchStore, registry, "experimented")
	coverage := execTool(t, registry, "record_security_coverage", `{"hypothesis_id":"`+experimented.ID.String()+`","dimension":"invariant","subject_key":"supply","verdict":"inadequately_tested","experiment_kind":"static_trace","bounds":{},"evidence":[],"idempotency_key":"c-trace"}`)
	if coverage.IsError {
		t.Fatalf("coverage: %s", coverage.Content)
	}
	if result := falsify(experimented.ID.String(), "1", `{}`, "f-4"); !result.IsError {
		t.Fatalf("static_trace coverage must not satisfy falsification: %+v", result)
	}
	coverage = execTool(t, registry, "record_security_coverage", `{"hypothesis_id":"`+experimented.ID.String()+`","dimension":"invariant","subject_key":"supply","verdict":"disproved","experiment_kind":"new_test","command":"go test ./... -run TestNonOwnerWithdraw","exit_code":0,"bounds":{},"evidence":[],"idempotency_key":"c-new-test"}`)
	if coverage.IsError {
		t.Fatalf("coverage: %s", coverage.Content)
	}
	if result := falsify(experimented.ID.String(), "1", `{}`, "f-5"); result.IsError {
		t.Fatalf("dynamic coverage for the hypothesis must satisfy falsification: %s", result.Content)
	}
	if experimented.Status != store.SecurityHypothesisFalsified {
		t.Fatalf("hypothesis status = %q, want falsified", experimented.Status)
	}
}
