package tools

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestValidatePoCFilesRejectsTraversalAndOversize(t *testing.T) {
	for _, name := range []string{"../secret", "/etc/passwd", "poc/../secret", `..\\secret`, "README.md", "readme.MD"} {
		if err := validatePoCFiles([]securityPoCFile{{Path: name, Content: "x"}}); err == nil {
			t.Fatalf("validatePoCFiles(%q) accepted an unsafe path", name)
		}
	}
	if err := validatePoCFiles([]securityPoCFile{{Path: "poc.txt", Content: string(bytes.Repeat([]byte("x"), maxSecurityPoCFileBytes+1))}}); err == nil {
		t.Fatal("validatePoCFiles accepted an oversized file")
	}
}

func TestBuildSecuritySubmissionBundleIsDeterministicAndScoped(t *testing.T) {
	finding := &store.SecurityFindingRecord{
		ID:          uuid.MustParse("00000000-0000-0000-0000-000000000011"),
		ScanID:      uuid.MustParse("00000000-0000-0000-0000-000000000022"),
		Fingerprint: "fp-canary", Repository: "https://example.invalid/repo", Revision: "abc123", ScanName: "bounty",
	}
	candidate := securityPoCCandidate{
		Setup: "seed CANARY", Command: "go test ./...", ExpectedOutput: "CANARY", ObservedOutput: "CANARY",
		Teardown: "none", Environment: "go1.x", Files: []securityPoCFile{{Path: "repro_test.go", Content: "package repro"}},
	}
	validation := securityPoCValidation{Confirmed: true, Command: candidate.Command, ObservedOutput: "CANARY", Reason: "reproduced"}
	ctx := SecurityScanContext{ScanName: "bounty", RunName: "report-run", ExecutionID: "exec-1"}
	first, err := buildSecuritySubmissionBundle(finding, ctx, candidate, validation, "## Title\nCanary", "builder-run", "validator-run")
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildSecuritySubmissionBundle(finding, ctx, candidate, validation, "## Title\nCanary", "builder-run", "validator-run")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("bundle is not deterministic")
	}

	reader, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
		body, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("unrelated-workspace-secret")) {
			t.Fatal("bundle leaked unrelated workspace content")
		}
	}
	want := []string{"manifest.json", "poc/README.md", "poc/repro_test.go", "submission.md", "validation.json"}
	if !slices.Equal(names, want) {
		t.Fatalf("bundle entries = %v, want %v", names, want)
	}
}

func TestBuildTriagedSecurityReviewBundleWithoutPoC(t *testing.T) {
	finding := &store.SecurityFindingRecord{
		ID: uuid.MustParse("00000000-0000-0000-0000-000000000033"), Fingerprint: "fp-review",
		Repository: "https://example.invalid/repo", Revision: "def456", ScanName: "bounty",
		Status: store.SecurityFindingStatusTriaged,
	}
	ctx := SecurityScanContext{ScanName: "bounty", RunName: "report-run", ExecutionID: "exec-2"}
	bundle, err := buildSecurityReportBundle(finding, ctx, nil, nil, securityBountySubmission{Markdown: "## Title\nNeeds runtime validation"}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
		if file.Name == "manifest.json" {
			body, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(body)
			_ = body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, []byte(`"finding_status": "triaged"`)) {
				t.Fatalf("manifest does not label the bundle triaged: %s", data)
			}
		}
	}
	want := []string{"manifest.json", "submission.md"}
	if !slices.Equal(names, want) {
		t.Fatalf("triaged bundle entries = %v, want %v", names, want)
	}
}

func TestSecurityReportBundleStatusIncludesTriaged(t *testing.T) {
	base := store.SecurityFindingRecord{Severity: "high"}
	for _, tc := range []struct {
		name, status, want string
		wantErr            bool
	}{
		{name: "confirmed submission", status: store.SecurityFindingStatusConfirmed, want: "ready"},
		{name: "triaged review", status: store.SecurityFindingStatusTriaged, want: "review"},
		{name: "open rejected", status: store.SecurityFindingStatusOpen, wantErr: true},
		{name: "terminal rejected", status: store.SecurityFindingStatusAcceptedRisk, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finding := base
			finding.Status = tc.status
			got, err := securityReportBundleStatus(&finding, bountyScanContextWithProgram())
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("securityReportBundleStatus() = %q, %v; want %q, error=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestSecurityReportBundleStatusFollowsProgramPublishedLevels(t *testing.T) {
	t.Parallel()
	mediumProgram := bountyScanContextWithProgram()
	mediumProgram.InScopeImpacts = append(mediumProgram.InScopeImpacts, SecurityProgramImpactClause{Level: "medium", Impact: "Griefing with no profit motive"})
	truncatedMediumProgram := mediumProgram
	truncatedMediumProgram.ImpactsTruncated = true
	noProgram := SecurityScanContext{ScanName: "bounty", RunName: "report-run", ExecutionID: "exec-3"}

	for _, tc := range []struct {
		name     string
		severity string
		scanCtx  SecurityScanContext
		want     string
		wantErr  bool
	}{
		{name: "program publishing mediums packages a medium", severity: "medium", scanCtx: mediumProgram, want: "ready"},
		{name: "program publishing only high and above rejects a medium", severity: "medium", scanCtx: bountyScanContextWithProgram(), wantErr: true},
		{name: "program publishing only high and above keeps packaging highs", severity: "high", scanCtx: bountyScanContextWithProgram(), want: "ready"},
		{name: "no program scope keeps the high floor", severity: "medium", scanCtx: noProgram, wantErr: true},
		{name: "no program scope still packages a high", severity: "high", scanCtx: noProgram, want: "ready"},
		{name: "truncated impact list keeps the high floor", severity: "medium", scanCtx: truncatedMediumProgram, wantErr: true},
		{name: "low stays below a medium program floor", severity: "low", scanCtx: mediumProgram, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			finding := store.SecurityFindingRecord{Severity: tc.severity, Status: store.SecurityFindingStatusConfirmed}
			got, err := securityReportBundleStatus(&finding, tc.scanCtx)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("securityReportBundleStatus() = %q, %v; want %q, error=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func completeBountySubmission() securityBountySubmission {
	return securityBountySubmission{
		Markdown:            "## Title\nFull report",
		ImpactClause:        "Permanent freezing of funds",
		RootCause:           "withdraw() clears the escrow index before transferring",
		MaxAchievableImpact: "every escrowed deposit becomes unrecoverable",
		AttackPath:          "1. attacker calls deposit 2. attacker calls withdraw twice",
		Feasibility:         "no privileges, ~0.01 ETH of gas, no capital at risk",
		FundsAtRisk:         "4,102 ETH at block 21,000,000",
		Remediation:         "transfer before clearing the index",
		PriorArt:            "searched repo issues/PRs, OSV and GHSA on 2026-08-14: no match",
		SeveritySystem:      "immunefi-v2.3",
	}
}

func bountyScanContextWithProgram() SecurityScanContext {
	return SecurityScanContext{
		ScanName:       "bounty",
		RunName:        "report-run",
		ExecutionID:    "exec-3",
		SeveritySystem: "immunefi-v2.3",
		InScopeImpacts: []SecurityProgramImpactClause{
			{Level: "critical", Impact: "Permanent freezing of funds"},
			{Level: "high", Impact: "Theft of unclaimed yield"},
		},
	}
}

func TestValidateBountySubmissionClaim(t *testing.T) {
	t.Parallel()
	scanCtx := bountyScanContextWithProgram()

	if problems := validateBountySubmissionClaim(completeBountySubmission(), scanCtx); len(problems) != 0 {
		t.Fatalf("expected a complete submission to pass, got %v", problems)
	}

	cases := []struct {
		name   string
		want   string
		mutate func(*securityBountySubmission)
	}{
		{"missing impact clause", "impact_clause is required", func(s *securityBountySubmission) { s.ImpactClause = "" }},
		{"missing root cause", "root_cause is required", func(s *securityBountySubmission) { s.RootCause = " " }},
		{"missing max achievable impact", "max_achievable_impact is required", func(s *securityBountySubmission) { s.MaxAchievableImpact = "" }},
		{"missing attack path", "attack_path is required", func(s *securityBountySubmission) { s.AttackPath = "" }},
		{"missing feasibility", "feasibility is required", func(s *securityBountySubmission) { s.Feasibility = "" }},
		{"missing remediation", "remediation is required", func(s *securityBountySubmission) { s.Remediation = "" }},
		{"missing prior art", "prior_art is required", func(s *securityBountySubmission) { s.PriorArt = "" }},
		{"missing funds at risk", "funds_at_risk is required", func(s *securityBountySubmission) { s.FundsAtRisk = "" }},
		{
			"invented impact clause",
			"not one of the program's published in-scope impacts",
			func(s *securityBountySubmission) { s.ImpactClause = "Loss of protocol dignity" },
		},
		{
			"translated severity system",
			"severity is never translated between systems",
			func(s *securityBountySubmission) { s.SeveritySystem = "sherlock" },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			submission := completeBountySubmission()
			testCase.mutate(&submission)
			problems := validateBountySubmissionClaim(submission, scanCtx)
			joined := strings.Join(problems, "; ")
			if !strings.Contains(joined, testCase.want) {
				t.Fatalf("problems = %q, want it to mention %q", joined, testCase.want)
			}
		})
	}

	// A program that never published an impact list cannot have its clauses
	// checked; the claim must not be rejected for failing an impossible test.
	untyped := SecurityScanContext{ScanName: "bounty", RunName: "report-run"}
	submission := completeBountySubmission()
	submission.ImpactClause = "Some impact the operator has not transcribed"
	submission.SeveritySystem = ""
	if problems := validateBountySubmissionClaim(submission, untyped); len(problems) != 0 {
		t.Fatalf("expected no problems without a transcribed impact list, got %v", problems)
	}
}

func TestSecurityReportBundleCarriesTheStructuredClaim(t *testing.T) {
	t.Parallel()
	finding := &store.SecurityFindingRecord{
		ID: uuid.MustParse("00000000-0000-0000-0000-000000000044"), Fingerprint: "fp-claim",
		Repository: "https://example.invalid/repo", Revision: "abc789", ScanName: "bounty",
		Status: store.SecurityFindingStatusConfirmed,
	}
	bundle, err := buildSecurityReportBundle(finding, bountyScanContextWithProgram(), nil, nil, completeBountySubmission(), "rank 1 of budget 2", "builder", "validator")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
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
	if _, ok := files["claim.json"]; !ok {
		t.Fatalf("bundle is missing claim.json, got %v", slices.Sorted(maps.Keys(files)))
	}
	var manifest securityBundleManifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SeveritySystem != "immunefi-v2.3" {
		t.Errorf("manifest severity system = %q, want immunefi-v2.3", manifest.SeveritySystem)
	}
	if manifest.ImpactClause != "Permanent freezing of funds" {
		t.Errorf("manifest impact clause = %q", manifest.ImpactClause)
	}
	if manifest.ProgramLevel != "critical" {
		t.Errorf("manifest program severity level = %q, want critical", manifest.ProgramLevel)
	}
	if manifest.BudgetState != "rank 1 of budget 2" {
		t.Errorf("manifest budget state = %q", manifest.BudgetState)
	}
	if manifest.FilesSHA256["claim.json"] == "" {
		t.Error("claim.json is not covered by the manifest hashes")
	}
}

func TestParseSecurityProgramImpactsAndBudget(t *testing.T) {
	t.Parallel()
	impacts := parseSecurityProgramImpacts("critical\tPermanent freezing of funds\n\nhigh\tTheft of unclaimed yield\nmalformed line\n\tblank level\n")
	if len(impacts) != 2 {
		t.Fatalf("parsed %d impacts, want 2: %+v", len(impacts), impacts)
	}
	if impacts[0].Level != "critical" || impacts[0].Impact != "Permanent freezing of funds" {
		t.Errorf("unexpected first impact: %+v", impacts[0])
	}
	for _, testCase := range []struct {
		value string
		want  int32
	}{{"3", 3}, {"", 0}, {"-2", 0}, {"not a number", 0}} {
		if got := parseSecuritySubmissionBudget(testCase.value); got != testCase.want {
			t.Errorf("parseSecuritySubmissionBudget(%q) = %d, want %d", testCase.value, got, testCase.want)
		}
	}
}

func TestTruncatedImpactListIsNotAnAllowlist(t *testing.T) {
	t.Parallel()
	scanCtx := bountyScanContextWithProgram()
	scanCtx.ImpactsTruncated = true
	submission := completeBountySubmission()
	submission.ImpactClause = "An impact that fell past the encoding bound"
	// A partial list cannot prove a clause is absent, so it must not reject
	// one; it may still resolve the level of a clause it does carry.
	if problems := validateBountySubmissionClaim(submission, scanCtx); len(problems) != 0 {
		t.Fatalf("truncated impact list rejected an unlisted clause: %v", problems)
	}
	scanCtx.ImpactsTruncated = false
	if problems := validateBountySubmissionClaim(submission, scanCtx); len(problems) == 0 {
		t.Fatal("a complete impact list must still reject an unpublished clause")
	}
}

func TestOutranksForSubmissionIsTotal(t *testing.T) {
	t.Parallel()
	high := store.SecurityFindingRecord{Score: 9, Fingerprint: "bbbb"}
	low := store.SecurityFindingRecord{Score: 4, Fingerprint: "aaaa"}
	if !outranksForSubmission(high, low) || outranksForSubmission(low, high) {
		t.Fatal("score must decide when it differs")
	}
	tieA := store.SecurityFindingRecord{Score: 7, Fingerprint: "aaaa"}
	tieB := store.SecurityFindingRecord{Score: 7, Fingerprint: "bbbb"}
	// Without a tie-break every member of a tie would claim the same rank and
	// a budget of one would package all of them.
	if !outranksForSubmission(tieA, tieB) {
		t.Fatal("ties must be broken deterministically")
	}
	if outranksForSubmission(tieB, tieA) {
		t.Fatal("the tie-break must be antisymmetric")
	}
	if outranksForSubmission(tieA, tieA) {
		t.Fatal("a finding must not outrank itself")
	}
}

func confirmedPoCValidation() securityPoCValidation {
	return securityPoCValidation{
		Confirmed:             true,
		CandidateSHA256:       "abc",
		Command:               "forge test --match-test testExploit",
		ObservedOutput:        "[FAIL] testExploit",
		Reason:                "the escrow index clears before the transfer",
		ReproducibilityClass:  "deterministic",
		TargetCodeExecuted:    true,
		NegativeControlRan:    true,
		NegativeControlPassed: true,
		OracleCanFail:         true,
		OracleEvidence:        "mutation: removed the balance check, assertion failed as expected",
	}
}

func TestValidateSecurityPoCEvidence(t *testing.T) {
	t.Parallel()

	if problems := validateSecurityPoCEvidence(confirmedPoCValidation()); len(problems) != 0 {
		t.Fatalf("expected complete evidence to pass, got %v", problems)
	}

	cases := []struct {
		name   string
		want   string
		mutate func(*securityPoCValidation)
	}{
		{"unknown class", "reproducibility_class must be one of", func(v *securityPoCValidation) { v.ReproducibilityClass = "vibes" }},
		{"missing class", "reproducibility_class is required", func(v *securityPoCValidation) { v.ReproducibilityClass = "" }},
		{"mock instead of target", "never a target-code reproduction", func(v *securityPoCValidation) { v.TargetCodeExecuted = false }},
		{"no control", "without a control", func(v *securityPoCValidation) { v.NegativeControlRan = false }},
		{
			"control triggered too",
			"attributed nothing to the defect",
			func(v *securityPoCValidation) { v.NegativeControlPassed = false },
		},
		{
			"negative successes",
			"successes cannot be negative",
			func(v *securityPoCValidation) {
				v.ReproducibilityClass = "statistical"
				v.Attempts, v.Successes, v.StoppingRule = 10, -1, "1000 trials"
			},
		},
		{"oracle never shown to fail", "oracle_can_fail requires oracle_evidence", func(v *securityPoCValidation) { v.OracleCanFail = false }},
		{"oracle evidence missing", "oracle_can_fail requires oracle_evidence", func(v *securityPoCValidation) { v.OracleEvidence = " " }},
		{
			"race without trials",
			"must report its attempts",
			func(v *securityPoCValidation) { v.ReproducibilityClass = "schedule_or_environment_dependent" },
		},
		{
			"more successes than attempts",
			"successes cannot exceed attempts",
			func(v *securityPoCValidation) {
				v.ReproducibilityClass = "statistical"
				v.Attempts, v.Successes, v.StoppingRule = 10, 11, "1000 trials"
			},
		},
		{
			"race without a stopping rule",
			"stopping_rule is required",
			func(v *securityPoCValidation) {
				v.ReproducibilityClass = "schedule_or_environment_dependent"
				v.Attempts, v.Successes = 100, 3
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			validation := confirmedPoCValidation()
			testCase.mutate(&validation)
			problems := validateSecurityPoCEvidence(validation)
			if !strings.Contains(strings.Join(problems, "; "), testCase.want) {
				t.Fatalf("problems = %v, want it to mention %q", problems, testCase.want)
			}
		})
	}

	// A schedule-dependent confirmation with honest trials is acceptable: a
	// race that fires 3 times in 100 is still a race.
	race := confirmedPoCValidation()
	race.ReproducibilityClass = "schedule_or_environment_dependent"
	race.Attempts, race.Successes, race.StoppingRule = 100, 3, "stopped after 100 trials with a stable trigger rate"
	if problems := validateSecurityPoCEvidence(race); len(problems) != 0 {
		t.Fatalf("a documented race reproduction was rejected: %v", problems)
	}

	// A disproof does not have to prove its own oracle; it has to name the
	// check that stops the attack, which `reason` carries.
	disproof := securityPoCValidation{
		Confirmed: false, CandidateSHA256: "abc", Command: "forge test", ObservedOutput: "[PASS]",
		Reason: "the guard at line 44 rejects the call", ReproducibilityClass: "deterministic",
	}
	if problems := validateSecurityPoCEvidence(disproof); len(problems) != 0 {
		t.Fatalf("a disproof was rejected: %v", problems)
	}
}
