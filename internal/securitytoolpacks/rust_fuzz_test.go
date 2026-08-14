package securitytoolpacks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// The derived cargo-fuzz root is not the base image, so recording only the base
// digest would let two materially different fuzz executors — a different
// nightly, a different cargo-fuzz — claim the same identity in replay metadata.
// The registry's digest and the marker the image build writes must be the same
// hash of the same closure identity, or the root is rejected at exec time.
func TestCargoFuzzClosureDigestMatchesTheImageProvenanceMarker(t *testing.T) {
	manifest := DefaultManifest("sha256:"+strings.Repeat("c", 64), nil)
	index := slices.IndexFunc(manifest.Tools, func(tool Tool) bool { return tool.Name == "cargo-fuzz" })
	if index < 0 {
		t.Fatal("cargo-fuzz is missing from the registry")
	}
	tool := manifest.Tools[index]
	if tool.ToolArtifactDigest == rustFuzzBaseImageDigest {
		t.Fatal("cargo-fuzz records the base image digest for a derived root")
	}
	if tool.ToolArtifactDigest != RustFuzzClosureDigest() {
		t.Fatalf("registry digest %q is not the closure digest %q", tool.ToolArtifactDigest, RustFuzzClosureDigest())
	}
	if !strings.Contains(tool.Version, rustFuzzNightly) {
		t.Errorf("tool version %q does not name the pinned nightly", tool.Version)
	}

	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile.security-tools"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	if !strings.Contains(text, RustFuzzClosureDigest()+"' > /usr/local/share/ga-security/toolroots/cargo-fuzz/.ga-oci-digest") {
		t.Error("the image does not write the closure digest into the cargo-fuzz provenance marker")
	}
	if !strings.Contains(text, "rustup toolchain install "+rustFuzzNightly) {
		t.Errorf("the image does not install the pinned nightly %s", rustFuzzNightly)
	}
	if strings.Contains(text, "rustup toolchain install nightly ") {
		t.Error("the image installs a floating nightly: identical builds would ship different compilers")
	}
	if !strings.Contains(text, "cargo install cargo-fuzz --version "+rustFuzzVersion+" --locked") {
		t.Errorf("the image does not install the pinned cargo-fuzz %s", rustFuzzVersion)
	}
	if !strings.Contains(text, rustFuzzBaseImageDigest) {
		t.Error("the image base digest and the recorded closure identity disagree")
	}
}

// Evidence that could not be collected is not a clean campaign: a collection
// failure has to fail the run rather than ride out under the fuzzer's own exit
// code.
func TestCargoFuzzCollectionFailureIsNotACleanCampaign(t *testing.T) {
	root := stageRustFuzzProject(t, "parse_block", nil, nil)
	directory := filepath.Join(root, "fuzz", "artifacts", "parse_block")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range maxRustCrashArtifacts + 1 {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("crash-%03d", i)), []byte("boom"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := rustFuzzConfig(root, map[string]string{"fuzz_target": "parse_block", "max_total_time": "2m"})
	if _, _, err := collectRustFuzzRun(root, map[string]bool{}, cfg, 0, nil); err == nil {
		t.Fatal("an over-budget crash set was collected silently")
	}
}

// The image installs one dated nightly, so `cargo +nightly` would fail before
// compiling anything, and libFuzzer's crash exit code has to be a value the
// registry actually maps — otherwise a crash is downgraded to a partial run
// with an "unmapped exit code" error instead of reported as findings.
func TestCargoFuzzInvokesTheInstalledToolchainAndMapsItsCrashExit(t *testing.T) {
	manifest := DefaultManifest("sha256:"+strings.Repeat("d", 64), nil)
	index := slices.IndexFunc(manifest.Tools, func(tool Tool) bool { return tool.Name == "cargo-fuzz" })
	if index < 0 {
		t.Fatal("cargo-fuzz is missing from the registry")
	}
	tool := manifest.Tools[index]
	if !slices.Contains(tool.Invocation, "+"+rustFuzzNightly) {
		t.Fatalf("argv does not select the installed toolchain: %v", tool.Invocation)
	}
	if slices.Contains(tool.Invocation, "+nightly") {
		t.Fatal("argv selects an undated nightly the image never installs")
	}
	if !slices.Contains(tool.Invocation, "-error_exitcode="+rustFuzzCrashExitCode) {
		t.Fatalf("argv does not pin libFuzzer's crash exit code: %v", tool.Invocation)
	}
	code, err := strconv.Atoi(rustFuzzCrashExitCode)
	if err != nil {
		t.Fatal(err)
	}
	if tool.ExitCodes[code] != StatusFindings {
		t.Fatalf("exit code %d maps to %q, want findings", code, tool.ExitCodes[code])
	}

	root := stageRustFuzzProject(t, "parse_block", map[string]string{"crash-abc": "boom"}, nil)
	cfg := rustFuzzConfig(root, map[string]string{"fuzz_target": "parse_block", "max_total_time": "2m"})
	report, _, err := collectRustFuzzRun(root, map[string]bool{}, cfg, code, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	// The replay command a reader is handed must name a toolchain that exists.
	if !strings.Contains(report.ReplayCommand, "+"+rustFuzzNightly) {
		t.Fatalf("replay command does not name the installed toolchain: %q", report.ReplayCommand)
	}
}
