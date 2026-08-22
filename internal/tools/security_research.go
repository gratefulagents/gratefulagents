package tools

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/google/uuid"
	"github.com/gratefulagents/sdk/pkg/agentsdk"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

const defaultSecurityResearchWorkflow = "security-bounty"

type securityResearchContext struct {
	target   *store.SecurityResearchTarget
	revision *store.SecurityResearchRevision
}

func registerSecurityResearchTools(registry *Registry, state *securityScanState) {
	if registry == nil || state == nil || state.researchStore == nil {
		return
	}
	registry.Register(&getSecurityResearchContextTool{state: state})
	registry.Register(&amendSecurityDossierTool{state: state})
	registry.Register(&createSecurityHypothesisTool{state: state})
	registry.Register(&transitionSecurityHypothesisTool{state: state})
	registry.Register(&recordSecurityCoverageTool{state: state})
	registry.Register(&createSecurityVariantSweepTool{state: state})
	registry.Register(&completeSecurityVariantSweepTool{state: state})
	registry.Register(&getSecurityCampaignStatusTool{state: state})
}

func resolveSecurityResearchCheckoutRevision(ctx context.Context, checkoutDir string) (string, error) {
	checkoutDir = strings.TrimSpace(checkoutDir)
	if checkoutDir == "" {
		return "", errors.New("trusted checkout directory is required")
	}
	output, err := exec.CommandContext(ctx, "git", "-C", checkoutDir, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return "", err
	}
	revision := strings.TrimSpace(string(output))
	decoded, err := hex.DecodeString(revision)
	if err != nil || len(decoded) < 20 {
		return "", fmt.Errorf("git HEAD %q is not an immutable commit hash", revision)
	}
	return strings.ToLower(revision), nil
}

func (s *securityScanState) ensureSecurityResearchContext(ctx context.Context) (*securityResearchContext, error) {
	namespace := strings.TrimSpace(s.scanCtx.Namespace)
	targetKey := strings.TrimSpace(s.scanCtx.ScanName)
	locator := strings.TrimSpace(s.scanCtx.Repository)
	revisionValue := strings.TrimSpace(s.scanCtx.Revision)
	if revisionValue == "" {
		resolved, err := resolveSecurityResearchCheckoutRevision(ctx, s.scanCtx.CheckoutDir)
		if err != nil {
			return nil, fmt.Errorf("resolving the immutable checkout revision: %w", err)
		}
		revisionValue = resolved
		s.scanCtx.Revision = resolved
	}
	if namespace == "" || targetKey == "" || locator == "" {
		return nil, fmt.Errorf("trusted run context must provide namespace, scan target, and repository")
	}
	metadata, _ := json.Marshal(map[string]string{
		"scan_name":    s.scanCtx.ScanName,
		"repository":   locator,
		"execution_id": s.scanCtx.ExecutionID,
	})
	target, err := s.researchStore.UpsertSecurityResearchTarget(ctx, &store.SecurityResearchTarget{
		Namespace: namespace,
		TargetKey: targetKey,
		Kind:      "repository",
		Locator:   locator,
		Metadata:  metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("binding trusted research target: %w", err)
	}
	revision, _, err := s.researchStore.BindSecurityResearchRevision(ctx, namespace, &store.SecurityResearchRevision{
		TargetID:  target.ID,
		Revision:  revisionValue,
		SourceURI: locator,
		Metadata:  metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("binding exact research revision: %w", err)
	}
	if revision == nil || revision.TargetID != target.ID || revision.Revision != revisionValue {
		return nil, fmt.Errorf("research store did not bind the trusted exact revision")
	}
	return &securityResearchContext{target: target, revision: revision}, nil
}

func (s *securityScanState) securityResearchActor() (string, error) {
	actor := strings.TrimSpace(s.scanCtx.RunName)
	if actor == "" {
		return "", fmt.Errorf("trusted run context must provide an actor")
	}
	return actor, nil
}

func securityResearchResult(value any) (Result, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return Result{Content: "encoding security research result: " + err.Error(), IsError: true}, nil
	}
	return Result{Content: string(raw)}, nil
}

func securityResearchFailure(err error) (Result, error) {
	return Result{Content: err.Error(), IsError: true}, nil
}

func parseSecurityResearchUUID(value, field string, optional bool) (*uuid.UUID, error) {
	value = strings.TrimSpace(value)
	if value == "" && optional {
		return nil, nil
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("%s must be a UUID", field)
	}
	return &id, nil
}

func (s *securityScanState) currentSecurityHypothesis(ctx context.Context, revisionID, hypothesisID uuid.UUID) error {
	values, err := s.researchStore.ListSecurityResearchHypotheses(ctx, s.scanCtx.Namespace, revisionID)
	if err != nil {
		return err
	}
	for i := range values {
		if values[i].ID == hypothesisID {
			return nil
		}
	}
	return fmt.Errorf("hypothesis does not belong to the trusted exact revision")
}

func (s *securityScanState) currentSecuritySweep(ctx context.Context, revisionID, sweepID uuid.UUID) (*store.SecurityResearchVariantSweep, error) {
	values, err := s.researchStore.ListSecurityResearchVariantSweeps(ctx, s.scanCtx.Namespace, revisionID)
	if err != nil {
		return nil, err
	}
	for i := range values {
		if values[i].ID == sweepID {
			return &values[i], nil
		}
	}
	return nil, fmt.Errorf("variant sweep does not belong to the trusted exact revision")
}

func (s *securityScanState) exactRevisionFinding(ctx context.Context, findingID uuid.UUID) (*store.SecurityFindingRecord, error) {
	finding, err := s.getFinding(ctx, findingID)
	if err != nil {
		return nil, err
	}
	if finding == nil || strings.TrimSpace(finding.Repository) != strings.TrimSpace(s.scanCtx.Repository) ||
		strings.TrimSpace(finding.Revision) != strings.TrimSpace(s.scanCtx.Revision) ||
		strings.TrimSpace(finding.ScanName) != strings.TrimSpace(s.scanCtx.ScanName) ||
		(strings.TrimSpace(s.scanCtx.ExecutionID) != "" && strings.TrimSpace(finding.ExecutionID) != strings.TrimSpace(s.scanCtx.ExecutionID)) {
		return nil, fmt.Errorf("finding does not belong to the trusted scan execution and exact revision")
	}
	return finding, nil
}

type getSecurityResearchContextTool struct{ state *securityScanState }

func (t *getSecurityResearchContextTool) Name() string { return "get_security_research_context" }
func (t *getSecurityResearchContextTool) Description() string {
	return "Get the durable dossier, hypotheses, coverage, and variant sweeps for this run's trusted target and exact revision."
}
func (t *getSecurityResearchContextTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false}`)
}
func (t *getSecurityResearchContextTool) IsReadOnly() bool                      { return true }
func (t *getSecurityResearchContextTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *getSecurityResearchContextTool) NeedsApproval() bool                   { return false }
func (t *getSecurityResearchContextTool) TimeoutSeconds() int                   { return 0 }
func (t *getSecurityResearchContextTool) Execute(ctx context.Context, _ json.RawMessage, _ string) (Result, error) {
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return securityResearchFailure(err)
	}
	dossier, err := t.state.researchStore.GetLatestSecurityResearchDossier(ctx, t.state.scanCtx.Namespace, bound.revision.ID)
	if err != nil {
		return securityResearchFailure(err)
	}
	hypotheses, err := t.state.researchStore.ListSecurityResearchHypotheses(ctx, t.state.scanCtx.Namespace, bound.revision.ID)
	if err != nil {
		return securityResearchFailure(err)
	}
	coverage, err := t.state.researchStore.ListSecurityResearchCoverage(ctx, t.state.scanCtx.Namespace, bound.revision.ID)
	if err != nil {
		return securityResearchFailure(err)
	}
	sweeps, err := t.state.researchStore.ListSecurityResearchVariantSweeps(ctx, t.state.scanCtx.Namespace, bound.revision.ID)
	if err != nil {
		return securityResearchFailure(err)
	}
	return securityResearchResult(map[string]any{
		"target":         bound.target,
		"revision":       bound.revision,
		"dossier":        dossier,
		"hypotheses":     hypotheses,
		"coverage":       coverage,
		"variant_sweeps": sweeps,
	})
}

type amendSecurityDossierInput struct {
	Content         json.RawMessage `json:"content"`
	ChangeSummary   string          `json:"change_summary"`
	ParentID        string          `json:"parent_id"`
	ExpectedVersion int32           `json:"expected_version"`
	IdempotencyKey  string          `json:"idempotency_key"`
}

type amendSecurityDossierTool struct{ state *securityScanState }

func (t *amendSecurityDossierTool) Name() string { return "amend_security_dossier" }
func (t *amendSecurityDossierTool) Description() string {
	return "Append an idempotent dossier amendment to this run's trusted target and exact revision."
}
func (t *amendSecurityDossierTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"content":{"type":"object"},"change_summary":{"type":"string"},"parent_id":{"type":"string"},"expected_version":{"type":"integer","minimum":1},"idempotency_key":{"type":"string"}},"required":["content","change_summary","idempotency_key"],"additionalProperties":false}`)
}
func (t *amendSecurityDossierTool) IsReadOnly() bool                      { return true }
func (t *amendSecurityDossierTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *amendSecurityDossierTool) NeedsApproval() bool                   { return false }
func (t *amendSecurityDossierTool) TimeoutSeconds() int                   { return 0 }
func (t *amendSecurityDossierTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in amendSecurityDossierInput
	if err := json.Unmarshal(input, &in); err != nil {
		return securityResearchFailure(fmt.Errorf("invalid input: %w", err))
	}
	if strings.TrimSpace(in.ChangeSummary) == "" || strings.TrimSpace(in.IdempotencyKey) == "" || len(in.Content) == 0 {
		return securityResearchFailure(fmt.Errorf("content, change_summary, and idempotency_key are required"))
	}
	parentID, err := parseSecurityResearchUUID(in.ParentID, "parent_id", true)
	if err != nil {
		return securityResearchFailure(err)
	}
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return securityResearchFailure(err)
	}
	actor, err := t.state.securityResearchActor()
	if err != nil {
		return securityResearchFailure(err)
	}
	value, created, err := t.state.researchStore.AmendSecurityResearchDossier(ctx, t.state.scanCtx.Namespace, &store.SecurityResearchDossier{
		RevisionID:     bound.revision.ID,
		Version:        in.ExpectedVersion,
		ParentID:       parentID,
		Content:        in.Content,
		ChangeSummary:  strings.TrimSpace(in.ChangeSummary),
		Actor:          actor,
		IdempotencyKey: strings.TrimSpace(in.IdempotencyKey),
	})
	if err != nil {
		return securityResearchFailure(err)
	}
	return securityResearchResult(map[string]any{"created": created, "dossier": value})
}

type createSecurityHypothesisInput struct {
	HypothesisKey  string          `json:"hypothesis_key"`
	Title          string          `json:"title"`
	Invariant      string          `json:"invariant"`
	Detail         json.RawMessage `json:"detail"`
	IdempotencyKey string          `json:"idempotency_key"`
}

type createSecurityHypothesisTool struct{ state *securityScanState }

func (t *createSecurityHypothesisTool) Name() string { return "create_security_hypothesis" }
func (t *createSecurityHypothesisTool) Description() string {
	return "Create an idempotent proposed hypothesis for this run's trusted exact revision."
}
func (t *createSecurityHypothesisTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"hypothesis_key":{"type":"string"},"title":{"type":"string"},"invariant":{"type":"string"},"detail":{"type":"object"},"idempotency_key":{"type":"string"}},"required":["hypothesis_key","title","invariant","detail","idempotency_key"],"additionalProperties":false}`)
}
func (t *createSecurityHypothesisTool) IsReadOnly() bool                      { return true }
func (t *createSecurityHypothesisTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *createSecurityHypothesisTool) NeedsApproval() bool                   { return false }
func (t *createSecurityHypothesisTool) TimeoutSeconds() int                   { return 0 }
func (t *createSecurityHypothesisTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in createSecurityHypothesisInput
	if err := json.Unmarshal(input, &in); err != nil {
		return securityResearchFailure(fmt.Errorf("invalid input: %w", err))
	}
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return securityResearchFailure(err)
	}
	actor, err := t.state.securityResearchActor()
	if err != nil {
		return securityResearchFailure(err)
	}
	value, created, err := t.state.researchStore.CreateSecurityResearchHypothesis(ctx, t.state.scanCtx.Namespace, &store.SecurityResearchHypothesis{
		RevisionID:     bound.revision.ID,
		HypothesisKey:  strings.TrimSpace(in.HypothesisKey),
		Title:          strings.TrimSpace(in.Title),
		Invariant:      strings.TrimSpace(in.Invariant),
		Status:         store.SecurityHypothesisProposed,
		Result:         store.SecurityHypothesisResultPending,
		Detail:         in.Detail,
		Actor:          actor,
		IdempotencyKey: strings.TrimSpace(in.IdempotencyKey),
	})
	if err != nil {
		return securityResearchFailure(err)
	}
	return securityResearchResult(map[string]any{"created": created, "hypothesis": value})
}

type transitionSecurityHypothesisInput struct {
	HypothesisID    string          `json:"hypothesis_id"`
	ExpectedVersion int32           `json:"expected_version"`
	ToStatus        string          `json:"to_status"`
	Result          string          `json:"result"`
	Rationale       string          `json:"rationale"`
	Detail          json.RawMessage `json:"detail"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Reopen          bool            `json:"reopen"`
}

type transitionSecurityHypothesisTool struct{ state *securityScanState }

func (t *transitionSecurityHypothesisTool) Name() string { return "transition_security_hypothesis" }
func (t *transitionSecurityHypothesisTool) Description() string {
	return "Transition or explicitly reopen a hypothesis on this run's trusted exact revision."
}
func (t *transitionSecurityHypothesisTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"hypothesis_id":{"type":"string"},"expected_version":{"type":"integer","minimum":1},"to_status":{"type":"string","enum":["investigating","supported","weakened","falsified","blocked","superseded","promoted"]},"result":{"type":"string","enum":["pending","positive","negative","failed","timed_out","inconclusive","abandoned"]},"rationale":{"type":"string"},"detail":{"type":"object"},"idempotency_key":{"type":"string"},"reopen":{"type":"boolean"}},"required":["hypothesis_id","expected_version","to_status","result","rationale","detail","idempotency_key"],"additionalProperties":false}`)
}
func (t *transitionSecurityHypothesisTool) IsReadOnly() bool                      { return true }
func (t *transitionSecurityHypothesisTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *transitionSecurityHypothesisTool) NeedsApproval() bool                   { return false }
func (t *transitionSecurityHypothesisTool) TimeoutSeconds() int                   { return 0 }
func (t *transitionSecurityHypothesisTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in transitionSecurityHypothesisInput
	if err := json.Unmarshal(input, &in); err != nil {
		return securityResearchFailure(fmt.Errorf("invalid input: %w", err))
	}
	hypothesisID, err := parseSecurityResearchUUID(in.HypothesisID, "hypothesis_id", false)
	if err != nil {
		return securityResearchFailure(err)
	}
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return securityResearchFailure(err)
	}
	if err := t.state.currentSecurityHypothesis(ctx, bound.revision.ID, *hypothesisID); err != nil {
		return securityResearchFailure(err)
	}
	actor, err := t.state.securityResearchActor()
	if err != nil {
		return securityResearchFailure(err)
	}
	transition := store.SecurityHypothesisTransition{
		ExpectedVersion: in.ExpectedVersion,
		ToStatus:        strings.TrimSpace(in.ToStatus),
		Result:          strings.TrimSpace(in.Result),
		Actor:           actor,
		Rationale:       strings.TrimSpace(in.Rationale),
		Detail:          in.Detail,
		IdempotencyKey:  strings.TrimSpace(in.IdempotencyKey),
	}
	var value *store.SecurityResearchHypothesis
	if in.Reopen {
		value, err = t.state.researchStore.ReopenSecurityResearchHypothesis(ctx, t.state.scanCtx.Namespace, *hypothesisID, transition)
	} else {
		value, err = t.state.researchStore.TransitionSecurityResearchHypothesis(ctx, t.state.scanCtx.Namespace, *hypothesisID, transition)
	}
	if err != nil {
		return securityResearchFailure(err)
	}
	return securityResearchResult(map[string]any{"hypothesis": value})
}

type recordSecurityCoverageInput struct {
	HypothesisID   string          `json:"hypothesis_id"`
	Dimension      string          `json:"dimension"`
	SubjectKey     string          `json:"subject_key"`
	Verdict        string          `json:"verdict"`
	Bounds         json.RawMessage `json:"bounds"`
	Evidence       json.RawMessage `json:"evidence"`
	IdempotencyKey string          `json:"idempotency_key"`
}

type recordSecurityCoverageTool struct{ state *securityScanState }

func (t *recordSecurityCoverageTool) Name() string { return "record_security_coverage" }
func (t *recordSecurityCoverageTool) Description() string {
	return "Record invariant, actor, state, or transition coverage for this run's trusted exact revision."
}
func (t *recordSecurityCoverageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"hypothesis_id":{"type":"string"},"dimension":{"type":"string","enum":["invariant","actor","state","transition"]},"subject_key":{"type":"string"},"verdict":{"type":"string","enum":["disproved","adequately_tested","inadequately_tested","not_tested"]},"bounds":{"type":"object"},"evidence":{"type":"array"},"idempotency_key":{"type":"string"}},"required":["dimension","subject_key","verdict","bounds","evidence","idempotency_key"],"additionalProperties":false}`)
}
func (t *recordSecurityCoverageTool) IsReadOnly() bool                      { return true }
func (t *recordSecurityCoverageTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *recordSecurityCoverageTool) NeedsApproval() bool                   { return false }
func (t *recordSecurityCoverageTool) TimeoutSeconds() int                   { return 0 }
func (t *recordSecurityCoverageTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in recordSecurityCoverageInput
	if err := json.Unmarshal(input, &in); err != nil {
		return securityResearchFailure(fmt.Errorf("invalid input: %w", err))
	}
	hypothesisID, err := parseSecurityResearchUUID(in.HypothesisID, "hypothesis_id", true)
	if err != nil {
		return securityResearchFailure(err)
	}
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return securityResearchFailure(err)
	}
	if hypothesisID != nil {
		if err := t.state.currentSecurityHypothesis(ctx, bound.revision.ID, *hypothesisID); err != nil {
			return securityResearchFailure(err)
		}
	}
	actor, err := t.state.securityResearchActor()
	if err != nil {
		return securityResearchFailure(err)
	}
	value, created, err := t.state.researchStore.RecordSecurityResearchCoverage(ctx, t.state.scanCtx.Namespace, &store.SecurityResearchCoverage{
		RevisionID:     bound.revision.ID,
		HypothesisID:   hypothesisID,
		Dimension:      strings.TrimSpace(in.Dimension),
		SubjectKey:     strings.TrimSpace(in.SubjectKey),
		Verdict:        strings.TrimSpace(in.Verdict),
		Bounds:         in.Bounds,
		Evidence:       in.Evidence,
		Actor:          actor,
		IdempotencyKey: strings.TrimSpace(in.IdempotencyKey),
	})
	if err != nil {
		return securityResearchFailure(err)
	}
	return securityResearchResult(map[string]any{"created": created, "coverage": value})
}

type createSecurityVariantSweepInput struct {
	FindingID        string          `json:"finding_id"`
	RootHypothesisID string          `json:"root_hypothesis_id"`
	RootCause        string          `json:"root_cause"`
	Scope            json.RawMessage `json:"scope"`
	Status           string          `json:"status"`
	IdempotencyKey   string          `json:"idempotency_key"`
}

type createSecurityVariantSweepTool struct{ state *securityScanState }

func (t *createSecurityVariantSweepTool) Name() string { return "create_security_variant_sweep" }
func (t *createSecurityVariantSweepTool) Description() string {
	return "Create a durable same-root-cause variant sweep for this run's trusted exact revision."
}
func (t *createSecurityVariantSweepTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"finding_id":{"type":"string"},"root_hypothesis_id":{"type":"string"},"root_cause":{"type":"string"},"scope":{"type":"object"},"status":{"type":"string","enum":["pending","running"]},"idempotency_key":{"type":"string"}},"required":["root_cause","scope","idempotency_key"],"additionalProperties":false}`)
}
func (t *createSecurityVariantSweepTool) IsReadOnly() bool                      { return true }
func (t *createSecurityVariantSweepTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *createSecurityVariantSweepTool) NeedsApproval() bool                   { return false }
func (t *createSecurityVariantSweepTool) TimeoutSeconds() int                   { return 0 }
func (t *createSecurityVariantSweepTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in createSecurityVariantSweepInput
	if err := json.Unmarshal(input, &in); err != nil {
		return securityResearchFailure(fmt.Errorf("invalid input: %w", err))
	}
	findingID, err := parseSecurityResearchUUID(in.FindingID, "finding_id", true)
	if err != nil {
		return securityResearchFailure(err)
	}
	rootHypothesisID, err := parseSecurityResearchUUID(in.RootHypothesisID, "root_hypothesis_id", true)
	if err != nil {
		return securityResearchFailure(err)
	}
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return securityResearchFailure(err)
	}
	if findingID != nil {
		if _, err := t.state.exactRevisionFinding(ctx, *findingID); err != nil {
			return securityResearchFailure(err)
		}
	}
	if rootHypothesisID != nil {
		if err := t.state.currentSecurityHypothesis(ctx, bound.revision.ID, *rootHypothesisID); err != nil {
			return securityResearchFailure(err)
		}
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = store.SecurityVariantSweepPending
	}
	actor, err := t.state.securityResearchActor()
	if err != nil {
		return securityResearchFailure(err)
	}
	value, created, err := t.state.researchStore.CreateSecurityResearchVariantSweep(ctx, t.state.scanCtx.Namespace, &store.SecurityResearchVariantSweep{
		RevisionID:       bound.revision.ID,
		FindingID:        findingID,
		RootHypothesisID: rootHypothesisID,
		RootCause:        strings.TrimSpace(in.RootCause),
		Scope:            in.Scope,
		Status:           status,
		Actor:            actor,
		IdempotencyKey:   strings.TrimSpace(in.IdempotencyKey),
	})
	if err != nil {
		return securityResearchFailure(err)
	}
	return securityResearchResult(map[string]any{"created": created, "variant_sweep": value})
}

type completeSecurityVariantSweepInput struct {
	SweepID        string          `json:"sweep_id"`
	Status         string          `json:"status"`
	Result         json.RawMessage `json:"result"`
	IdempotencyKey string          `json:"idempotency_key"`
}

type completeSecurityVariantSweepTool struct{ state *securityScanState }

func (t *completeSecurityVariantSweepTool) Name() string { return "complete_security_variant_sweep" }
func (t *completeSecurityVariantSweepTool) Description() string {
	return "Complete or block a variant sweep on this run's trusted exact revision."
}
func (t *completeSecurityVariantSweepTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"sweep_id":{"type":"string"},"status":{"type":"string","enum":["completed","blocked"]},"result":{"type":"object"},"idempotency_key":{"type":"string"}},"required":["sweep_id","status","result","idempotency_key"],"additionalProperties":false}`)
}
func (t *completeSecurityVariantSweepTool) IsReadOnly() bool                      { return true }
func (t *completeSecurityVariantSweepTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *completeSecurityVariantSweepTool) NeedsApproval() bool                   { return false }
func (t *completeSecurityVariantSweepTool) TimeoutSeconds() int                   { return 0 }
func (t *completeSecurityVariantSweepTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in completeSecurityVariantSweepInput
	if err := json.Unmarshal(input, &in); err != nil {
		return securityResearchFailure(fmt.Errorf("invalid input: %w", err))
	}
	sweepID, err := parseSecurityResearchUUID(in.SweepID, "sweep_id", false)
	if err != nil {
		return securityResearchFailure(err)
	}
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return securityResearchFailure(err)
	}
	if _, err := t.state.currentSecuritySweep(ctx, bound.revision.ID, *sweepID); err != nil {
		return securityResearchFailure(err)
	}
	actor, err := t.state.securityResearchActor()
	if err != nil {
		return securityResearchFailure(err)
	}
	value, err := t.state.researchStore.CompleteSecurityResearchVariantSweep(ctx, t.state.scanCtx.Namespace, *sweepID, strings.TrimSpace(in.Status), in.Result, actor, strings.TrimSpace(in.IdempotencyKey))
	if err != nil {
		return securityResearchFailure(err)
	}
	return securityResearchResult(map[string]any{"variant_sweep": value})
}

type getSecurityCampaignStatusInput struct {
	Workflow string `json:"workflow"`
}

type getSecurityCampaignStatusTool struct{ state *securityScanState }

func (t *getSecurityCampaignStatusTool) Name() string { return "get_security_campaign_status" }
func (t *getSecurityCampaignStatusTool) Description() string {
	return "Summarize hypothesis, coverage, sweep, and exact submission precision status for this trusted target and revision."
}
func (t *getSecurityCampaignStatusTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"workflow":{"type":"string"}},"additionalProperties":false}`)
}
func (t *getSecurityCampaignStatusTool) IsReadOnly() bool                      { return true }
func (t *getSecurityCampaignStatusTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *getSecurityCampaignStatusTool) NeedsApproval() bool                   { return false }
func (t *getSecurityCampaignStatusTool) TimeoutSeconds() int                   { return 0 }
func (t *getSecurityCampaignStatusTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in getSecurityCampaignStatusInput
	if len(input) != 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return securityResearchFailure(fmt.Errorf("invalid input: %w", err))
		}
	}
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return securityResearchFailure(err)
	}
	hypotheses, err := t.state.researchStore.ListSecurityResearchHypotheses(ctx, t.state.scanCtx.Namespace, bound.revision.ID)
	if err != nil {
		return securityResearchFailure(err)
	}
	coverage, err := t.state.researchStore.ListSecurityResearchCoverage(ctx, t.state.scanCtx.Namespace, bound.revision.ID)
	if err != nil {
		return securityResearchFailure(err)
	}
	sweeps, err := t.state.researchStore.ListSecurityResearchVariantSweeps(ctx, t.state.scanCtx.Namespace, bound.revision.ID)
	if err != nil {
		return securityResearchFailure(err)
	}
	workflow := strings.TrimSpace(in.Workflow)
	if workflow == "" {
		workflow = securityResearchWorkflow(t.state.scanCtx)
	}
	precision, err := t.state.researchStore.GetSecuritySubmissionPrecision(ctx, t.state.scanCtx.Namespace, bound.target.ID, workflow, nil)
	if err != nil {
		return securityResearchFailure(err)
	}
	hypothesisStatuses := map[string]int{}
	hypothesisResults := map[string]int{}
	for _, value := range hypotheses {
		hypothesisStatuses[value.Status]++
		hypothesisResults[value.Result]++
	}
	coverageVerdicts := map[string]int{}
	coverageDimensions := map[string]int{}
	for _, value := range coverage {
		coverageVerdicts[value.Verdict]++
		coverageDimensions[value.Dimension]++
	}
	sweepStatuses := map[string]int{}
	for _, value := range sweeps {
		sweepStatuses[value.Status]++
	}
	precisionValue := map[string]int64{
		"submitted": precision.Submitted, "accepted": precision.Accepted,
		"duplicate": precision.Duplicate, "informative": precision.Informative,
		"rejected": precision.Rejected, "resolved": precision.Resolved,
	}
	return securityResearchResult(map[string]any{
		"target_id":              bound.target.ID,
		"revision_id":            bound.revision.ID,
		"revision":               bound.revision.Revision,
		"workflow":               workflow,
		"hypothesis_statuses":    hypothesisStatuses,
		"hypothesis_results":     hypothesisResults,
		"coverage_dimensions":    coverageDimensions,
		"coverage_verdicts":      coverageVerdicts,
		"variant_sweep_statuses": sweepStatuses,
		"precision":              precisionValue,
	})
}

func securityResearchWorkflow(scanCtx SecurityScanContext) string {
	if workflow := strings.TrimSpace(scanCtx.ScanName); workflow != "" {
		return workflow
	}
	return defaultSecurityResearchWorkflow
}
