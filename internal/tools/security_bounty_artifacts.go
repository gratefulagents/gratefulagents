package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/sdk/pkg/agentsdk"

	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

const (
	maxSecurityPoCFiles       = 16
	maxSecurityPoCFileBytes   = 128 << 10
	maxSecurityPoCTotalBytes  = 1 << 20
	maxSecurityBundleBytes    = 2 << 20
	securityBundleMediaType   = "application/zip"
	securityBundleStorePrefix = "security-submissions/v1"
)

// SecurityBountyBlobStore is the private object-store surface used by the
// platform-controlled bundle writer. Models never choose a bucket or key.
type SecurityBountyBlobStore interface {
	Put(ctx context.Context, key string, content []byte, mediaType string) error
}

type securityPoCFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type securityPoCCandidate struct {
	Setup          string            `json:"setup"`
	Command        string            `json:"command"`
	ExpectedOutput string            `json:"expected_output"`
	ObservedOutput string            `json:"observed_output"`
	Teardown       string            `json:"teardown"`
	Environment    string            `json:"environment"`
	Files          []securityPoCFile `json:"files"`
}

type securityPoCValidation struct {
	Confirmed       bool   `json:"confirmed"`
	CandidateSHA256 string `json:"candidate_sha256"`
	Command         string `json:"command"`
	ObservedOutput  string `json:"observed_output"`
	Reason          string `json:"reason"`
	// ReproducibilityClass states how this reproduction can be reproduced
	// again. Ordering, reorg and race bugs do not replay byte-identically, so
	// a single definition of determinism would reject a real race and accept
	// a flake after one lucky rerun.
	ReproducibilityClass string `json:"reproducibility_class"`
	// The harness-health fields below decide whether a confirmation means
	// anything: a run that never reached the target, ran no control, or
	// carried an assertion that cannot fail has proved nothing.
	TargetCodeExecuted bool `json:"target_code_executed"`
	NegativeControlRan bool `json:"negative_control_ran"`
	// NegativeControlPassed reports that the control did NOT trigger. A
	// harness that fires for the control as well as the exploit has not
	// attributed anything to the defect.
	NegativeControlPassed bool   `json:"negative_control_passed"`
	OracleCanFail         bool   `json:"oracle_can_fail"`
	OracleEvidence        string `json:"oracle_evidence"`
	// Trials records attempts for a non-deterministic class.
	Attempts     int    `json:"attempts,omitempty"`
	Successes    int    `json:"successes,omitempty"`
	StoppingRule string `json:"stopping_rule,omitempty"`
}

// validateSecurityPoCEvidence enforces the difference between "the command ran
// and printed something" and "this reproduction proves anything". A confirmed
// verdict must exercise real target code, run its control, and demonstrate
// that the oracle can fail; a non-deterministic class must carry its trials.
func validateSecurityPoCEvidence(validation securityPoCValidation) []string {
	var problems []string
	class := securitytoolpacks.ReproducibilityClass(strings.TrimSpace(validation.ReproducibilityClass))
	if !securitytoolpacks.ValidReproducibilityClass(class) {
		problems = append(problems, "reproducibility_class must be one of deterministic, seeded_replayable, schedule_or_environment_dependent, statistical, observational_only")
	}
	if !validation.Confirmed {
		// A disproof does not need to prove its own oracle: it needs to say
		// which check stops the attack, which `reason` already carries.
		return problems
	}
	if class == "" {
		problems = append(problems, "reproducibility_class is required for a confirmed reproduction")
	}
	if !validation.TargetCodeExecuted {
		problems = append(problems, "target_code_executed must be true: a model or mock is never a target-code reproduction")
	}
	if !validation.NegativeControlRan {
		problems = append(problems, "negative_control_ran must be true: without a control the result is not attributable to the defect")
	} else if !validation.NegativeControlPassed {
		problems = append(problems, "negative_control_passed must be true: a harness that triggers for the control as well as the exploit has attributed nothing to the defect")
	}
	if !validation.OracleCanFail || strings.TrimSpace(validation.OracleEvidence) == "" {
		problems = append(problems, "oracle_can_fail requires oracle_evidence: show the mutation or calibration that made the assertion fail on purpose")
	}
	switch class {
	case securitytoolpacks.ReproducibilityScheduleDependent, securitytoolpacks.ReproducibilityStatistical:
		if validation.Attempts <= 0 {
			problems = append(problems, "a schedule-dependent or statistical reproduction must report its attempts, including the ones that did not trigger")
		}
		if validation.Successes < 0 {
			problems = append(problems, "successes cannot be negative")
		}
		if validation.Successes > validation.Attempts {
			problems = append(problems, "successes cannot exceed attempts")
		}
		if strings.TrimSpace(validation.StoppingRule) == "" {
			problems = append(problems, "stopping_rule is required so a low trigger rate is not read as exhaustive")
		}
	}
	return problems
}

type securityBountySubmission struct {
	Markdown string `json:"markdown"`
	// The fields below are the ones bounty programs reject reports for
	// omitting. They are validated before anything is persisted, so a report
	// missing them never becomes an artifact.
	ImpactClause        string `json:"impact_clause"`
	RootCause           string `json:"root_cause"`
	MaxAchievableImpact string `json:"max_achievable_impact"`
	AttackPath          string `json:"attack_path"`
	Feasibility         string `json:"feasibility"`
	FundsAtRisk         string `json:"funds_at_risk"`
	Remediation         string `json:"remediation"`
	PriorArt            string `json:"prior_art"`
	// SeveritySystem echoes the governing program's system. It is checked
	// against the controller-stamped value: severity is never translated
	// between systems.
	SeveritySystem string `json:"severity_system"`
}

type securityBundleManifest struct {
	SchemaVersion  string            `json:"schema_version"`
	FindingID      string            `json:"finding_id"`
	FindingStatus  string            `json:"finding_status"`
	Fingerprint    string            `json:"fingerprint"`
	Repository     string            `json:"repository"`
	Revision       string            `json:"revision"`
	ScanName       string            `json:"scan_name"`
	ExecutionID    string            `json:"execution_id,omitempty"`
	BuilderRun     string            `json:"builder_run"`
	ValidatorRun   string            `json:"validator_run"`
	ReportRun      string            `json:"report_run"`
	SeveritySystem string            `json:"severity_system,omitempty"`
	ImpactClause   string            `json:"impact_clause,omitempty"`
	ProgramLevel   string            `json:"program_severity_level,omitempty"`
	BudgetState    string            `json:"submission_budget_state,omitempty"`
	FilesSHA256    map[string]string `json:"files_sha256"`
}

type securityBountyArtifactDeps struct {
	Blobs    SecurityBountyBlobStore
	BlobsErr error
}

// RegisterSecurityBountyArtifactTools adds finding-bound tools only when the
// run is a durable post-script job. Without Postgres they remain unavailable:
// free-form notes are deliberately not treated as downloadable PoCs.
func RegisterSecurityBountyArtifactTools(registry *Registry, state *securityScanState, blobs SecurityBountyBlobStore, blobsErr error) {
	if registry == nil || state == nil || state.findingStore == nil || state.scanCtx.PostScriptFingerprint == "" {
		return
	}
	artifactStore, ok := state.findingStore.(store.SecurityFindingArtifactStore)
	if !ok {
		return
	}
	deps := securityBountyArtifactDeps{Blobs: blobs, BlobsErr: blobsErr}
	registry.Register(&saveSecurityPoCTool{state: state, artifacts: artifactStore})
	registry.Register(&getSecurityPoCTool{state: state, artifacts: artifactStore})
	registry.Register(&validateSecurityPoCTool{state: state, artifacts: artifactStore})
	registry.Register(&saveSecurityBountySubmissionTool{state: state, artifacts: artifactStore, deps: deps})
}

func bountyScriptEnabled(state *securityScanState, name string) bool {
	return state != nil && slices.Contains(state.scanCtx.PostScripts, name)
}

func boundSecurityFinding(ctx context.Context, state *securityScanState) (*store.SecurityFindingRecord, error) {
	if state == nil || state.scanCtx.PostScriptFingerprint == "" {
		return nil, fmt.Errorf("tool is only available to a finding-bound security post-script")
	}
	return state.resolveFinding(ctx, "", state.scanCtx.PostScriptFingerprint)
}

func validatePoCFiles(files []securityPoCFile) error {
	if len(files) == 0 || len(files) > maxSecurityPoCFiles {
		return fmt.Errorf("files must contain 1 to %d text files", maxSecurityPoCFiles)
	}
	total := 0
	seen := map[string]bool{}
	for i := range files {
		name := strings.TrimSpace(strings.ReplaceAll(files[i].Path, "\\", "/"))
		clean := path.Clean(name)
		if name == "" || clean == "." || clean != name || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("file %d has unsafe relative path %q", i, files[i].Path)
		}
		if strings.EqualFold(clean, "README.md") {
			return fmt.Errorf("PoC file path %q is reserved for the generated reproduction transcript", clean)
		}
		if seen[clean] {
			return fmt.Errorf("duplicate PoC file path %q", clean)
		}
		seen[clean] = true
		files[i].Path = clean
		size := len([]byte(files[i].Content))
		if size > maxSecurityPoCFileBytes {
			return fmt.Errorf("PoC file %q exceeds %d bytes", clean, maxSecurityPoCFileBytes)
		}
		total += size
	}
	if total > maxSecurityPoCTotalBytes {
		return fmt.Errorf("PoC files exceed the %d-byte total limit", maxSecurityPoCTotalBytes)
	}
	return nil
}

func upsertFindingArtifact(ctx context.Context, artifacts store.SecurityFindingArtifactStore, namespace string, findingID uuid.UUID, executionID, kind string, content any, actor, status string) error {
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	_, err = artifacts.UpsertSecurityFindingArtifact(ctx, namespace, &store.SecurityFindingArtifact{
		FindingID:   findingID,
		ExecutionID: strings.TrimSpace(executionID),
		Kind:        kind,
		Content:     raw,
		SHA256:      hex.EncodeToString(digest[:]),
		ActorRun:    actor,
		Status:      status,
	})
	return err
}

type saveSecurityPoCTool struct {
	state     *securityScanState
	artifacts store.SecurityFindingArtifactStore
}

func (t *saveSecurityPoCTool) Name() string { return "save_security_poc" }
func (t *saveSecurityPoCTool) Description() string {
	return "Save a bounded, local proof-of-concept candidate and its exact reproduction transcript for this finding."
}
func (t *saveSecurityPoCTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"setup":{"type":"string"},"command":{"type":"string"},"expected_output":{"type":"string"},"observed_output":{"type":"string"},"teardown":{"type":"string"},"environment":{"type":"string"},"files":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}}},"required":["command","expected_output","observed_output","environment","files"]}`)
}
func (t *saveSecurityPoCTool) IsReadOnly() bool { return true }
func (t *saveSecurityPoCTool) IsEnabled(_ *agentsdk.RunContext) bool {
	return bountyScriptEnabled(t.state, "poc-builder")
}
func (t *saveSecurityPoCTool) NeedsApproval() bool { return false }
func (t *saveSecurityPoCTool) TimeoutSeconds() int { return 0 }
func (t *saveSecurityPoCTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var candidate securityPoCCandidate
	if err := json.Unmarshal(input, &candidate); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(candidate.Command) == "" || strings.TrimSpace(candidate.ExpectedOutput) == "" || strings.TrimSpace(candidate.ObservedOutput) == "" || strings.TrimSpace(candidate.Environment) == "" {
		return Result{Content: "command, expected_output, observed_output, and environment are required", IsError: true}, nil
	}
	if err := validatePoCFiles(candidate.Files); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	finding, err := boundSecurityFinding(ctx, t.state)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	// A new/retried candidate invalidates any bundle from an earlier attempt
	// before candidate replacement, so no failure can leave an old ZIP ready
	// while a newer candidate is current.
	if _, err := t.artifacts.UpsertSecurityFindingArtifact(ctx, finding.Namespace, &store.SecurityFindingArtifact{
		FindingID: finding.ID, ExecutionID: t.state.scanCtx.ExecutionID,
		Kind: store.SecurityFindingArtifactSubmissionBundle, Content: json.RawMessage(`{"schema_version":"v1"}`),
		Status: "generating", ActorRun: t.state.scanCtx.RunName,
	}); err != nil {
		return Result{Content: "invalidating prior bundle metadata: " + err.Error(), IsError: true}, nil
	}
	if err := upsertFindingArtifact(ctx, t.artifacts, finding.Namespace, finding.ID, t.state.scanCtx.ExecutionID, store.SecurityFindingArtifactPoCCandidate, candidate, t.state.scanCtx.RunName, "candidate"); err != nil {
		return Result{Content: "saving PoC candidate: " + err.Error(), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("PoC candidate saved for finding %s; a separate validator must reproduce it before submission packaging.", finding.Fingerprint)}, nil
}

type getSecurityPoCTool struct {
	state     *securityScanState
	artifacts store.SecurityFindingArtifactStore
}

func (t *getSecurityPoCTool) Name() string { return "get_security_poc" }
func (t *getSecurityPoCTool) Description() string {
	return "Load the bounded PoC candidate and immutable SHA-256 for independent validation of this finding."
}
func (t *getSecurityPoCTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *getSecurityPoCTool) IsReadOnly() bool { return true }
func (t *getSecurityPoCTool) IsEnabled(_ *agentsdk.RunContext) bool {
	return bountyScriptEnabled(t.state, "poc-validator")
}
func (t *getSecurityPoCTool) NeedsApproval() bool { return false }
func (t *getSecurityPoCTool) TimeoutSeconds() int { return 0 }
func (t *getSecurityPoCTool) Execute(ctx context.Context, _ json.RawMessage, _ string) (Result, error) {
	finding, err := boundSecurityFinding(ctx, t.state)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	candidate, err := t.artifacts.GetSecurityFindingArtifact(ctx, finding.Namespace, finding.ID, t.state.scanCtx.ExecutionID, store.SecurityFindingArtifactPoCCandidate)
	if err != nil || candidate == nil {
		return Result{Content: "no PoC candidate exists for this execution", IsError: true}, nil
	}
	var envelope struct {
		CandidateSHA256 string               `json:"candidate_sha256"`
		Candidate       securityPoCCandidate `json:"candidate"`
	}
	envelope.CandidateSHA256 = candidate.SHA256
	if err := json.Unmarshal(candidate.Content, &envelope.Candidate); err != nil {
		return Result{Content: "stored PoC candidate is invalid", IsError: true}, nil
	}
	raw, _ := json.Marshal(envelope)
	return Result{Content: string(raw)}, nil
}

type validateSecurityPoCTool struct {
	state     *securityScanState
	artifacts store.SecurityFindingArtifactStore
}

func (t *validateSecurityPoCTool) Name() string { return "validate_security_poc" }
func (t *validateSecurityPoCTool) Description() string {
	return "Record an independent local reproduction verdict for the stored PoC candidate bound to this finding."
}
func (t *validateSecurityPoCTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"confirmed":{"type":"boolean"},"candidate_sha256":{"type":"string"},"command":{"type":"string"},` +
		`"observed_output":{"type":"string"},"reason":{"type":"string"},` +
		`"reproducibility_class":{"type":"string","enum":["deterministic","seeded_replayable","schedule_or_environment_dependent","statistical","observational_only"],"description":"How this result reproduces. Ordering, reorg and race bugs do not replay byte-identically."},` +
		`"target_code_executed":{"type":"boolean","description":"The real target code ran, not a mock or simplified model."},` +
		`"negative_control_ran":{"type":"boolean","description":"The same harness was run against unmodified or non-attacker input."},` +
		`"negative_control_passed":{"type":"boolean","description":"The control did not trigger. A harness that fires for the control too has attributed nothing to the defect."},` +
		`"oracle_can_fail":{"type":"boolean","description":"A mutation or known calibration failure made the assertion fail on purpose."},` +
		`"oracle_evidence":{"type":"string","description":"What made the assertion fail: the mutation applied, or the calibration case used."},` +
		`"attempts":{"type":"integer","minimum":0},"successes":{"type":"integer","minimum":0},` +
		`"stopping_rule":{"type":"string","description":"Why the campaign stopped, so a low trigger rate is not read as exhaustive."}},` +
		`"required":["confirmed","candidate_sha256","command","observed_output","reason","reproducibility_class"]}`)
}
func (t *validateSecurityPoCTool) IsReadOnly() bool { return true }
func (t *validateSecurityPoCTool) IsEnabled(_ *agentsdk.RunContext) bool {
	return bountyScriptEnabled(t.state, "poc-validator")
}
func (t *validateSecurityPoCTool) NeedsApproval() bool { return false }
func (t *validateSecurityPoCTool) TimeoutSeconds() int { return 0 }
func (t *validateSecurityPoCTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var validation securityPoCValidation
	if err := json.Unmarshal(input, &validation); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(validation.Command) == "" || strings.TrimSpace(validation.ObservedOutput) == "" || strings.TrimSpace(validation.Reason) == "" {
		return Result{Content: "command, observed_output, and reason are required", IsError: true}, nil
	}
	if problems := validateSecurityPoCEvidence(validation); len(problems) != 0 {
		return Result{Content: "validation evidence is incomplete and was not saved: " + strings.Join(problems, "; "), IsError: true}, nil
	}
	finding, err := boundSecurityFinding(ctx, t.state)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if finding.Status == store.SecurityFindingStatusFalsePositive || finding.Status == store.SecurityFindingStatusAcceptedRisk || finding.Status == store.SecurityFindingStatusFixed {
		return Result{Content: "terminal finding status is preserved; PoC validation cannot reopen it", IsError: true}, nil
	}
	candidate, err := t.artifacts.GetSecurityFindingArtifact(ctx, finding.Namespace, finding.ID, t.state.scanCtx.ExecutionID, store.SecurityFindingArtifactPoCCandidate)
	if err != nil || candidate == nil {
		return Result{Content: "no stored PoC candidate is available to validate", IsError: true}, nil
	}
	if !strings.EqualFold(strings.TrimSpace(validation.CandidateSHA256), candidate.SHA256) {
		return Result{Content: "candidate_sha256 does not match the current execution's stored PoC", IsError: true}, nil
	}
	if candidate.ActorRun == t.state.scanCtx.RunName {
		return Result{Content: "PoC validation must run in a different AgentRun than the builder", IsError: true}, nil
	}
	status := "rejected"
	findingStatus := store.SecurityFindingStatusTriaged
	if validation.Confirmed {
		status, findingStatus = "confirmed", store.SecurityFindingStatusConfirmed
	}
	if err := upsertFindingArtifact(ctx, t.artifacts, finding.Namespace, finding.ID, t.state.scanCtx.ExecutionID, store.SecurityFindingArtifactPoCValidation, validation, t.state.scanCtx.RunName, status); err != nil {
		return Result{Content: "saving PoC validation: " + err.Error(), IsError: true}, nil
	}
	if err := t.state.setFindingStatus(ctx, finding.ID, findingStatus, "PoC validator: "+validation.Reason); err != nil {
		return Result{Content: "saving finding verdict: " + err.Error(), IsError: true}, nil
	}
	if validation.Confirmed && t.state.researchStore != nil {
		bound, err := t.state.ensureSecurityResearchContext(ctx)
		if err != nil {
			return Result{Content: "binding confirmed finding to durable research: " + err.Error(), IsError: true}, nil
		}
		scope, _ := json.Marshal(map[string]any{"finding_fingerprint": finding.Fingerprint, "required": true})
		_, _, err = t.state.researchStore.CreateSecurityResearchVariantSweep(ctx, finding.Namespace, &store.SecurityResearchVariantSweep{
			RevisionID: bound.revision.ID, FindingID: &finding.ID, RootCause: finding.Title,
			Scope: scope, Status: store.SecurityVariantSweepPending, Actor: t.state.scanCtx.RunName,
			IdempotencyKey: "confirmed-finding:" + finding.ID.String() + ":" + bound.revision.ID.String(),
		})
		if err != nil {
			return Result{Content: "creating required variant sweep: " + err.Error(), IsError: true}, nil
		}
	}
	return Result{Content: "PoC validation recorded as " + status + "."}, nil
}

type saveSecurityBountySubmissionTool struct {
	state     *securityScanState
	artifacts store.SecurityFindingArtifactStore
	deps      securityBountyArtifactDeps
}

// securityProgramSeverityFloor returns the effective report eligibility floor.
// It follows the governing program's in-scope impact clauses unless the table is
// unavailable or critical-only, in which case the platform defaults to medium.
// Published severity labels are never translated or rewritten.
//
// Without a governing scope there is no published table to read, and a
// truncated list cannot prove which levels the program publishes, so both use
// the platform's medium default. Critical-only programs use that default too,
// while their published impact clauses remain unchanged.
func securityProgramSeverityFloor(scanCtx SecurityScanContext) string {
	floor := security.SeverityMedium
	if len(scanCtx.InScopeImpacts) == 0 || scanCtx.ImpactsTruncated {
		return floor
	}
	lowest := -1
	for _, impact := range scanCtx.InScopeImpacts {
		rank := security.SeverityRank(impact.Level)
		if rank < 0 {
			continue
		}
		if lowest < 0 || rank < lowest {
			lowest, floor = rank, impact.Level
		}
	}
	if floor == security.SeverityCritical {
		return security.SeverityMedium
	}
	return floor
}

func securityReportBundleStatus(finding *store.SecurityFindingRecord, scanCtx SecurityScanContext) (string, error) {
	floor := securityProgramSeverityFloor(scanCtx)
	if finding == nil || (finding.Status != store.SecurityFindingStatusConfirmed && finding.Status != store.SecurityFindingStatusTriaged) || finding.DuplicateOf != nil || finding.SuppressedBy != "" || !security.SeverityAtLeast(finding.Severity, floor) {
		return "", fmt.Errorf("finding is not an eligible triaged or confirmed, unsuppressed, non-duplicate report at or above the effective program severity floor %q", floor)
	}
	if finding.Status == store.SecurityFindingStatusConfirmed {
		return "ready", nil
	}
	return "review", nil
}

func (t *saveSecurityBountySubmissionTool) Name() string { return "save_security_bounty_submission" }
func (t *saveSecurityBountySubmissionTool) Description() string {
	return "Save the Markdown report, build a deterministic per-finding review ZIP, and upload it to the platform's private S3 bucket."
}
func (t *saveSecurityBountySubmissionTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"markdown":{"type":"string","description":"The full report in the governing program's template."},` +
		`"impact_clause":{"type":"string","description":"The impact, copied verbatim from the program's in-scope impact list. Never invent an impact or asset."},` +
		`"root_cause":{"type":"string","description":"The specific line, function, or logic mistake that causes the vulnerability."},` +
		`"max_achievable_impact":{"type":"string","description":"The maximum impact reachable from this root cause. Omitting it is a documented downgrade trigger."},` +
		`"attack_path":{"type":"string","description":"Ordered steps from the attacker's position to the impact, with every precondition stated."},` +
		`"feasibility":{"type":"string","description":"Attacker capital, financial risk, privilege assumptions, and mempool or chain assumptions."},` +
		`"funds_at_risk":{"type":"string","description":"Quantified value at risk with the block or snapshot it was measured at, or an explicit statement that the impact class permits no measurement."},` +
		`"remediation":{"type":"string","description":"The recommended fix."},` +
		`"prior_art":{"type":"string","description":"What was searched for duplicates and known issues, where, and the result."},` +
		`"severity_system":{"type":"string","description":"The governing program's severity system. Severity is never translated between systems."}},` +
		`"required":["markdown","impact_clause","root_cause","max_achievable_impact","attack_path","feasibility","remediation","prior_art"]}`)
}
func (t *saveSecurityBountySubmissionTool) IsReadOnly() bool { return true }
func (t *saveSecurityBountySubmissionTool) IsEnabled(_ *agentsdk.RunContext) bool {
	return bountyScriptEnabled(t.state, "report-writer")
}
func (t *saveSecurityBountySubmissionTool) NeedsApproval() bool { return false }
func (t *saveSecurityBountySubmissionTool) TimeoutSeconds() int { return 60 }
func (t *saveSecurityBountySubmissionTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var submission securityBountySubmission
	if err := json.Unmarshal(input, &submission); err != nil {
		return Result{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(submission.Markdown) == "" || len(submission.Markdown) > maxSecurityPoCTotalBytes {
		return Result{Content: "markdown is required and must not exceed 1 MiB", IsError: true}, nil
	}
	if problems := validateBountySubmissionClaim(submission, t.state.scanCtx); len(problems) != 0 {
		// Fail closed and write nothing: an incomplete claim is the single
		// most common reason a real submission is rejected, so it must not
		// become an artifact that looks submission-ready.
		return Result{Content: "submission is incomplete and was not saved: " + strings.Join(problems, "; "), IsError: true}, nil
	}
	finding, err := boundSecurityFinding(ctx, t.state)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	artifactStatus, err := securityReportBundleStatus(finding, t.state.scanCtx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	var candidate *securityPoCCandidate
	var validation *securityPoCValidation
	var builderRun, validatorRun string
	if finding.Status == store.SecurityFindingStatusConfirmed {
		candidateArtifact, err := t.artifacts.GetSecurityFindingArtifact(ctx, finding.Namespace, finding.ID, t.state.scanCtx.ExecutionID, store.SecurityFindingArtifactPoCCandidate)
		if err != nil || candidateArtifact == nil {
			return Result{Content: "PoC candidate artifact is missing", IsError: true}, nil
		}
		validationArtifact, err := t.artifacts.GetSecurityFindingArtifact(ctx, finding.Namespace, finding.ID, t.state.scanCtx.ExecutionID, store.SecurityFindingArtifactPoCValidation)
		if err != nil || validationArtifact == nil || validationArtifact.Status != "confirmed" {
			return Result{Content: "independent confirmed PoC validation is required", IsError: true}, nil
		}
		if candidateArtifact.ActorRun == validationArtifact.ActorRun {
			return Result{Content: "builder and validator provenance must be different AgentRuns", IsError: true}, nil
		}
		candidate, validation = &securityPoCCandidate{}, &securityPoCValidation{}
		if json.Unmarshal(candidateArtifact.Content, candidate) != nil || json.Unmarshal(validationArtifact.Content, validation) != nil || !validation.Confirmed || !strings.EqualFold(validation.CandidateSHA256, candidateArtifact.SHA256) {
			return Result{Content: "stored PoC artifacts are invalid or validation does not bind the current candidate", IsError: true}, nil
		}
		builderRun, validatorRun = candidateArtifact.ActorRun, validationArtifact.ActorRun
	}
	budgetState, retained, finishReservation, err := t.prepareSecurityResearchSubmission(ctx, finding, submission)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if retained {
		return Result{Content: budgetState}, nil
	}
	defer func() {
		if finishReservation != nil {
			_ = finishReservation(false)
		}
	}()
	if err := upsertFindingArtifact(ctx, t.artifacts, finding.Namespace, finding.ID, t.state.scanCtx.ExecutionID, store.SecurityFindingArtifactBountySubmission, submission, t.state.scanCtx.RunName, artifactStatus); err != nil {
		return Result{Content: "saving bounty submission: " + err.Error(), IsError: true}, nil
	}
	filename := fmt.Sprintf("%s-%s-security-review.zip", finding.ScanName, finding.Fingerprint)
	if finding.Status == store.SecurityFindingStatusConfirmed {
		filename = fmt.Sprintf("%s-%s-bounty-submission.zip", finding.ScanName, finding.Fingerprint)
	}
	recordBundleError := func(message string) {
		_, _ = t.artifacts.UpsertSecurityFindingArtifact(ctx, finding.Namespace, &store.SecurityFindingArtifact{
			FindingID: finding.ID, ExecutionID: t.state.scanCtx.ExecutionID,
			Kind: store.SecurityFindingArtifactSubmissionBundle, Content: json.RawMessage(`{"schema_version":"v1"}`),
			Filename: filename, Status: "error", Error: message, ActorRun: t.state.scanCtx.RunName,
		})
	}
	if t.deps.Blobs == nil {
		if t.deps.BlobsErr != nil {
			err = t.deps.BlobsErr
		} else {
			err = fmt.Errorf("private object store is unavailable")
		}
		recordBundleError(err.Error())
		return Result{Content: err.Error(), IsError: true}, nil
	}
	bundle, err := buildSecurityReportBundle(finding, t.state.scanCtx, candidate, validation, submission, budgetState, builderRun, validatorRun)
	if err != nil {
		recordBundleError("building bundle: " + err.Error())
		return Result{Content: "building bundle: " + err.Error(), IsError: true}, nil
	}
	digest := sha256.Sum256(bundle)
	digestHex := hex.EncodeToString(digest[:])
	objectKey := fmt.Sprintf("%s/%s/%s/%s/%s.zip", securityBundleStorePrefix, finding.Namespace, finding.ScanID, finding.ID, digestHex)
	if err := t.deps.Blobs.Put(ctx, objectKey, bundle, securityBundleMediaType); err != nil {
		recordBundleError("uploading bundle: " + err.Error())
		return Result{Content: "uploading bundle: " + err.Error(), IsError: true}, nil
	}
	_, err = t.artifacts.UpsertSecurityFindingArtifact(ctx, finding.Namespace, &store.SecurityFindingArtifact{FindingID: finding.ID, ExecutionID: t.state.scanCtx.ExecutionID, Kind: store.SecurityFindingArtifactSubmissionBundle, Content: json.RawMessage(`{"schema_version":"v1"}`), S3Key: objectKey, SHA256: digestHex, SizeBytes: int64(len(bundle)), MediaType: securityBundleMediaType, Filename: filename, Status: "ready", ActorRun: t.state.scanCtx.RunName})
	if err != nil {
		return Result{Content: "saving bundle metadata: " + err.Error(), IsError: true}, nil
	}
	if finishReservation != nil {
		if err := finishReservation(true); err != nil {
			return Result{Content: "finalizing durable submission: " + err.Error(), IsError: true}, nil
		}
		finishReservation = nil
	}
	return Result{Content: fmt.Sprintf("Security review bundle uploaded (%s, sha256 %s). Durable submission: %s", filename, digestHex, budgetState)}, nil
}

func buildSecuritySubmissionBundle(finding *store.SecurityFindingRecord, scanCtx SecurityScanContext, candidate securityPoCCandidate, validation securityPoCValidation, markdown, builderRun, validatorRun string) ([]byte, error) {
	return buildSecurityReportBundle(finding, scanCtx, &candidate, &validation, securityBountySubmission{Markdown: markdown}, "", builderRun, validatorRun)
}

func buildSecurityReportBundle(finding *store.SecurityFindingRecord, scanCtx SecurityScanContext, candidate *securityPoCCandidate, validation *securityPoCValidation, submission securityBountySubmission, budgetState, builderRun, validatorRun string) ([]byte, error) {
	files := map[string][]byte{"submission.md": []byte(submission.Markdown)}
	// The structured claim travels with the report so a triager can read the
	// root cause, maximum achievable impact and funds at risk without mining
	// them out of prose.
	if strings.TrimSpace(submission.ImpactClause) != "" {
		claimJSON, _ := json.MarshalIndent(submission, "", "  ")
		files["claim.json"] = append(claimJSON, '\n')
	}
	if candidate != nil {
		var readme strings.Builder
		fmt.Fprintf(&readme, "# Proof of concept\n\n## Setup\n%s\n\n## Command\n```sh\n%s\n```\n\n## Expected output\n```\n%s\n```\n\n## Observed output\n```\n%s\n```\n\n## Teardown\n%s\n\n## Environment\n%s\n", candidate.Setup, candidate.Command, candidate.ExpectedOutput, candidate.ObservedOutput, candidate.Teardown, candidate.Environment)
		files["poc/README.md"] = []byte(readme.String())
		for _, file := range candidate.Files {
			files["poc/"+file.Path] = []byte(file.Content)
		}
	}
	if validation != nil {
		validationJSON, _ := json.MarshalIndent(validation, "", "  ")
		files["validation.json"] = append(validationJSON, '\n')
	}
	hashes := make(map[string]string, len(files))
	for name, body := range files {
		sum := sha256.Sum256(body)
		hashes[name] = hex.EncodeToString(sum[:])
	}
	programLevel, _ := securityProgramImpactLevel(scanCtx, submission.ImpactClause)
	manifest := securityBundleManifest{SchemaVersion: "v1", FindingID: finding.ID.String(), FindingStatus: finding.Status, Fingerprint: finding.Fingerprint, Repository: finding.Repository, Revision: finding.Revision, ScanName: finding.ScanName, ExecutionID: scanCtx.ExecutionID, BuilderRun: builderRun, ValidatorRun: validatorRun, ReportRun: scanCtx.RunName, SeveritySystem: scanCtx.SeveritySystem, ImpactClause: submission.ImpactClause, ProgramLevel: programLevel, BudgetState: budgetState, FilesSHA256: hashes}
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	files["manifest.json"] = append(manifestJSON, '\n')
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Unix(0, 0).UTC()}
		header.SetMode(0o600)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > maxSecurityBundleBytes {
		return nil, fmt.Errorf("bundle exceeds %d bytes", maxSecurityBundleBytes)
	}
	return buf.Bytes(), nil
}

// securityProgramImpactLevel returns the governing program's own severity
// level for a verbatim impact clause. The second result reports whether the
// program published an impact list at all: when it did not, the clause cannot
// be checked and the caller must not pretend otherwise.
func securityProgramImpactLevel(scanCtx SecurityScanContext, clause string) (string, bool) {
	if len(scanCtx.InScopeImpacts) == 0 {
		return "", false
	}
	clause = strings.TrimSpace(clause)
	for _, impact := range scanCtx.InScopeImpacts {
		if impact.Impact == clause {
			return impact.Level, true
		}
	}
	return "", true
}

// validateBountySubmissionClaim enforces the fields programs reject reports
// for omitting, plus the two rules that cause outright rules violations: the
// impact must be selected verbatim from the program's published list, and the
// severity system is never translated.
func validateBountySubmissionClaim(submission securityBountySubmission, scanCtx SecurityScanContext) []string {
	var problems []string
	required := []struct {
		field string
		value string
	}{
		{"impact_clause", submission.ImpactClause},
		{"root_cause", submission.RootCause},
		{"max_achievable_impact", submission.MaxAchievableImpact},
		{"attack_path", submission.AttackPath},
		{"feasibility", submission.Feasibility},
		{"remediation", submission.Remediation},
		{"prior_art", submission.PriorArt},
	}
	for _, entry := range required {
		if strings.TrimSpace(entry.value) == "" {
			problems = append(problems, entry.field+" is required")
		}
	}
	// Funds at risk is required whenever the program measures impact in
	// value. An impact class that permits no measurement must say so rather
	// than leave the field blank.
	if strings.TrimSpace(submission.FundsAtRisk) == "" {
		problems = append(problems, "funds_at_risk is required (state the measured value, or why the impact class permits no measurement)")
	}
	if declared := strings.TrimSpace(submission.SeveritySystem); declared != "" && scanCtx.SeveritySystem != "" && !strings.EqualFold(declared, scanCtx.SeveritySystem) {
		problems = append(problems, fmt.Sprintf("severity_system %q does not match the governing program's %q; severity is never translated between systems", declared, scanCtx.SeveritySystem))
	}
	// Only a complete impact list can prove a clause is not published. A
	// truncated list would otherwise reject a genuinely in-scope clause that
	// fell past the encoding bound.
	if level, published := securityProgramImpactLevel(scanCtx, submission.ImpactClause); published && !scanCtx.ImpactsTruncated &&
		level == "" && strings.TrimSpace(submission.ImpactClause) != "" {
		problems = append(problems, "impact_clause is not one of the program's published in-scope impacts; copy one verbatim instead of writing a new one")
	}
	return problems
}

//nolint:gocyclo // Submission preparation deliberately evaluates all evidence, ranking, precision, and lease gates in order.
func (t *saveSecurityBountySubmissionTool) prepareSecurityResearchSubmission(ctx context.Context, finding *store.SecurityFindingRecord, payload securityBountySubmission) (string, bool, func(bool) error, error) {
	periodDays := t.state.scanCtx.SubmissionBudgetPeriodDays
	budget := t.state.scanCtx.SubmissionBudget
	if t.state.researchStore == nil {
		if periodDays > 0 && budget > 0 {
			return "", false, nil, fmt.Errorf("durable security research storage is required to enforce the configured rolling submission period")
		}
		state, retained, err := t.submissionBudgetState(ctx, finding)
		return state, retained, nil, err
	}
	bound, err := t.state.ensureSecurityResearchContext(ctx)
	if err != nil {
		return "", false, nil, err
	}
	if finding.Status == store.SecurityFindingStatusConfirmed || finding.Status == store.SecurityFindingStatusTriaged {
		sweeps, err := t.state.researchStore.ListSecurityResearchVariantSweeps(ctx, t.state.scanCtx.Namespace, bound.revision.ID)
		if err != nil {
			return "", false, nil, fmt.Errorf("checking the required variant sweep: %w", err)
		}
		completed := false
		for _, sweep := range sweeps {
			if sweep.FindingID != nil && *sweep.FindingID == finding.ID && sweep.Status == store.SecurityVariantSweepCompleted && store.ValidSecurityVariantSweepCompletionEvidence(sweep.Result) {
				completed = true
				break
			}
		}
		if !completed {
			return "", false, nil, fmt.Errorf("a completed variant sweep for this triaged or confirmed finding is required before bounty packaging")
		}
	}
	if periodDays <= 0 || budget <= 0 {
		state, retained, err := t.submissionBudgetState(ctx, finding)
		return state, retained, nil, err
	}
	workflow := securityResearchWorkflow(t.state.scanCtx)
	precision, err := t.state.researchStore.GetSecuritySubmissionPrecision(ctx, t.state.scanCtx.Namespace, bound.target.ID, workflow, nil)
	if err != nil {
		return "", false, nil, fmt.Errorf("reading submission precision: %w", err)
	}
	effectiveBudget, evidenceGate := securitySubmissionPrecisionPolicy(budget, precision)
	if (evidenceGate == "strict-perfect-verification" || evidenceGate == "heightened-independent-reproduction") && finding.Status != store.SecurityFindingStatusConfirmed {
		return "", false, nil, fmt.Errorf("precision policy %s requires a confirmed finding with independent PoC validation", evidenceGate)
	}
	rank, err := t.submissionRank(ctx, finding, evidenceGate)
	if err != nil {
		return "", false, nil, err
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return "", false, nil, fmt.Errorf("encoding durable submission candidate: %w", err)
	}
	candidate, _, err := t.state.researchStore.CreateSecurityResearchSubmission(ctx, t.state.scanCtx.Namespace, &store.SecurityResearchSubmission{
		RevisionID:   bound.revision.ID,
		FindingID:    &finding.ID,
		Workflow:     workflow,
		CandidateKey: finding.Fingerprint,
		Rank:         int32(rank),
		Payload:      rawPayload,
		Status:       "candidate",
	})
	if err != nil {
		return "", false, nil, fmt.Errorf("creating durable submission candidate: %w", err)
	}
	attemptKey := strings.TrimSpace(t.state.scanCtx.ExecutionID)
	if attemptKey == "" {
		attemptKey = strings.TrimSpace(t.state.scanCtx.RunName)
	}
	reservationKey := "bounty-package:" + attemptKey + ":" + finding.Fingerprint
	reservation := &store.SecuritySubmissionReservationResult{Reserved: false, Limit: effectiveBudget}
	if int32(rank) <= effectiveBudget {
		reservation, err = t.state.researchStore.ReserveSecurityResearchSubmission(ctx, t.state.scanCtx.Namespace, store.SecuritySubmissionReservationRequest{
			SubmissionID:   candidate.ID,
			Workflow:       workflow,
			PeriodDays:     periodDays,
			BudgetLimit:    effectiveBudget,
			IdempotencyKey: reservationKey,
		})
		if err != nil {
			return "", false, nil, fmt.Errorf("reserving rolling submission budget: %w", err)
		}
	}
	decision, reason := "submit", fmt.Sprintf("reserved rolling %d-day submission budget", periodDays)
	if int32(rank) > effectiveBudget {
		decision = "retain"
		reason = fmt.Sprintf("candidate rank %d is outside deterministic top-%d", rank, effectiveBudget)
	} else if !reservation.Reserved {
		decision = "retain"
		reason = fmt.Sprintf("rolling %d-day submission budget exhausted (%d/%d used)", periodDays, reservation.Used, reservation.Limit)
	}
	inputs, _ := json.Marshal(map[string]any{
		"published_budget_limit": budget,
		"effective_budget_limit": effectiveBudget,
		"period_days":            periodDays,
		"used":                   reservation.Used,
		"precision":              precision,
		"precision_policy":       "accepted_over_adjudicated_v1",
		"evidence_gate":          evidenceGate,
	})
	_, _, err = t.state.researchStore.CreateSecurityResearchDecisionSnapshot(ctx, t.state.scanCtx.Namespace, &store.SecurityResearchDecisionSnapshot{
		RevisionID:     bound.revision.ID,
		SubmissionID:   &candidate.ID,
		Workflow:       workflow,
		CandidateKey:   finding.Fingerprint,
		Decision:       decision,
		Reason:         reason,
		Rank:           int32(rank),
		Inputs:         inputs,
		IdempotencyKey: "decision:" + reservationKey,
	})
	if err != nil {
		if reservation.Reserved {
			if cleaner, ok := t.state.researchStore.(store.SecuritySubmissionReservationCleanupStore); ok {
				_ = cleaner.VoidSecurityResearchSubmissionReservation(context.Background(), t.state.scanCtx.Namespace, candidate.ID, reservationKey)
			}
		}
		return "", false, nil, fmt.Errorf("recording submission decision: %w", err)
	}
	if !reservation.Reserved {
		return fmt.Sprintf("Submission %s retained as candidate: %s; candidate rank %d.", candidate.ID, reason, rank), true, nil, nil
	}
	finish := func(success bool) error {
		if success {
			return t.state.researchStore.MarkSecurityResearchSubmissionSubmitted(context.Background(), t.state.scanCtx.Namespace, candidate.ID, time.Now().UTC())
		}
		if cleaner, ok := t.state.researchStore.(store.SecuritySubmissionReservationCleanupStore); ok {
			return cleaner.VoidSecurityResearchSubmissionReservation(context.Background(), t.state.scanCtx.Namespace, candidate.ID, reservationKey)
		}
		return nil
	}
	return fmt.Sprintf("submission_id %s; rank %d; rolling %d-day budget reserved (%d/%d used); evidence gate %s", candidate.ID, rank, periodDays, reservation.Used, reservation.Limit, evidenceGate), false, finish, nil
}

// securitySubmissionPrecisionPolicy applies a versioned, conservative feedback
// rule after enough adjudicated outcomes exist. Pending submissions never enter
// the denominator, and feedback can only tighten the published program cap.
func securitySubmissionPrecisionPolicy(published int32, precision *store.SecuritySubmissionPrecision) (int32, string) {
	if published <= 0 {
		return published, "program-default"
	}
	if precision == nil {
		return published, "cold-start"
	}
	adjudicated := precision.Accepted + precision.Duplicate + precision.Informative + precision.Rejected
	if adjudicated < 5 {
		return published, "cold-start-minimum-5"
	}
	effective := published
	switch signal := float64(precision.Accepted) / float64(adjudicated); {
	case signal < 0.2:
		effective = min(effective, 1)
		return effective, "strict-perfect-verification"
	case signal < 0.4:
		effective = min(effective, 2)
		return effective, "heightened-independent-reproduction"
	default:
		return effective, "standard-program-evidence"
	}
}

func (t *saveSecurityBountySubmissionTool) submissionRank(ctx context.Context, finding *store.SecurityFindingRecord, evidenceGate string) (int, error) {
	if finding == nil {
		return 0, fmt.Errorf("finding is required")
	}
	filter := t.state.scopeFilter()
	filter.ExecutionID = ""
	filter.TaskName = ""
	filter.Status = ""
	findings, err := t.state.listFindings(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("evaluating the program's submission budget: %w", err)
	}
	rank := 1
	for _, candidate := range findings {
		if candidate.ID == finding.ID {
			continue
		}
		if _, err := securityReportBundleStatus(&candidate, t.state.scanCtx); err != nil {
			continue
		}
		if (evidenceGate == "strict-perfect-verification" || evidenceGate == "heightened-independent-reproduction") && candidate.Status != store.SecurityFindingStatusConfirmed {
			continue
		}
		if outranksForSubmission(candidate, *finding) {
			rank++
		}
	}
	return rank, nil
}

func (t *saveSecurityBountySubmissionTool) submissionBudgetState(ctx context.Context, finding *store.SecurityFindingRecord) (string, bool, error) {
	budget := t.state.scanCtx.SubmissionBudget
	if budget <= 0 || finding == nil {
		return "", false, nil
	}
	rank, err := t.submissionRank(ctx, finding, "")
	if err != nil {
		return "", false, err
	}
	if int32(rank) > budget {
		return fmt.Sprintf("Submission retained as candidate: scan-wide budget %d is exhausted; candidate rank %d.", budget, rank), true, nil
	}
	return fmt.Sprintf("rank %d of budget %d (scan-wide)", rank, budget), false, nil
}

// outranksForSubmission reports whether other should consume a submission slot
// ahead of finding. Score decides, and equal scores fall back to the
// fingerprint so the ordering is total: without a tie-break every member of a
// tie would claim the same rank and the cap could be exceeded.
func outranksForSubmission(other, finding store.SecurityFindingRecord) bool {
	if other.Score != finding.Score {
		return other.Score > finding.Score
	}
	return other.Fingerprint < finding.Fingerprint
}
