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
}

type securityBountySubmission struct {
	Markdown string `json:"markdown"`
}

type securityBundleManifest struct {
	SchemaVersion string            `json:"schema_version"`
	FindingID     string            `json:"finding_id"`
	FindingStatus string            `json:"finding_status"`
	Fingerprint   string            `json:"fingerprint"`
	Repository    string            `json:"repository"`
	Revision      string            `json:"revision"`
	ScanName      string            `json:"scan_name"`
	ExecutionID   string            `json:"execution_id,omitempty"`
	BuilderRun    string            `json:"builder_run"`
	ValidatorRun  string            `json:"validator_run"`
	ReportRun     string            `json:"report_run"`
	FilesSHA256   map[string]string `json:"files_sha256"`
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
	return json.RawMessage(`{"type":"object","properties":{"confirmed":{"type":"boolean"},"candidate_sha256":{"type":"string"},"command":{"type":"string"},"observed_output":{"type":"string"},"reason":{"type":"string"}},"required":["confirmed","candidate_sha256","command","observed_output","reason"]}`)
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
	return Result{Content: "PoC validation recorded as " + status + "."}, nil
}

type saveSecurityBountySubmissionTool struct {
	state     *securityScanState
	artifacts store.SecurityFindingArtifactStore
	deps      securityBountyArtifactDeps
}

func securityReportBundleStatus(finding *store.SecurityFindingRecord) (string, error) {
	if finding == nil || (finding.Status != store.SecurityFindingStatusConfirmed && finding.Status != store.SecurityFindingStatusTriaged) || finding.DuplicateOf != nil || finding.SuppressedBy != "" || (finding.Severity != "high" && finding.Severity != "critical") {
		return "", fmt.Errorf("finding is not an eligible triaged or confirmed, unsuppressed, non-duplicate high/critical report")
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
	return json.RawMessage(`{"type":"object","properties":{"markdown":{"type":"string"}},"required":["markdown"]}`)
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
	finding, err := boundSecurityFinding(ctx, t.state)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	artifactStatus, err := securityReportBundleStatus(finding)
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
	bundle, err := buildSecurityReportBundle(finding, t.state.scanCtx, candidate, validation, submission.Markdown, builderRun, validatorRun)
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
	return Result{Content: fmt.Sprintf("Security review bundle uploaded (%s, sha256 %s).", filename, digestHex)}, nil
}

func buildSecuritySubmissionBundle(finding *store.SecurityFindingRecord, scanCtx SecurityScanContext, candidate securityPoCCandidate, validation securityPoCValidation, markdown, builderRun, validatorRun string) ([]byte, error) {
	return buildSecurityReportBundle(finding, scanCtx, &candidate, &validation, markdown, builderRun, validatorRun)
}

func buildSecurityReportBundle(finding *store.SecurityFindingRecord, scanCtx SecurityScanContext, candidate *securityPoCCandidate, validation *securityPoCValidation, markdown, builderRun, validatorRun string) ([]byte, error) {
	files := map[string][]byte{"submission.md": []byte(markdown)}
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
	manifest := securityBundleManifest{SchemaVersion: "v1", FindingID: finding.ID.String(), FindingStatus: finding.Status, Fingerprint: finding.Fingerprint, Repository: finding.Repository, Revision: finding.Revision, ScanName: finding.ScanName, ExecutionID: scanCtx.ExecutionID, BuilderRun: builderRun, ValidatorRun: validatorRun, ReportRun: scanCtx.RunName, FilesSHA256: hashes}
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
