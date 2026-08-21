package securitytoolpacks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGoFuzzColdCampaignCollectsCrashArtifact(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "parser", "testdata", "fuzz", "FuzzDecode", "deadbeef")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("go test fuzz v1\n[]byte(\"boom\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts, err := collectGoFuzzArtifacts(root, nil, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Name != "go-fuzz-corpus/parser/testdata/fuzz/FuzzDecode/deadbeef" || string(artifacts[0].Data) != "go test fuzz v1\n[]byte(\"boom\")\n" || artifacts[0].Digest == "" {
		t.Fatalf("unexpected artifacts: %+v", artifacts)
	}
}

func TestGoFuzzWarmCampaignCollectsOnlyNewCrashArtifact(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "testdata", "fuzz", "FuzzDecode", "old")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseline, err := goFuzzCorpusPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(root, "testdata", "fuzz", "FuzzDecode", "new")
	if err := os.WriteFile(newPath, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts, err := collectGoFuzzArtifacts(root, baseline, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || string(artifacts[0].Data) != "new" {
		t.Fatalf("unexpected artifacts: %+v", artifacts)
	}
}

func TestGoFuzzProcessReportsColdAndRestoredCrashCampaigns(t *testing.T) {
	cases := []struct {
		name       string
		restored   int
		provenance string
	}{
		{name: "cold", restored: 0, provenance: "provenance=cold"},
		{name: "restored", restored: 1, provenance: "provenance=restored"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			seedDir := filepath.Join(root, "parser", "testdata", "fuzz", "FuzzDecode")
			if err := os.MkdirAll(seedDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(seedDir, "seed"), []byte("seed"), 0o600); err != nil {
				t.Fatal(err)
			}
			metadata, err := EncodeGoFuzzCampaignMetadata(test.restored)
			if err != nil {
				t.Fatal(err)
			}
			metadataPath := filepath.Join(root, filepath.FromSlash(GoFuzzCampaignMetadataPath))
			if err := os.MkdirAll(filepath.Dir(metadataPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(metadataPath, metadata, 0o600); err != nil {
				t.Fatal(err)
			}
			digest, exists, err := DigestPath(root)
			if err != nil || !exists {
				t.Fatalf("DigestPath() = %q, %t, %v", digest, exists, err)
			}

			result := (ProcessSandbox{}).Execute(context.Background(), ExecutionRequest{
				Tool: Tool{Name: "go-fuzz-tests"},
				Invocation: Invocation{
					Argv:    []string{"/bin/sh", "-c", `/bin/mkdir -p "$1/parser/testdata/fuzz/FuzzDecode"; printf 'go test fuzz v1\n[]byte("boom")\n' > "$1/parser/testdata/fuzz/FuzzDecode/crash"`, "_", root},
					Budgets: Budgets{Timeout: time.Second, MaxOutputSize: 1 << 20},
				},
				Config: RunConfig{
					Tool:      "go-fuzz-tests",
					Target:    Target{Locator: root, Digest: digest},
					Arguments: map[string]string{"package": "./parser", "fuzz": "^FuzzDecode$", "fuzztime": "30s"},
				},
			})
			if result.Err != nil {
				t.Fatalf("Execute() error = %v", result.Err)
			}
			if len(result.Artifacts) != 1 || !strings.HasSuffix(result.Artifacts[0].Name, "/crash") {
				t.Fatalf("crash artifacts = %+v", result.Artifacts)
			}
			if result.Bounded == nil {
				t.Fatal("campaign has no bounded scope")
			}
			for _, want := range []string{"inputs in=1", "inputs out=2", "new inputs=1", test.provenance} {
				if !strings.Contains(result.Bounded.Corpus, want) {
					t.Fatalf("corpus = %q, missing %q", result.Bounded.Corpus, want)
				}
			}
			if !strings.Contains(result.Bounded.Bounds, "fuzztime=30s, wall_time=") {
				t.Fatalf("bounds = %q", result.Bounded.Bounds)
			}
		})
	}
}

func TestCollectGoFuzzArtifactsEnforcesBudget(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "testdata", "fuzz", "FuzzDecode", "deadbeef")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("too large"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := collectGoFuzzArtifacts(root, nil, 2); err == nil {
		t.Fatal("expected artifact budget error")
	}
}

func TestProcessSandboxExecutesArgvWithoutShell(t *testing.T) {
	request := ExecutionRequest{
		Invocation: Invocation{Argv: []string{"/usr/bin/printf", "%s", "target; touch /tmp/must-not-exist"}, Budgets: Budgets{Timeout: time.Second, MaxOutputSize: 1024}},
		Config:     RunConfig{Target: Target{Locator: "fixture"}},
	}
	result := (ProcessSandbox{}).Execute(context.Background(), request)
	if result.Err != nil || string(result.Output) != "target; touch /tmp/must-not-exist" {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat("/tmp/must-not-exist"); !os.IsNotExist(err) {
		t.Fatal("argument was interpreted by a shell")
	}
}

func TestProcessSandboxRejectsPATHShadowForLockedTools(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "executed")
	shadow := filepath.Join(directory, "naabu")
	if err := os.WriteFile(shadow, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+":"+os.Getenv("PATH"))
	request := ExecutionRequest{
		Tool:       Tool{Name: "naabu", ToolArtifactDigest: sha256Digest([]byte("not-the-shadow"))},
		Invocation: Invocation{Argv: []string{"naabu", "-version"}, Budgets: Budgets{Timeout: time.Second, MaxOutputSize: 1024}},
		Config:     RunConfig{Target: Target{Locator: "192.0.2.1"}},
	}
	result := (ProcessSandbox{}).Execute(context.Background(), request)
	if result.Err == nil {
		t.Fatal("expected operator-toolkit lookup failure")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("PATH-shadowed tool was executed")
	}
}

func TestProcessSandboxOutputBudgetIsNotPass(t *testing.T) {
	request := ExecutionRequest{
		Invocation: Invocation{Argv: []string{"/usr/bin/printf", "123456789"}, Budgets: Budgets{Timeout: time.Second, MaxOutputSize: 4}},
		Config:     RunConfig{Target: Target{Locator: "fixture"}},
	}
	result := (ProcessSandbox{}).Execute(context.Background(), request)
	if result.Err == nil || string(result.Output) != "1234" {
		t.Fatalf("result=%+v", result)
	}
}

func TestDigestPathIsStableAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	contract := filepath.Join(root, "src", "Vault.sol")
	if err := os.WriteFile(contract, []byte("contract Vault {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, exists, err := DigestPath(root)
	if err != nil || !exists {
		t.Fatalf("digest=%q exists=%t err=%v", first, exists, err)
	}
	second, _, _ := DigestPath(root)
	if first != second {
		t.Fatal("directory digest is not stable")
	}
	if err := os.WriteFile(contract, []byte("contract Vault { function x() external {} }"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, _, _ := DigestPath(root)
	if first == changed {
		t.Fatal("directory mutation did not change digest")
	}
	if err := os.Symlink(contract, filepath.Join(root, "link.sol")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DigestPath(root); err == nil {
		t.Fatal("expected symlink rejection")
	}
	if err := os.Remove(filepath.Join(root, "link.sol")); err != nil {
		t.Fatal(err)
	}
	digest, _, err := DigestPath(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, cleanup, err := snapshotDirectoryTarget(Target{Locator: root, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := os.WriteFile(filepath.Join(snapshot, "generated"), []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "generated")); !os.IsNotExist(err) {
		t.Fatal("tool output mutated original target")
	}
}

func TestRegularFileTargetUsesPrivateVerifiedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openapi.json")
	original := []byte(`{"openapi":"3.0.0"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, cleanup, err := snapshotDirectoryTarget(Target{Locator: path, Digest: sha256Digest(original)})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if snapshot == path {
		t.Fatal("regular target was not snapshotted")
	}
	if err := os.WriteFile(path, []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(snapshot)
	if err != nil || string(data) != string(original) {
		t.Fatalf("snapshot=%q err=%v", data, err)
	}
}

func TestWorkDirectoryQuotaIsFinalAndKernelBacked(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process limits")
	}
	root := t.TempDir()
	cmd := exec.Command("dd", "if=/dev/zero", "of="+filepath.Join(root, "oversized"), "bs=1024", "count=8", "status=none")
	cmd.Dir = root
	if err := runWithDirectoryLimit(cmd, root, 1024); err == nil {
		t.Fatal("oversized fast writer escaped quota")
	}
	if err := os.WriteFile(filepath.Join(root, "final"), make([]byte, 1025), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkDirectoryQuota(root, 1024, 4096); !errors.Is(err, errOutputTooLarge) {
		t.Fatalf("final quota err=%v", err)
	}
}

func TestWritableTargetAndWorkShareOneQuota(t *testing.T) {
	work := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "result"), make([]byte, 600), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "compiler-cache"), make([]byte, 600), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkDirectoriesQuota([]string{work, target}, 1024, 4096); !errors.Is(err, errOutputTooLarge) {
		t.Fatalf("combined writable quota err=%v", err)
	}
}

func TestZAPReportRequiresExaminedSite(t *testing.T) {
	if zapReportExaminedTarget([]byte(`{"site":[]}`), "https://example.test", []string{"https://example.test"}) {
		t.Fatal("empty ZAP report counted as examined")
	}
	if !zapReportExaminedTarget([]byte(`{"site":[{"@name":"https://example.test"}]}`), "https://example.test", []string{"https://example.test"}) {
		t.Fatal("site evidence was not recognized")
	}
	for _, report := range []string{`{"site":[{}]}`, `{"site":[null]}`, `{"site":[{"@name":"https://outside.test"}]}`} {
		if zapReportExaminedTarget([]byte(report), "https://example.test", []string{"https://example.test"}) {
			t.Fatalf("non-evidentiary report accepted: %s", report)
		}
	}
}

func TestOCIOutputCollectionRejectsLinksAndBoundsReads(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "result")); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedOCIOutput(root, "result", 1024); err == nil {
		t.Fatal("symlink output was accepted")
	}
	if err := os.Remove(filepath.Join(root, "result")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "result"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedOCIOutput(root, "result", 4); !errors.Is(err, errOutputTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func TestResultExitCodesAreDistinct(t *testing.T) {
	seen := map[int]Status{}
	for _, status := range []Status{StatusPass, StatusFindings, StatusError, StatusTimeout, StatusPartial, StatusNotApplicable} {
		code := ResultExitCode(status)
		if previous, exists := seen[code]; exists {
			t.Fatalf("%s and %s share exit code %d", previous, status, code)
		}
		seen[code] = status
	}
}

func TestBoundedNegativeResultExitsSuccessfully(t *testing.T) {
	t.Parallel()
	// A bounded clean run is a successful execution: the tool ran and found
	// nothing under its bounds. Exiting non-zero would make every clean scan
	// look like a broken one to the scripts and agents that read the code.
	if code := ResultExitCode(StatusNotFoundUnder); code != 0 {
		t.Fatalf("ResultExitCode(not_found_under) = %d, want 0", code)
	}
	if code := ResultExitCode(StatusPass); code != 0 {
		t.Fatalf("ResultExitCode(pass) = %d, want 0 for the retired spelling", code)
	}
	for _, status := range []Status{StatusFindings, StatusPartial, StatusNotApplicable, StatusTimeout, StatusError} {
		if ResultExitCode(status) == 0 {
			t.Errorf("ResultExitCode(%s) = 0, want a non-zero code", status)
		}
	}
}
