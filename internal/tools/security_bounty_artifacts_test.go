package tools

import (
	"archive/zip"
	"bytes"
	"io"
	"slices"
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
	bundle, err := buildSecurityReportBundle(finding, ctx, nil, nil, "## Title\nNeeds runtime validation", "", "")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
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
			got, err := securityReportBundleStatus(&finding)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("securityReportBundleStatus() = %q, %v; want %q, error=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
