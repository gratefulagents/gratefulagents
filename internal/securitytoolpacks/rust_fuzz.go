package securitytoolpacks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/security"
)

// Rust is the largest client and program surface we can name as in scope —
// reth, lighthouse, zksync-era, the Solana programs — and until this pack it
// had no executable path at all.
//
// Two decisions define it:
//
//   - it runs the project's OWN fuzz/fuzz_targets. A harness we wrote would be
//     a model-authored artifact in the accept path, which is exactly what this
//     epic keeps out of it; the maintainers' targets are the ones their CI and
//     their reviewers already trust.
//   - the verdict is not parsed from libFuzzer's console output. The executor
//     synthesizes a record from what the run left on disk: the crash inputs in
//     fuzz/artifacts. A crash file is a reproducible input; a stderr banner is
//     a string that changes between releases.
const (
	maxRustCrashArtifacts = 32
	maxRustCrashBytes     = 1 << 20
	// rustFuzzArtifactDir is where cargo-fuzz writes crashing inputs.
	rustFuzzArtifactDir = "artifacts"
	rustFuzzDirName     = "fuzz"
)

var rustFuzzTargetPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// The cargo-fuzz root is derived rather than pulled: the base image supplies
// the toolchain, and the build adds a dated nightly plus a pinned cargo-fuzz.
// Recording only the base digest would let two materially different fuzz
// executors claim the same identity in replay metadata, so the closure — base
// image, nightly date, cargo-fuzz version — is what gets hashed, and the image
// build writes the same value into the root's provenance marker.
const (
	rustFuzzBaseImageDigest = "sha256:c1e5f19e773b7878c3f7a805dd00a495e747acbdc76fb2337a4ebf0418896b33"
	rustFuzzNightly         = "nightly-2026-06-01"
	rustFuzzVersion         = "0.13.2"

	rustFuzzToolVersion = rustFuzzVersion + "+" + rustFuzzNightly
)

// RustFuzzClosureIdentity is the exact string the image build hashes into the
// cargo-fuzz root's provenance marker.
func RustFuzzClosureIdentity() string {
	return "docker.io/library/rust@" + rustFuzzBaseImageDigest + "+rustup:" + rustFuzzNightly + "+cargo-fuzz:" + rustFuzzVersion
}

// RustFuzzClosureDigest is the identity of the whole derived executor.
func RustFuzzClosureDigest() string {
	return sha256Digest([]byte(RustFuzzClosureIdentity()))
}

// rustFuzzCrash is one crashing input the campaign produced.
type rustFuzzCrash struct {
	// Name is the artifact file name cargo-fuzz assigned, e.g.
	// crash-<sha1>. It is the argument that replays the crash.
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}

// rustFuzzReport is the executor-built record the adapter consumes. It states
// what ran, for how long, and what it produced, so a clean result carries its
// own bounds instead of implying safety.
type rustFuzzReport struct {
	FuzzTarget    string          `json:"fuzz_target"`
	MaxTotalTime  string          `json:"max_total_time"`
	Seed          string          `json:"seed,omitempty"`
	ExitCode      int             `json:"exit_code"`
	Crashes       []rustFuzzCrash `json:"crashes"`
	ReplayCommand string          `json:"replay_command"`
	// ConsoleTail is a bounded tail of what the fuzzer printed. It is
	// evidence for a human, never the thing the verdict is derived from.
	ConsoleTail string `json:"console_tail,omitempty"`
}

// validateRustFuzzArguments keeps the target selection a single upstream fuzz
// target name rather than a flag payload.
func validateRustFuzzArguments(cfg RunConfig) error {
	if !rustFuzzTargetPattern.MatchString(cfg.Arguments["fuzz_target"]) {
		return fmt.Errorf("argument %q must name exactly one cargo-fuzz target", "fuzz_target")
	}
	if _, err := ParseFuzzCampaign(cfg.Arguments["max_total_time"]); err != nil {
		return fmt.Errorf("argument %q must be a campaign length between %s and %s", "max_total_time", minFuzzCampaign, maxFuzzCampaign)
	}
	return nil
}

// rustFuzzCrashPaths lists the crash inputs for one target. cargo-fuzz keeps
// them under <project>/fuzz/artifacts/<target>/.
func rustFuzzCrashPaths(root, target string) ([]string, error) {
	directory := filepath.Join(root, rustFuzzDirName, rustFuzzArtifactDir, target)
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		paths = append(paths, filepath.Join(directory, entry.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

// collectRustFuzzRun turns what the campaign left on disk into the report and
// the reproducible artifacts. Only inputs that did not exist before the run are
// this campaign's, so a corpus carried in from an earlier one is never
// re-reported as a fresh crash.
func collectRustFuzzRun(root string, baseline map[string]bool, cfg RunConfig, exitCode int, console []byte) (rustFuzzReport, []Artifact, error) {
	target := cfg.Arguments["fuzz_target"]
	campaign, _ := ParseFuzzCampaign(cfg.Arguments["max_total_time"])
	report := rustFuzzReport{
		FuzzTarget: target, MaxTotalTime: campaign.String(), ExitCode: exitCode,
		ReplayCommand: fmt.Sprintf("cargo +nightly fuzz run %s fuzz/artifacts/%s/<crash-input>", target, target),
		ConsoleTail:   boundedConsoleTail(console),
	}
	if cfg.Seed != nil {
		report.Seed = fmt.Sprintf("%d", *cfg.Seed)
	}
	paths, err := rustFuzzCrashPaths(root, target)
	if err != nil {
		return report, nil, err
	}
	artifacts := make([]Artifact, 0, len(paths))
	total := 0
	for _, path := range paths {
		if baseline[path] {
			continue
		}
		if len(artifacts) >= maxRustCrashArtifacts {
			return report, nil, fmt.Errorf("more than %d new cargo-fuzz crash artifacts", maxRustCrashArtifacts)
		}
		data, readErr := os.ReadFile(path) // #nosec G304 -- path is rooted in the private staged target snapshot.
		if readErr != nil {
			return report, nil, readErr
		}
		total += len(data)
		if total > maxRustCrashBytes {
			return report, nil, fmt.Errorf("cargo-fuzz crash artifacts exceed the %d-byte budget", maxRustCrashBytes)
		}
		name := filepath.Base(path)
		report.Crashes = append(report.Crashes, rustFuzzCrash{Name: name, Digest: sha256Digest(data), Size: len(data)})
		artifacts = append(artifacts, Artifact{
			Name: "cargo-fuzz-crash/" + name, MediaType: "application/octet-stream",
			Digest: sha256Digest(data), Size: len(data), Data: data,
		})
	}
	return report, artifacts, nil
}

// boundedConsoleTail keeps the last few kilobytes of fuzzer output as human
// evidence without letting a chatty run blow the result document.
func boundedConsoleTail(console []byte) string {
	const maxTail = 4 << 10
	if len(console) <= maxTail {
		return strings.TrimSpace(string(console))
	}
	return "…" + strings.TrimSpace(string(console[len(console)-maxTail:]))
}

type rustFuzzAdapter struct{}

// Normalize reports one finding per crashing input. The crash file is the
// evidence and the replay command is recorded with it, so the reader never has
// to trust a console banner.
func (rustFuzzAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var report rustFuzzReport
	if err := json.Unmarshal(native, &report); err != nil {
		return nil, fmt.Errorf("cargo-fuzz report: %w", err)
	}
	if report.FuzzTarget == "" {
		return nil, fmt.Errorf("cargo-fuzz report names no fuzz target")
	}
	asset := target.Locator + ":" + report.FuzzTarget
	if len(report.Crashes) == 0 {
		return []securityRecord{{Asset: asset, Examined: true}}, nil
	}
	out := make([]securityRecord, 0, len(report.Crashes))
	for _, crash := range report.Crashes {
		message := fmt.Sprintf("cargo-fuzz target %s crashed on input %s (%d bytes) within %s", report.FuzzTarget, crash.Name, crash.Size, report.MaxTotalTime)
		evidence := message + "\nreplay: " + strings.Replace(report.ReplayCommand, "<crash-input>", crash.Name, 1)
		if report.ConsoleTail != "" {
			evidence += "\n" + report.ConsoleTail
		}
		out = append(out, securityRecord{Asset: asset, Record: fromPipelineRecord(security.ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: "cargo-fuzz-crash", RuleName: "Rust fuzz target crashed",
			Message: r.Text(message), Severity: "high", Category: "fuzzing", FilePath: target.Locator, Symbol: report.FuzzTarget,
			RawEvidence: r.Text(evidence),
			Extra:       map[string]string{"crash_input": crash.Name, "crash_digest": crash.Digest, "max_total_time": report.MaxTotalTime, "seed": report.Seed},
		})})
	}
	return out, nil
}

// rustFuzzBoundedScope states what a clean campaign was bounded by: which
// upstream harness ran, for how long, and against what corpus.
func rustFuzzBoundedScope(cfg RunConfig, corpusInputs int) *BoundedScope {
	campaign, _ := ParseFuzzCampaign(cfg.Arguments["max_total_time"])
	return &BoundedScope{
		Harness: "fuzz/fuzz_targets/" + cfg.Arguments["fuzz_target"],
		Corpus:  fmt.Sprintf("corpus inputs=%d", corpusInputs),
		Bounds:  "max_total_time=" + campaign.String(),
	}
}

// rustFuzzCorpusSize counts the inputs the campaign started from.
func rustFuzzCorpusSize(root, target string) int {
	entries, err := os.ReadDir(filepath.Join(root, rustFuzzDirName, "corpus", target))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			count++
		}
	}
	return count
}
