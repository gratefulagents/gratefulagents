package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// This file tests the seam every other bug-bounty test leaves untested: one
// offline walk of the whole lane, from a replayed tool document to a packaged
// submission bundle, for a vulnerable fixture and its fixed twin. Nothing here
// touches a chain, a network, Docker, or a real object store: the toolpack runs
// against a stub sandbox that replays a committed `forge test --json` document,
// and the store and blob doubles below live in memory.

// --- lane doubles -----------------------------------------------------------

// bountyLaneStore is the finding store the lane needs: the existing
// fakeSecurityFindingStore (reused verbatim) plus the artifact surface
// RegisterSecurityBountyArtifactTools requires. Without the artifact surface
// the bounty tools are never registered at all, which is itself part of the
// lane's contract.
type bountyLaneStore struct {
	*fakeSecurityFindingStore
	artifacts map[string]store.SecurityFindingArtifact
}

func newBountyLaneStore() *bountyLaneStore {
	return &bountyLaneStore{
		fakeSecurityFindingStore: newFakeSecurityFindingStore(),
		artifacts:                map[string]store.SecurityFindingArtifact{},
	}
}

func bountyArtifactKey(findingID uuid.UUID, executionID, kind string) string {
	return findingID.String() + "|" + executionID + "|" + kind
}

func (s *bountyLaneStore) UpsertSecurityFindingArtifact(_ context.Context, namespace string, artifact *store.SecurityFindingArtifact) (*store.SecurityFindingArtifact, error) {
	if namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	stored := *artifact
	if stored.ID == uuid.Nil {
		stored.ID = uuid.New()
	}
	s.artifacts[bountyArtifactKey(stored.FindingID, stored.ExecutionID, stored.Kind)] = stored
	copied := stored
	return &copied, nil
}

func (s *bountyLaneStore) GetSecurityFindingArtifact(_ context.Context, namespace string, findingID uuid.UUID, executionID, kind string) (*store.SecurityFindingArtifact, error) {
	if namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	artifact, ok := s.artifacts[bountyArtifactKey(findingID, executionID, kind)]
	if !ok {
		return nil, nil
	}
	copied := artifact
	return &copied, nil
}

func (s *bountyLaneStore) ListSecurityFindingArtifacts(_ context.Context, _ string, findingID uuid.UUID, executionID string) ([]store.SecurityFindingArtifact, error) {
	var out []store.SecurityFindingArtifact
	for _, artifact := range s.artifacts {
		if artifact.FindingID == findingID && artifact.ExecutionID == executionID {
			out = append(out, artifact)
		}
	}
	slices.SortFunc(out, func(a, b store.SecurityFindingArtifact) int { return strings.Compare(a.Kind, b.Kind) })
	return out, nil
}

// bountyLaneBlobs stands in for the platform's private S3 bucket. Every write
// is kept so the test can prove that a rejected submission uploads nothing and
// that two builds of the same submission are byte-identical.
type bountyLaneBlobs struct {
	puts []bountyLaneBlob
}

type bountyLaneBlob struct {
	key       string
	content   []byte
	mediaType string
}

func (b *bountyLaneBlobs) Put(_ context.Context, key string, content []byte, mediaType string) error {
	b.puts = append(b.puts, bountyLaneBlob{key: key, content: append([]byte(nil), content...), mediaType: mediaType})
	return nil
}

// --- lane fixtures ----------------------------------------------------------

const (
	bountyLaneScan        = "bounty-lane"
	bountyLaneNamespace   = "default"
	bountyLaneExecution   = "exec-bounty-lane-1"
	bountyLaneRecordRun   = "bounty-lane-execution"
	bountyLaneRepository  = "https://example.invalid/escrow"
	bountyLaneRevision    = "9f1c2b3d4e5f60718293a4b5c6d7e8f901234567"
	bountyLaneBuilderRun  = "bounty-lane-poc-builder"
	bountyLaneValidatorRn = "bounty-lane-poc-validator"
	bountyLaneReportRun   = "bounty-lane-report-writer"
	bountyLaneScannerRun  = "bounty-lane-scanner"
	bountyLaneSeveritySys = "immunefi-v2.3"
	bountyLaneCriticalImp = "Permanent freezing of funds"
	bountyLaneMediumImp   = "Griefing (no profit motive) that disrupts settlement"
)

// bountyLaneProgram is the governing program's published scope as the
// controller stamps it: the severity system, the verbatim in-scope impact
// clauses with the program's own levels, and the submission budget.
func bountyLaneProgram() []SecurityProgramImpactClause {
	return []SecurityProgramImpactClause{
		{Level: "critical", Impact: bountyLaneCriticalImp},
		{Level: "high", Impact: "Theft of unclaimed yield"},
	}
}

// bountyLaneContext builds one role's scan context. Every role of the lane
// shares the execution id and the scan record key, because the builder, the
// validator and the report writer are separate AgentRuns of ONE deterministic
// execution: without the shared execution id the post-scripts could not even
// see the finding the scanner run persisted.
func bountyLaneContext(runName, fingerprint string, impacts []SecurityProgramImpactClause) SecurityScanContext {
	return SecurityScanContext{
		ScanName:              bountyLaneScan,
		Namespace:             bountyLaneNamespace,
		RunName:               runName,
		RecordRunName:         bountyLaneRecordRun,
		ExecutionID:           bountyLaneExecution,
		Repository:            bountyLaneRepository,
		Revision:              bountyLaneRevision,
		DedupePermille:        -1,
		SessionID:             uuid.New(),
		PostScripts:           []string{"poc-builder", "poc-validator", "report-writer"},
		PostScriptFingerprint: fingerprint,
		SeveritySystem:        bountyLaneSeveritySys,
		InScopeImpacts:        impacts,
		SubmissionBudget:      2,
	}
}

// bountyLaneRegistry wires one role's tools over the shared store and blob
// doubles. The bounty tools are registered through the real entry point, so a
// missing artifact surface or an absent post-script binding would leave them
// unregistered exactly as it would in production.
func bountyLaneRegistry(t *testing.T, laneStore *bountyLaneStore, blobs SecurityBountyBlobStore, scanCtx SecurityScanContext) *Registry {
	t.Helper()
	registry := newSecurityTestRegistryWithCtx(t, laneStore, nil, scanCtx)
	RegisterSecurityBountyArtifactTools(registry, securityTestState(t, registry), blobs, nil)
	return registry
}

type bountyLaneSandbox struct {
	native securitytoolpacks.NativeResult
}

func (s *bountyLaneSandbox) Execute(context.Context, securitytoolpacks.ExecutionRequest) securitytoolpacks.NativeResult {
	return s.native
}

func bountyLaneDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func bountyLaneFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "bounty-lane", name))
	if err != nil {
		t.Fatalf("read lane fixture: %v", err)
	}
	return data
}

// runForgeForkLane replays a committed `forge test --json` document through the
// real registry, adapter and runner. The tool document is the only input: no
// fork endpoint is dialled, because the sandbox that would exec forge is a
// stub. The operator-authorized endpoint alias still has to exist, otherwise
// the fork pack refuses to build an invocation at all.
func runForgeForkLane(t *testing.T, document []byte, exitCode int, bounded *securitytoolpacks.BoundedScope) securitytoolpacks.Result {
	t.Helper()
	t.Setenv("GA_SECURITY_EVM_FORK_ENDPOINTS", "mainnet-archive")
	registry, err := securitytoolpacks.NewRegistry(securitytoolpacks.DefaultManifest(bountyLaneDigest([]byte("bounty-lane-image")), nil))
	if err != nil {
		t.Fatalf("build toolpack registry: %v", err)
	}
	sandbox := &bountyLaneSandbox{native: securitytoolpacks.NativeResult{
		Output:   document,
		ExitCode: exitCode,
		Bounded:  bounded,
	}}
	seed := int64(20260814)
	return securitytoolpacks.NewRunner(registry, sandbox).Run(context.Background(), securitytoolpacks.RunConfig{
		Tool: "forge-fork-test",
		Target: securitytoolpacks.Target{
			Type: "foundry_project", Locator: "contracts", Revision: bountyLaneRevision,
			Digest:    bountyLaneDigest([]byte("escrow-foundry-project")),
			MediaType: "application/vnd.gratefulagents.foundry-security-project.v1+directory",
		},
		Arguments: map[string]string{
			"fork_endpoint":     "mainnet-archive",
			"chain_id":          "1",
			"fork_block_number": "21000000",
			"fork_block_hash":   "0x" + strings.Repeat("ab", 32),
		},
		Seed: &seed,
	})
}

// ingestLaneFindings pushes the runner's normalized records through the real
// ingest tool, which is how a toolpack result becomes a finding a post-script
// can bind to. It returns the persisted finding.
func ingestLaneFindings(t *testing.T, laneStore *bountyLaneStore, records []security.ScannerRecord) store.SecurityFindingRecord {
	t.Helper()
	scannerRegistry := newSecurityTestRegistryWithCtx(t, laneStore, nil, bountyLaneContext(bountyLaneScannerRun, "", bountyLaneProgram()))
	payload, err := json.Marshal(map[string]any{"records": records})
	if err != nil {
		t.Fatal(err)
	}
	if result := execTool(t, scannerRegistry, "ingest_scanner_results", string(payload)); result.IsError {
		t.Fatalf("ingest_scanner_results rejected the lane's records: %s", result.Content)
	}
	findings, err := laneStore.ListSecurityFindings(context.Background(), store.SecurityFindingFilter{
		Namespace: bountyLaneNamespace, ScanName: bountyLaneScan, ExecutionID: bountyLaneExecution,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("lane persisted %d findings, want exactly 1: %+v", len(findings), findings)
	}
	return findings[0]
}

func bountyLanePoCInput(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(securityPoCCandidate{
		Setup:          "forge install && forge build",
		Command:        "forge test --match-test test_attackerDrainsEscrowAfterReentry --fork-block-number 21000000",
		ExpectedOutput: "escrow balance unchanged",
		ObservedOutput: "[FAIL] test_attackerDrainsEscrowAfterReentry: escrow balance 0 != 4102000000000000000000",
		Teardown:       "none: the fork is discarded with the anvil process",
		Environment:    "foundry 1.7.1, forked mainnet at block 21000000",
		Files: []securityPoCFile{
			{Path: "test/EscrowForkTest.t.sol", Content: "// SPDX-License-Identifier: MIT\ncontract EscrowForkTest {}\n"},
			{Path: "script/replay.sh", Content: "#!/bin/sh\nforge test --match-test test_attackerDrainsEscrowAfterReentry\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func bountyLaneValidationInput(t *testing.T, mutate func(*securityPoCValidation)) string {
	t.Helper()
	validation := securityPoCValidation{
		Confirmed:             true,
		Command:               "forge test --match-test test_attackerDrainsEscrowAfterReentry",
		ObservedOutput:        "[FAIL] test_attackerDrainsEscrowAfterReentry: escrow balance 0 != 4102000000000000000000",
		Reason:                "withdraw() clears the escrow index before transferring, so the reentrant call re-reads a stale balance",
		ReproducibilityClass:  string(securitytoolpacks.ReproducibilityDeterministic),
		TargetCodeExecuted:    true,
		NegativeControlRan:    true,
		NegativeControlPassed: true,
		OracleCanFail:         true,
		OracleEvidence:        "mutation: restored the index clear after the transfer and the assertion failed as expected",
	}
	if mutate != nil {
		mutate(&validation)
	}
	raw, err := json.Marshal(validation)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func bountyLaneSubmissionInput(t *testing.T, impactClause string) string {
	t.Helper()
	submission := completeBountySubmission()
	submission.Markdown = "## Escrow reentrancy freezes deposits\n\nForked-state reproduction at block 21000000."
	submission.ImpactClause = impactClause
	submission.SeveritySystem = bountyLaneSeveritySys
	raw, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func bountyLaneBundleFiles(t *testing.T, bundle []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatalf("bundle is not a readable zip: %v", err)
	}
	files := map[string][]byte{}
	for _, file := range reader.File {
		body, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = data
	}
	return files
}

// --- the vulnerable direction ----------------------------------------------

// TestBountyLaneVulnerableFixtureProducesAPackagedSubmission walks the lane end
// to end: replayed forge document -> finding -> PoC candidate (builder run) ->
// independent validation (validator run) -> packaged submission bundle (report
// run). Each rejected step in between is a gate that a real submission would
// have failed on, driven through the shipped tool implementations rather than
// through their helpers.
func TestBountyLaneVulnerableFixtureProducesAPackagedSubmission(t *testing.T) {
	result := runForgeForkLane(t, bountyLaneFixture(t, "forge-fork-test-vulnerable.json"), 1, nil)
	if result.Status != securitytoolpacks.StatusFindings {
		t.Fatalf("vulnerable fixture status = %q, want %q (errors: %v)", result.Status, securitytoolpacks.StatusFindings, result.Errors)
	}
	if len(result.Findings) != 1 || result.Findings[0].RuleID != "forge-fork-assertion-failed" {
		t.Fatalf("vulnerable fixture findings = %+v, want one forge-fork-assertion-failed record", result.Findings)
	}

	laneStore := newBountyLaneStore()
	blobs := &bountyLaneBlobs{}
	finding := ingestLaneFindings(t, laneStore, result.Findings)
	if finding.Status != store.SecurityFindingStatusOpen {
		t.Fatalf("ingested finding status = %q, want open before validation", finding.Status)
	}

	builder := bountyLaneRegistry(t, laneStore, blobs, bountyLaneContext(bountyLaneBuilderRun, finding.Fingerprint, bountyLaneProgram()))
	validator := bountyLaneRegistry(t, laneStore, blobs, bountyLaneContext(bountyLaneValidatorRn, finding.Fingerprint, bountyLaneProgram()))
	reporter := bountyLaneRegistry(t, laneStore, blobs, bountyLaneContext(bountyLaneReportRun, finding.Fingerprint, bountyLaneProgram()))

	pocInput := bountyLanePoCInput(t)
	if result := execTool(t, builder, "save_security_poc", pocInput); result.IsError {
		t.Fatalf("save_security_poc rejected the candidate: %s", result.Content)
	}
	candidate, err := laneStore.GetSecurityFindingArtifact(context.Background(), bountyLaneNamespace, finding.ID, bountyLaneExecution, store.SecurityFindingArtifactPoCCandidate)
	if err != nil || candidate == nil {
		t.Fatalf("PoC candidate was not stored: %v", err)
	}
	if candidate.ActorRun != bountyLaneBuilderRun {
		t.Fatalf("candidate builder provenance = %q, want %q", candidate.ActorRun, bountyLaneBuilderRun)
	}

	// Gate: provenance. A builder that grades its own homework proves nothing,
	// so validation from the builder's own AgentRun must be refused even when
	// the evidence is otherwise complete.
	sameRun := bountyLaneRegistry(t, laneStore, blobs, bountyLaneContext(bountyLaneBuilderRun, finding.Fingerprint, bountyLaneProgram()))
	selfValidation := execTool(t, sameRun, "validate_security_poc", bountyLaneValidationInput(t, func(v *securityPoCValidation) {
		v.CandidateSHA256 = candidate.SHA256
	}))
	if !selfValidation.IsError || !strings.Contains(selfValidation.Content, "different AgentRun") {
		t.Fatalf("same-run validation was accepted: %+v", selfValidation)
	}

	// Gate: the validation must bind the exact candidate it reproduced, or a
	// later candidate edit would silently inherit an old confirmation.
	staleBinding := execTool(t, validator, "validate_security_poc", bountyLaneValidationInput(t, func(v *securityPoCValidation) {
		v.CandidateSHA256 = strings.Repeat("0", 64)
	}))
	if !staleBinding.IsError || !strings.Contains(staleBinding.Content, "candidate_sha256 does not match") {
		t.Fatalf("validation bound to the wrong candidate was accepted: %+v", staleBinding)
	}

	// Gate: harness health. A confirmation with an oracle that was never shown
	// to fail is a green check with nothing behind it.
	blindOracle := execTool(t, validator, "validate_security_poc", bountyLaneValidationInput(t, func(v *securityPoCValidation) {
		v.CandidateSHA256 = candidate.SHA256
		v.OracleCanFail, v.OracleEvidence = false, ""
	}))
	if !blindOracle.IsError || !strings.Contains(blindOracle.Content, "oracle_can_fail requires oracle_evidence") {
		t.Fatalf("validation without oracle evidence was accepted: %+v", blindOracle)
	}
	if artifact, _ := laneStore.GetSecurityFindingArtifact(context.Background(), bountyLaneNamespace, finding.ID, bountyLaneExecution, store.SecurityFindingArtifactPoCValidation); artifact != nil {
		t.Fatalf("a rejected validation was persisted anyway: %+v", artifact)
	}

	confirmed := execTool(t, validator, "validate_security_poc", bountyLaneValidationInput(t, func(v *securityPoCValidation) {
		v.CandidateSHA256 = candidate.SHA256
	}))
	if confirmed.IsError {
		t.Fatalf("validate_security_poc rejected a complete reproduction: %s", confirmed.Content)
	}
	validated, err := laneStore.GetSecurityFinding(context.Background(), bountyLaneNamespace, finding.ID)
	if err != nil || validated == nil || validated.Status != store.SecurityFindingStatusConfirmed {
		t.Fatalf("finding status after validation = %+v, %v; want confirmed", validated, err)
	}

	// Gate: the impact clause is the program's, not the reporter's. An invented
	// clause is a rules violation on every major platform, so the lane must
	// fail closed before anything is uploaded.
	invented := execTool(t, reporter, "save_security_bounty_submission", bountyLaneSubmissionInput(t, "Loss of protocol dignity"))
	if !invented.IsError || !strings.Contains(invented.Content, "published in-scope impacts") {
		t.Fatalf("an invented impact clause was packaged: %+v", invented)
	}
	if len(blobs.puts) != 0 {
		t.Fatalf("a rejected submission uploaded %d objects", len(blobs.puts))
	}
	if bundle, _ := laneStore.GetSecurityFindingArtifact(context.Background(), bountyLaneNamespace, finding.ID, bountyLaneExecution, store.SecurityFindingArtifactSubmissionBundle); bundle == nil || bundle.Status != "generating" {
		t.Fatalf("rejected submission left bundle metadata %+v, want the builder's invalidated placeholder", bundle)
	}

	packaged := execTool(t, reporter, "save_security_bounty_submission", bountyLaneSubmissionInput(t, bountyLaneCriticalImp))
	if packaged.IsError {
		t.Fatalf("save_security_bounty_submission rejected a complete submission: %s", packaged.Content)
	}
	// A second build of the same submission must be byte-identical: a bundle
	// whose bytes drift cannot be re-verified against the digest a triager was
	// given.
	if repeat := execTool(t, reporter, "save_security_bounty_submission", bountyLaneSubmissionInput(t, bountyLaneCriticalImp)); repeat.IsError {
		t.Fatalf("rebuilding the submission failed: %s", repeat.Content)
	}
	if len(blobs.puts) != 2 {
		t.Fatalf("uploaded %d bundles, want 2", len(blobs.puts))
	}
	if blobs.puts[0].key != blobs.puts[1].key || !bytes.Equal(blobs.puts[0].content, blobs.puts[1].content) {
		t.Fatalf("bundle is not deterministic: key %q vs %q, %d vs %d bytes",
			blobs.puts[0].key, blobs.puts[1].key, len(blobs.puts[0].content), len(blobs.puts[1].content))
	}
	if blobs.puts[0].mediaType != securityBundleMediaType {
		t.Errorf("bundle media type = %q, want %q", blobs.puts[0].mediaType, securityBundleMediaType)
	}
	// Byte identity across two builds only survives because the entry metadata
	// is pinned. A wall-clock timestamp in the zip header would make the digest
	// a triager was given drift between two builds minutes apart, and this test
	// would only notice it by luck.
	archive, err := zip.NewReader(bytes.NewReader(blobs.puts[0].content), int64(len(blobs.puts[0].content)))
	if err != nil {
		t.Fatalf("bundle is not a readable zip: %v", err)
	}
	for _, file := range archive.File {
		if !file.Modified.Equal(time.Unix(0, 0).UTC()) {
			t.Errorf("bundle entry %q carries a wall-clock timestamp %s, not the pinned epoch", file.Name, file.Modified)
		}
	}

	files := bountyLaneBundleFiles(t, blobs.puts[0].content)
	wantEntries := []string{"claim.json", "manifest.json", "poc/README.md", "poc/script/replay.sh", "poc/test/EscrowForkTest.t.sol", "submission.md", "validation.json"}
	if got := slices.Sorted(maps.Keys(files)); !slices.Equal(got, wantEntries) {
		t.Fatalf("bundle entries = %v, want %v", got, wantEntries)
	}
	var manifest securityBundleManifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatalf("manifest.json is not readable: %v", err)
	}
	if manifest.ImpactClause != bountyLaneCriticalImp || manifest.ProgramLevel != "critical" {
		t.Errorf("manifest impact clause/level = %q/%q, want %q/critical", manifest.ImpactClause, manifest.ProgramLevel, bountyLaneCriticalImp)
	}
	if manifest.SeveritySystem != bountyLaneSeveritySys {
		t.Errorf("manifest severity system = %q, want %q", manifest.SeveritySystem, bountyLaneSeveritySys)
	}
	if manifest.BudgetState != "rank 1 of budget 2 (scan-wide)" {
		t.Errorf("manifest budget state = %q", manifest.BudgetState)
	}
	// The manifest is what carries provenance off the platform, so the two
	// runs it names must be the two runs that actually did the work.
	if manifest.BuilderRun != bountyLaneBuilderRun || manifest.ValidatorRun != bountyLaneValidatorRn || manifest.ReportRun != bountyLaneReportRun {
		t.Errorf("manifest provenance = builder %q, validator %q, report %q", manifest.BuilderRun, manifest.ValidatorRun, manifest.ReportRun)
	}
	if manifest.FindingStatus != store.SecurityFindingStatusConfirmed || manifest.Fingerprint != finding.Fingerprint {
		t.Errorf("manifest finding = %q/%q, want confirmed/%s", manifest.FindingStatus, manifest.Fingerprint, finding.Fingerprint)
	}
	for name := range files {
		if name == "manifest.json" {
			continue
		}
		sum := sha256.Sum256(files[name])
		if manifest.FilesSHA256[name] != hex.EncodeToString(sum[:]) {
			t.Errorf("manifest hash for %q does not cover the packaged bytes", name)
		}
	}
	if !bytes.Contains(files["poc/README.md"], []byte("forge test --match-test test_attackerDrainsEscrowAfterReentry")) {
		t.Errorf("PoC transcript lost the reproduction command: %s", files["poc/README.md"])
	}
	if !bytes.Contains(files["validation.json"], []byte("negative_control_passed")) {
		t.Errorf("validation.json lost the harness-health evidence: %s", files["validation.json"])
	}
	stored, err := laneStore.GetSecurityFindingArtifact(context.Background(), bountyLaneNamespace, finding.ID, bountyLaneExecution, store.SecurityFindingArtifactSubmissionBundle)
	if err != nil || stored == nil || stored.Status != "ready" || stored.S3Key != blobs.puts[1].key {
		t.Fatalf("bundle metadata = %+v, %v; want a ready record pointing at the uploaded object", stored, err)
	}
	if !strings.HasSuffix(stored.Filename, "-bounty-submission.zip") {
		t.Errorf("bundle filename = %q, want a bounty-submission name for a confirmed finding", stored.Filename)
	}
}

// TestBountyLaneSeverityFloorFollowsTheProgramsPublishedImpacts drives the same
// lane with a medium-severity finding under two governing programs. The floor
// is the program's own published table, not a platform constant: a program that
// publishes medium impacts pays for mediums, and one that publishes only high
// and above does not.
func TestBountyLaneSeverityFloorFollowsTheProgramsPublishedImpacts(t *testing.T) {
	mediumProgram := append(bountyLaneProgram(), SecurityProgramImpactClause{Level: "medium", Impact: bountyLaneMediumImp})

	for _, testCase := range []struct {
		name         string
		impacts      []SecurityProgramImpactClause
		impactClause string
		wantPackaged bool
		wantMessage  string
	}{
		{
			name:         "program publishing medium impacts packages a medium finding",
			impacts:      mediumProgram,
			impactClause: bountyLaneMediumImp,
			wantPackaged: true,
		},
		{
			// The clause here is a published critical one, so the submission is
			// rejected for the finding's severity and not for an unlisted
			// clause: this is the floor gate, isolated.
			name:         "program publishing only high and above rejects a medium finding",
			impacts:      bountyLaneProgram(),
			impactClause: bountyLaneCriticalImp,
			wantMessage:  `lowest published severity "high"`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := runForgeForkLane(t, bountyLaneFixture(t, "forge-fork-test-vulnerable.json"), 1, nil)
			records := result.Findings
			if len(records) != 1 {
				t.Fatalf("lane produced %d records, want 1", len(records))
			}
			// The forge-json adapter stamps every failed assertion "high", so a
			// medium chain finding cannot come out of this pack; the ingested
			// record carries the severity the triager assigned instead.
			records[0].Severity = "medium"

			laneStore := newBountyLaneStore()
			blobs := &bountyLaneBlobs{}
			finding := ingestLaneFindings(t, laneStore, records)
			if finding.Severity != "medium" {
				t.Fatalf("ingested severity = %q, want medium", finding.Severity)
			}

			builder := bountyLaneRegistry(t, laneStore, blobs, bountyLaneContext(bountyLaneBuilderRun, finding.Fingerprint, testCase.impacts))
			validator := bountyLaneRegistry(t, laneStore, blobs, bountyLaneContext(bountyLaneValidatorRn, finding.Fingerprint, testCase.impacts))
			reporter := bountyLaneRegistry(t, laneStore, blobs, bountyLaneContext(bountyLaneReportRun, finding.Fingerprint, testCase.impacts))

			if saved := execTool(t, builder, "save_security_poc", bountyLanePoCInput(t)); saved.IsError {
				t.Fatalf("save_security_poc: %s", saved.Content)
			}
			candidate, err := laneStore.GetSecurityFindingArtifact(context.Background(), bountyLaneNamespace, finding.ID, bountyLaneExecution, store.SecurityFindingArtifactPoCCandidate)
			if err != nil || candidate == nil {
				t.Fatalf("PoC candidate was not stored: %v", err)
			}
			if confirmed := execTool(t, validator, "validate_security_poc", bountyLaneValidationInput(t, func(v *securityPoCValidation) {
				v.CandidateSHA256 = candidate.SHA256
			})); confirmed.IsError {
				t.Fatalf("validate_security_poc: %s", confirmed.Content)
			}

			packaged := execTool(t, reporter, "save_security_bounty_submission", bountyLaneSubmissionInput(t, testCase.impactClause))
			if testCase.wantPackaged {
				if packaged.IsError {
					t.Fatalf("a medium finding was rejected by a program that publishes mediums: %s", packaged.Content)
				}
				if len(blobs.puts) != 1 {
					t.Fatalf("uploaded %d bundles, want 1", len(blobs.puts))
				}
				var manifest securityBundleManifest
				if err := json.Unmarshal(bountyLaneBundleFiles(t, blobs.puts[0].content)["manifest.json"], &manifest); err != nil {
					t.Fatal(err)
				}
				if manifest.ProgramLevel != "medium" || manifest.ImpactClause != bountyLaneMediumImp {
					t.Errorf("manifest level/clause = %q/%q, want medium/%q", manifest.ProgramLevel, manifest.ImpactClause, bountyLaneMediumImp)
				}
				return
			}
			if !packaged.IsError || !strings.Contains(packaged.Content, testCase.wantMessage) {
				t.Fatalf("submission result = %+v, want a rejection mentioning %q", packaged, testCase.wantMessage)
			}
			if len(blobs.puts) != 0 {
				t.Fatalf("a below-floor finding uploaded %d bundles", len(blobs.puts))
			}
		})
	}
}

// --- the fixed direction ----------------------------------------------------

// TestBountyLaneFixedFixtureEndsInABoundedNegative walks the same lane against
// the fixed fixture. The lane must end in an explicit bounded negative that
// carries what it was bounded by — never a "pass", never a finding, and never a
// packageable bundle.
func TestBountyLaneFixedFixtureEndsInABoundedNegative(t *testing.T) {
	// The bounds come from the executor, which is the only layer that knows the
	// harness, corpus and campaign a clean run was bounded by.
	bounded := &securitytoolpacks.BoundedScope{
		Harness: "test/EscrowForkTest.t.sol:EscrowForkTest",
		Corpus:  "2 forked-state assertions replayed from the committed fixture",
		Seeds:   []string{"20260814"},
		Bounds:  "1 fork at mainnet block 21000000, 256 fuzz runs per assertion",
	}
	result := runForgeForkLane(t, bountyLaneFixture(t, "forge-fork-test-fixed.json"), 0, bounded)

	if result.Status != securitytoolpacks.StatusNotFoundUnder {
		t.Fatalf("fixed fixture status = %q, want %q (errors: %v)", result.Status, securitytoolpacks.StatusNotFoundUnder, result.Errors)
	}
	// The retired "pass" verdict claimed safety a bounded run cannot claim.
	if result.Status == securitytoolpacks.StatusPass {
		t.Fatal("a clean run must never be reported as a pass")
	}
	if len(result.Findings) != 0 {
		t.Fatalf("fixed fixture produced findings: %+v", result.Findings)
	}
	if result.Bounded == nil {
		t.Fatal("a bounded negative without its bounds cannot be interpreted")
	}
	if result.Bounded.Harness == "" || result.Bounded.Corpus == "" || len(result.Bounded.Seeds) == 0 || result.Bounded.Bounds == "" {
		t.Fatalf("bounded scope is incomplete: %+v", result.Bounded)
	}
	// Coverage is what the negative is bounded over, so a bounded negative with
	// nothing examined would be a claim about untested code.
	if result.Bounded.Coverage == "" || len(result.Coverage.Examined) != 2 {
		t.Fatalf("bounded coverage = %q over %v", result.Bounded.Coverage, result.Coverage.Examined)
	}
	if len(result.Coverage.Skipped) != 0 || len(result.Coverage.Uncovered) != 0 {
		t.Fatalf("clean run reported skipped/uncovered assets: %+v", result.Coverage)
	}

	laneStore := newBountyLaneStore()
	blobs := &bountyLaneBlobs{}
	// Nothing is ingested, because there is nothing to ingest. The rest of the
	// lane is then unreachable by construction: a post-script pointed at the
	// vulnerable run's fingerprint finds no finding to bind, so no PoC, no
	// validation and no bundle can be created for a fixed target.
	builder := bountyLaneRegistry(t, laneStore, blobs, bountyLaneContext(bountyLaneBuilderRun, "fp-from-the-vulnerable-run", bountyLaneProgram()))
	unbound := execTool(t, builder, "save_security_poc", bountyLanePoCInput(t))
	if !unbound.IsError || !strings.Contains(unbound.Content, "no finding with fingerprint") {
		t.Fatalf("save_security_poc bound a PoC to a finding that does not exist: %+v", unbound)
	}
	if len(laneStore.artifacts) != 0 {
		t.Fatalf("the fixed lane wrote %d artifacts: %+v", len(laneStore.artifacts), laneStore.artifacts)
	}
	if len(blobs.puts) != 0 {
		t.Fatalf("the fixed lane uploaded %d bundles", len(blobs.puts))
	}
}
