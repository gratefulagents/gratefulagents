package securitytoolpacks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/pelletier/go-toml/v2"
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
	maxRustCrashArtifacts   = 32
	maxRustCrashBytes       = 1 << 20
	maxRustCorpusArtifacts  = 64
	maxRustTotalArtifacts   = 64
	maxRustCorpusBytes      = 1 << 20
	maxRustCorpusEntryBytes = 64 << 10
	// rustFuzzArtifactDir is where cargo-fuzz writes crashing inputs.
	rustFuzzArtifactDir = "artifacts"
	rustFuzzDirName     = "fuzz"
)

// RustFuzzCampaignMetadataPath is reserved for control-plane-authored corpus
// provenance. Repositories may not supply this file themselves.
const RustFuzzCampaignMetadataPath = ".gratefulagents/rust-fuzz-campaign.json"

const rustFuzzCampaignMetadataVersion = "v1"

type rustFuzzCampaignMetadata struct {
	SchemaVersion  string `json:"schema_version"`
	RestoredInputs int    `json:"restored_inputs"`
}

func EncodeRustFuzzCampaignMetadata(restoredInputs int) ([]byte, error) {
	if restoredInputs < 0 {
		return nil, fmt.Errorf("restored input count must not be negative")
	}
	return json.Marshal(rustFuzzCampaignMetadata{SchemaVersion: rustFuzzCampaignMetadataVersion, RestoredInputs: restoredInputs})
}

func readRustFuzzCampaignMetadata(root string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(RustFuzzCampaignMetadataPath)))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var metadata rustFuzzCampaignMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return 0, fmt.Errorf("decode Rust fuzz campaign metadata: %w", err)
	}
	if metadata.SchemaVersion != rustFuzzCampaignMetadataVersion || metadata.RestoredInputs < 0 {
		return 0, fmt.Errorf("invalid Rust fuzz campaign metadata")
	}
	return metadata.RestoredInputs, nil
}

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

	// rustFuzzCrashExitCode is passed to libFuzzer explicitly rather than
	// relying on its default, so the exit-code contract in the registry and
	// the value the fuzzer actually uses cannot drift apart.
	rustFuzzCrashExitCode = "77"
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
	FuzzTarget       string          `json:"fuzz_target"`
	MaxTotalTime     string          `json:"max_total_time"`
	Seed             string          `json:"seed,omitempty"`
	ExitCode         int             `json:"exit_code"`
	Crashes          []rustFuzzCrash `json:"crashes"`
	ReplayCommand    string          `json:"replay_command"`
	Workers          int             `json:"workers"`
	TraceCompares    bool            `json:"trace_compares"`
	HarnessDigest    string          `json:"harness_digest,omitempty"`
	ManifestDigest   string          `json:"manifest_digest,omitempty"`
	WallTime         string          `json:"wall_time,omitempty"`
	CorpusInputs     int             `json:"corpus_inputs"`
	CorpusRestored   int             `json:"corpus_restored"`
	CorpusOutputs    int             `json:"corpus_outputs"`
	CorpusNew        int             `json:"corpus_new"`
	CorpusRetained   int             `json:"corpus_retained"`
	CorpusProvenance string          `json:"corpus_provenance"`
	CorpusError      string          `json:"corpus_error,omitempty"`
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
	workers, err := strconv.Atoi(cfg.Arguments["workers"])
	if err != nil || workers < 1 || workers > 2 {
		return fmt.Errorf("argument %q must be between 1 and 2", "workers")
	}
	if _, _, err := rustFuzzHarnessPath(cfg.Target.Locator, cfg.Arguments["fuzz_target"]); err != nil {
		return err
	}
	return nil
}

// rustFuzzHarnessPath resolves the selected Cargo bin instead of assuming its
// source file has the same name. cargo-fuzz permits an explicit [[bin]] path;
// whichever form is used must remain inside the staged fuzz project.
func rustFuzzHarnessPath(root, target string) (string, string, error) {
	fuzzDir := filepath.Join(root, rustFuzzDirName)
	manifestPath := filepath.Join(fuzzDir, "Cargo.toml")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || !manifestInfo.Mode().IsRegular() {
		return "", "", fmt.Errorf("fuzz/Cargo.toml must be a regular, non-symlink file")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", "", fmt.Errorf("read fuzz/Cargo.toml: %w", err)
	}
	if len(raw) > 1<<20 {
		return "", "", fmt.Errorf("fuzz/Cargo.toml exceeds the 1 MiB validation limit")
	}
	var manifest struct {
		Bins []struct {
			Name string `toml:"name"`
			Path string `toml:"path"`
		} `toml:"bin"`
	}
	if err := toml.Unmarshal(raw, &manifest); err != nil {
		return "", "", fmt.Errorf("parse fuzz/Cargo.toml: %w", err)
	}
	matches := 0
	for _, candidate := range manifest.Bins {
		if candidate.Name == target {
			matches++
		}
	}
	if matches != 1 {
		return "", "", fmt.Errorf("cargo-fuzz target %q must be declared exactly once", target)
	}
	for _, candidate := range manifest.Bins {
		if candidate.Name != target {
			continue
		}
		relative := candidate.Path
		if relative == "" {
			return "", "", fmt.Errorf("cargo-fuzz target %q must declare an explicit harness path", target)
		}
		harness := filepath.Clean(filepath.Join(fuzzDir, filepath.FromSlash(relative)))
		rel, relErr := filepath.Rel(fuzzDir, harness)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return "", "", fmt.Errorf("cargo-fuzz target %q resolves outside fuzz/", target)
		}
		resolved, resolveErr := filepath.EvalSymlinks(harness)
		resolvedRel, resolvedRelErr := filepath.Rel(fuzzDir, resolved)
		if resolveErr != nil || resolvedRelErr != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
			return "", "", fmt.Errorf("cargo-fuzz target %q harness escapes fuzz/ through a symlink", target)
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil || !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("cargo-fuzz target %q has no regular harness at %s", target, filepath.ToSlash(relative))
		}
		return resolved, manifestPath, nil
	}
	return "", "", fmt.Errorf("cargo-fuzz target %q is not declared as a fuzz/Cargo.toml [[bin]]", target)
}

func rustFuzzWorkers(cfg RunConfig) int {
	workers, _ := strconv.Atoi(cfg.Arguments["workers"])
	if workers < 1 {
		return 1
	}
	return workers
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
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil, infoErr
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("cargo-fuzz crash artifact %q is not a regular file", entry.Name())
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
		Workers: rustFuzzWorkers(cfg), TraceCompares: true,
		ReplayCommand: fmt.Sprintf("cargo +%s fuzz run %s fuzz/artifacts/%s/<crash-input>", rustFuzzNightly, target, target),
		ConsoleTail:   boundedConsoleTail(console),
	}
	harnessPath, manifestPath, pathErr := rustFuzzHarnessPath(root, target)
	if pathErr != nil {
		return report, nil, pathErr
	}
	if data, err := os.ReadFile(harnessPath); err == nil {
		report.HarnessDigest = sha256Digest(data)
	}
	if data, err := os.ReadFile(manifestPath); err == nil {
		report.ManifestDigest = sha256Digest(data)
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
		remaining := maxRustCrashBytes - total
		data, readErr := readRustFuzzRegularFile(filepath.Dir(path), filepath.Base(path), remaining)
		if readErr != nil {
			return report, nil, readErr
		}
		total += len(data)
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

func readRustFuzzRegularFile(directory, name string, limit int) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("artifact byte budget exhausted")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > int64(limit) {
		return nil, fmt.Errorf("artifact %q is not a bounded regular file", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("artifact %q exceeds its byte budget", name)
	}
	return data, nil
}

func rustFuzzCorpusPaths(root, target string) (map[string]bool, error) {
	directory := filepath.Join(root, rustFuzzDirName, "corpus", target)
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	paths := make(map[string]bool, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil, infoErr
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("cargo-fuzz corpus entry %q is not a regular file", entry.Name())
		}
		paths[filepath.Join(directory, entry.Name())] = true
	}
	return paths, nil
}

func collectRustFuzzCorpus(root, target string, baseline map[string]bool) ([]Artifact, map[string]bool, int, error) {
	after, err := rustFuzzCorpusPaths(root, target)
	if err != nil {
		return nil, nil, 0, err
	}
	paths := make([]string, 0, len(after))
	for path := range after {
		if !baseline[path] {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	discovered := len(paths)
	artifacts := make([]Artifact, 0, min(len(paths), maxRustCorpusArtifacts))
	total := 0
	for _, path := range paths {
		if len(artifacts) >= maxRustCorpusArtifacts {
			break
		}
		remaining := min(maxRustCorpusEntryBytes, maxRustCorpusBytes-total)
		if remaining <= 0 {
			break
		}
		data, readErr := readRustFuzzRegularFile(filepath.Dir(path), filepath.Base(path), remaining)
		if readErr != nil {
			// Oversized generated inputs are not evidence failures. They are
			// simply ineligible for the bounded durable corpus.
			continue
		}
		total += len(data)
		digest := sha256Digest(data)
		name := strings.TrimPrefix(digest, "sha256:")[:32]
		artifacts = append(artifacts, Artifact{Name: "cargo-fuzz-corpus/" + name, MediaType: "application/octet-stream", Digest: digest, Size: len(data), Data: data})
	}
	return artifacts, after, discovered, nil
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
			Extra: map[string]string{
				"crash_input": crash.Name, "crash_digest": crash.Digest, "max_total_time": report.MaxTotalTime,
				"seed": report.Seed, "workers": strconv.Itoa(report.Workers), "trace_compares": strconv.FormatBool(report.TraceCompares),
				"harness_digest": report.HarnessDigest, "manifest_digest": report.ManifestDigest, "wall_time": report.WallTime,
			},
		})})
	}
	return out, nil
}

// rustFuzzBoundedScope states what a clean campaign was bounded by: which
// upstream harness ran, for how long, and against what corpus.
func rustFuzzBoundedScope(cfg RunConfig, harness string, wallTime time.Duration, corpusInputs, restored, corpusOutputs, newInputs int) *BoundedScope {
	campaign, _ := ParseFuzzCampaign(cfg.Arguments["max_total_time"])
	provenance := "cold"
	if restored > 0 {
		provenance = "restored"
	}
	committed := max(0, corpusInputs-restored)
	return &BoundedScope{
		Harness: harness,
		Corpus:  fmt.Sprintf("inputs in=%d (committed=%d, restored=%d), inputs out=%d, new inputs=%d, provenance=%s", corpusInputs, committed, restored, corpusOutputs, newInputs, provenance),
		Bounds:  fmt.Sprintf("max_total_time=%s, wall_time=%s, workers=%d, trace_compares=true", campaign, wallTime.Round(time.Millisecond), rustFuzzWorkers(cfg)),
	}
}

// rustFuzzCorpusSize counts the inputs the campaign started from.
func rustFuzzCorpusSize(root, target string) int {
	paths, _ := rustFuzzCorpusPaths(root, target)
	return len(paths)
}
