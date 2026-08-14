package securitytoolpacks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func rustFuzzConfig(root string, arguments map[string]string) RunConfig {
	seed := int64(7)
	return RunConfig{
		Tool: "cargo-fuzz",
		Target: Target{
			Type: "rust_fuzz_project", Locator: root, Revision: "fixture-v1",
			Digest: sha256Digest([]byte("rust-fuzz")), MediaType: "application/vnd.gratefulagents.rust-fuzz-project.v1+directory",
		},
		Arguments: arguments,
		Seed:      &seed,
	}
}

func stageRustFuzzProject(t *testing.T, target string, crashes map[string]string, corpus []string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "fuzz", "fuzz_targets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fuzz", "fuzz_targets", target+".rs"), []byte("// upstream harness"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(crashes) > 0 {
		directory := filepath.Join(root, "fuzz", "artifacts", target)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range crashes {
			if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(corpus) > 0 {
		directory := filepath.Join(root, "fuzz", "corpus", target)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range corpus {
			if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// The pack must run the maintainers' own harness: nothing in argv may name a
// harness we wrote, a path we chose, or a fuzzer flag a model supplied.
func TestCargoFuzzRunsTheProjectsOwnTargetsUnderTypedArguments(t *testing.T) {
	manifest := DefaultManifest("sha256:"+strings.Repeat("a", 64), nil)
	index := slices.IndexFunc(manifest.Tools, func(tool Tool) bool { return tool.Name == "cargo-fuzz" })
	if index < 0 {
		t.Fatal("cargo-fuzz is missing from the registry")
	}
	tool := manifest.Tools[index]
	if !tool.Enabled {
		t.Fatalf("cargo-fuzz is not executable: %s", tool.DisabledReason)
	}
	if !slices.Contains(tool.TargetTypes, "rust_fuzz_project") {
		t.Fatalf("cargo-fuzz target types = %v", tool.TargetTypes)
	}
	joined := strings.Join(tool.Invocation, " ")
	for _, forbidden := range []string{"http://", "https://", "--", "-fork="} {
		if forbidden == "--" {
			continue
		}
		if strings.Contains(joined, forbidden) {
			t.Errorf("argv carries %q: %s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "fuzz_targets") && !strings.Contains(joined, "--fuzz-dir") {
		t.Errorf("argv does not point at the project's own fuzz directory: %s", joined)
	}
	for _, required := range []string{"fuzz_target", "max_total_time"} {
		if !slices.ContainsFunc(tool.Arguments, func(a Argument) bool { return a.Name == required }) {
			t.Errorf("cargo-fuzz does not declare %s", required)
		}
	}
}

func TestCargoFuzzRejectsMalformedTargetsAndCampaigns(t *testing.T) {
	registry, err := NewRegistry(DefaultManifest("sha256:"+strings.Repeat("b", 64), nil))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	root := stageRustFuzzProject(t, "parse_block", nil, nil)
	for name, arguments := range map[string]map[string]string{
		"empty target":        {"fuzz_target": ""},
		"flag payload":        {"fuzz_target": "parse -runs=1"},
		"path traversal":      {"fuzz_target": "../../etc/passwd"},
		"campaign too short":  {"fuzz_target": "parse_block", "max_total_time": "1s"},
		"campaign too long":   {"fuzz_target": "parse_block", "max_total_time": "24h"},
		"campaign not a time": {"fuzz_target": "parse_block", "max_total_time": "soon"},
	} {
		if _, _, err := registry.BuildInvocation(rustFuzzConfig(root, arguments)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	invocation, _, err := registry.BuildInvocation(rustFuzzConfig(root, map[string]string{"fuzz_target": "parse_block", "max_total_time": "3m"}))
	if err != nil {
		t.Fatalf("valid cargo-fuzz request rejected: %v", err)
	}
	if !slices.Contains(invocation.Argv, "-max_total_time=180") {
		t.Fatalf("campaign length did not reach libFuzzer in seconds: %v", invocation.Argv)
	}
	// The deadline has to cover the campaign plus the Rust build, or a long
	// campaign is killed and reported as a timeout it never had.
	if invocation.Budgets.Timeout < 3*time.Minute+rustFuzzBuildAllowance {
		t.Fatalf("budget %s does not cover a 3m campaign plus its build", invocation.Budgets.Timeout)
	}
}

// A crash is evidence because it is a file that replays, not because libFuzzer
// printed a banner.
func TestCargoFuzzReportsCrashesAsReproducibleArtifacts(t *testing.T) {
	root := stageRustFuzzProject(t, "parse_block", map[string]string{"crash-abc": "bad input"}, []string{"seed-1", "seed-2"})
	cfg := rustFuzzConfig(root, map[string]string{"fuzz_target": "parse_block", "max_total_time": "2m"})

	report, artifacts, err := collectRustFuzzRun(root, map[string]bool{}, cfg, 1, []byte("==1==ERROR: libFuzzer: deadly signal"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(report.Crashes) != 1 || report.Crashes[0].Name != "crash-abc" {
		t.Fatalf("crash was not recorded: %+v", report.Crashes)
	}
	if len(artifacts) != 1 || string(artifacts[0].Data) != "bad input" {
		t.Fatalf("crash artifact does not carry the reproducing input: %+v", artifacts)
	}
	document, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	records, err := rustFuzzAdapter{}.Normalize(Tool{Name: "cargo-fuzz", Version: "0.13.2"}, cfg.Target, document, Redactor{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := ruleIDs(records); !slices.Contains(got, "cargo-fuzz-crash") {
		t.Fatalf("crash was not reported: %v", got)
	}
	if !strings.Contains(records[0].Record.RawEvidence, "crash-abc") {
		t.Fatalf("evidence does not name the replay input: %q", records[0].Record.RawEvidence)
	}
}

// A crash carried in from an earlier campaign is not this campaign's finding.
func TestCargoFuzzIgnoresPreexistingCrashArtifacts(t *testing.T) {
	root := stageRustFuzzProject(t, "parse_block", map[string]string{"crash-old": "already known"}, nil)
	cfg := rustFuzzConfig(root, map[string]string{"fuzz_target": "parse_block", "max_total_time": "2m"})
	baseline := map[string]bool{filepath.Join(root, "fuzz", "artifacts", "parse_block", "crash-old"): true}

	report, artifacts, err := collectRustFuzzRun(root, baseline, cfg, 0, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(report.Crashes) != 0 || len(artifacts) != 0 {
		t.Fatalf("a pre-existing crash was re-reported: %+v", report.Crashes)
	}
	document, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	records, err := rustFuzzAdapter{}.Normalize(Tool{Name: "cargo-fuzz"}, cfg.Target, document, Redactor{})
	if err != nil || len(records) != 1 || !records[0].Examined {
		t.Fatalf("a clean campaign produced %v (%v)", records, err)
	}
}

// A clean campaign is a bounded negative result and has to say what bounded it.
func TestCargoFuzzCleanCampaignCarriesItsBounds(t *testing.T) {
	root := stageRustFuzzProject(t, "decode_header", nil, []string{"a", "b", "c"})
	cfg := rustFuzzConfig(root, map[string]string{"fuzz_target": "decode_header", "max_total_time": "5m"})

	bounded := rustFuzzBoundedScope(cfg, rustFuzzCorpusSize(root, "decode_header"))
	if bounded == nil || !strings.Contains(bounded.Bounds, "5m") {
		t.Fatalf("bounds do not state the campaign length: %+v", bounded)
	}
	if !strings.Contains(bounded.Harness, "fuzz/fuzz_targets/decode_header") {
		t.Fatalf("bounds do not name the upstream harness: %+v", bounded)
	}
	if !strings.Contains(bounded.Corpus, "inputs=3") {
		t.Fatalf("bounds do not state the corpus size: %+v", bounded)
	}
}
