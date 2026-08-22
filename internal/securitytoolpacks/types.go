// Package securitytoolpacks implements deterministic, typed wrappers for
// security tools. It deliberately separates execution, native artifacts, and
// normalization so normal CI can replay committed output without network access.
package securitytoolpacks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/security"
)

type Domain string

const (
	DomainWeb        Domain = "web_api"
	DomainCrypto     Domain = "cryptography"
	DomainNetwork    Domain = "network_protocol"
	DomainBlockchain Domain = "blockchain_smart_contract"
)

type Status string

const (
	// StatusNotFoundUnder is the bounded negative result: this harness, with
	// this corpus, these seeds and these bounds, did not find the issue. It is
	// never evidence of safety, which is why it replaced the old "pass"
	// verdict; the accompanying BoundedScope records what was actually
	// covered.
	StatusNotFoundUnder Status = "not_found_under"
	// StatusPass is the retired verdict. It is still accepted when decoding
	// stored runs and manifests, and normalized to StatusNotFoundUnder,
	// because a clean bounded run never proved anything was safe.
	//
	// Deprecated: emit StatusNotFoundUnder instead.
	StatusPass          Status = "pass"
	StatusFindings      Status = "findings"
	StatusError         Status = "error"
	StatusTimeout       Status = "timeout"
	StatusPartial       Status = "partial"
	StatusNotApplicable Status = "not_applicable"
)

type Budgets struct {
	Timeout       time.Duration `json:"timeout"`
	CPU           int           `json:"cpu_millis"`
	Memory        int64         `json:"memory_bytes"`
	Requests      int           `json:"requests"`
	Concurrency   int           `json:"concurrency"`
	MaxOutputSize int64         `json:"max_output_bytes"`
}

type Requirements struct {
	Network   bool     `json:"network"`
	Privilege string   `json:"privilege"`
	Protocols []string `json:"protocols,omitempty"`
}

type Argument struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Flag     string   `json:"flag,omitempty"`
	// Default is the reviewed value used when a run omits an optional
	// argument. It is part of the registry, not of the request, so an
	// omitted argument still resolves to a value the catalog declares and
	// the recorded configuration always states what actually ran.
	Default string `json:"default,omitempty"`
}

type Tool struct {
	Name               string            `json:"name"`
	Enabled            bool              `json:"enabled"`
	DisabledReason     string            `json:"disabled_reason,omitempty"`
	Domain             Domain            `json:"domain"`
	Version            string            `json:"version"`
	Image              string            `json:"image"`
	ImageDigest        string            `json:"image_digest"`
	ToolArtifactDigest string            `json:"tool_artifact_digest"`
	WrapperDigest      string            `json:"wrapper_digest"`
	PlatformDigests    map[string]string `json:"platform_digests,omitempty"`
	OCIRoot            string            `json:"oci_root,omitempty"`
	OCIExecutable      string            `json:"oci_executable,omitempty"`
	OCIOutputPath      string            `json:"oci_output_path,omitempty"`
	OCIPath            string            `json:"oci_path,omitempty"`
	OCIWritableTarget  bool              `json:"oci_writable_target,omitempty"`
	Invocation         []string          `json:"invocation"`
	Arguments          []Argument        `json:"arguments,omitempty"`
	TargetTypes        []string          `json:"target_types"`
	ArtifactTypes      []string          `json:"artifact_types,omitempty"`
	KnowledgeDigests   map[string]string `json:"knowledge_digests,omitempty"`
	Requirements       Requirements      `json:"requirements"`
	Budgets            Budgets           `json:"budgets"`
	SeedSupported      bool              `json:"seed_supported,omitempty"`
	ExitCodes          map[int]Status    `json:"exit_codes"`
	OutputMediaType    string            `json:"output_media_type"`
	Adapter            string            `json:"adapter"`
	RedactionRules     []string          `json:"redaction_rules"`
	Idempotent         bool              `json:"idempotent"`
	Resettable         bool              `json:"resettable,omitempty"`
}

type Manifest struct {
	SchemaVersion string `json:"schema_version"`
	Tools         []Tool `json:"tools"`
}

type Target struct {
	Type      string `json:"type"`
	Locator   string `json:"locator"`
	Revision  string `json:"revision"`
	Digest    string `json:"digest"`
	MediaType string `json:"media_type,omitempty"`
}

type RunConfig struct {
	Tool      string            `json:"tool"`
	Target    Target            `json:"target"`
	Arguments map[string]string `json:"arguments,omitempty"`
	Seed      *int64            `json:"seed,omitempty"`
	Scope     []string          `json:"scope,omitempty"`
	Sensitive []string          `json:"sensitive_fields,omitempty"`
}

type Coverage struct {
	Examined  []string `json:"examined"`
	Skipped   []string `json:"skipped"`
	Uncovered []string `json:"uncovered"`
}

type Artifact struct {
	Name        string `json:"name"`
	StorageName string `json:"storage_name,omitempty"`
	MediaType   string `json:"media_type"`
	Digest      string `json:"digest"`
	Size        int    `json:"size"`
	Data        []byte `json:"-"`
}

type Replay struct {
	Target             Target            `json:"target"`
	Tool               string            `json:"tool"`
	ToolVersion        string            `json:"tool_version"`
	ImageDigest        string            `json:"image_digest"`
	ToolArtifactDigest string            `json:"tool_artifact_digest"`
	WrapperDigest      string            `json:"wrapper_digest"`
	PlatformDigest     string            `json:"platform_digest,omitempty"`
	SandboxDigest      string            `json:"sandbox_digest,omitempty"`
	Knowledge          map[string]string `json:"knowledge_digests,omitempty"`
	Configuration      json.RawMessage   `json:"configuration"`
	ConfigurationID    string            `json:"configuration_digest"`
	Seed               *int64            `json:"seed,omitempty"`
	Environment        map[string]string `json:"environment,omitempty"`
	InputDigests       []string          `json:"input_digests"`
}

type Result struct {
	Status    Status                   `json:"status"`
	Findings  []security.ScannerRecord `json:"findings"`
	Artifacts []Artifact               `json:"artifacts"`
	Coverage  Coverage                 `json:"coverage"`
	Errors    []string                 `json:"errors"`
	Replay    Replay                   `json:"replay"`
	Stages    []string                 `json:"stages"`
	// Reproducibility records how this run can be reproduced. Real chain bugs
	// involve ordering, reorgs, retries and scheduler races, so demanding
	// byte-identical replay would reject a genuine race and accept a flake
	// after one lucky rerun.
	Reproducibility ReproducibilityClass `json:"reproducibility_class,omitempty"`
	// Trials records every attempt for a non-deterministic class, successes
	// included and excluded alike, so a trigger rate can be read rather than
	// guessed.
	Trials *TrialSummary `json:"trials,omitempty"`
	// Harness records whether the run could have failed at all.
	Harness *HarnessHealth `json:"harness_health,omitempty"`
	// Bounded records the scope a negative result is bounded by. It is
	// required to interpret StatusNotFoundUnder.
	Bounded *BoundedScope `json:"bounded_scope,omitempty"`
}

// maxCoverageSummaryBytes bounds the rendered coverage summary so it always
// fits the SecurityToolRun status field that carries it.
const maxCoverageSummaryBytes = 1024

// ReproducibilityClass states how a result can be reproduced. Confirmation
// must be reproducible under the correct class, not under a single universal
// definition of determinism.
type ReproducibilityClass string

const (
	// ReproducibilityDeterministic replays byte-identically from the record.
	ReproducibilityDeterministic ReproducibilityClass = "deterministic"
	// ReproducibilitySeeded replays from a recorded seed.
	ReproducibilitySeeded ReproducibilityClass = "seeded_replayable"
	// ReproducibilityScheduleDependent depends on scheduling, timing,
	// ordering, reorgs or environment; the causal trace is what reproduces.
	ReproducibilityScheduleDependent ReproducibilityClass = "schedule_or_environment_dependent"
	// ReproducibilityStatistical reproduces a distribution, not an outcome.
	ReproducibilityStatistical ReproducibilityClass = "statistical"
	// ReproducibilityObservational cannot be re-run; it was observed.
	ReproducibilityObservational ReproducibilityClass = "observational_only"
)

// ValidReproducibilityClass reports whether class is a known class. An empty
// class is allowed and means the run did not state one.
func ValidReproducibilityClass(class ReproducibilityClass) bool {
	switch class {
	case "", ReproducibilityDeterministic, ReproducibilitySeeded,
		ReproducibilityScheduleDependent, ReproducibilityStatistical, ReproducibilityObservational:
		return true
	}
	return false
}

// TrialSummary preserves every attempt behind a non-deterministic result.
type TrialSummary struct {
	Attempts int `json:"attempts"`
	// Successes is how many attempts reproduced the security-relevant
	// outcome. Zero attempts or zero successes is a reportable fact, not a
	// reason to omit the summary.
	Successes int `json:"successes"`
	// StoppingRule states why the campaign stopped, so a null result cannot
	// be read as exhaustive.
	StoppingRule string `json:"stopping_rule,omitempty"`
	// EquivalenceCriteria states what counted as the same outcome across
	// attempts, since byte-identical output is the wrong test here.
	EquivalenceCriteria string `json:"equivalence_criteria,omitempty"`
	// TraceDigest references the recorded scheduler, message or transaction
	// trace that carries the causal ordering.
	TraceDigest string `json:"trace_digest,omitempty"`
	Topology    string `json:"topology,omitempty"`
	ForkState   string `json:"fork_state,omitempty"`
}

// TriggerRate is the observed share of attempts that reproduced the outcome.
// It returns 0 when nothing was attempted.
func (t TrialSummary) TriggerRate() float64 {
	if t.Attempts <= 0 {
		return 0
	}
	return float64(t.Successes) / float64(t.Attempts)
}

// HarnessHealth answers the question a clean run cannot: could this run have
// failed at all? A harness that never reaches the target, never runs the
// control, or carries an assertion that cannot fail proves nothing.
type HarnessHealth struct {
	// EntryPointReached reports that the intended entry point executed.
	EntryPointReached bool `json:"entry_point_reached"`
	// TargetCodeExecuted reports that the real target code ran, as opposed to
	// a mock or a simplified model. A model must never be presented as a
	// target-code reproduction.
	TargetCodeExecuted bool `json:"target_code_executed"`
	// NegativeControlRan and NegativeControlPassed record the control: the
	// same harness against unmodified or non-attacker input.
	NegativeControlRan    bool `json:"negative_control_ran"`
	NegativeControlPassed bool `json:"negative_control_passed"`
	// OracleCanFail records that a mutation or a known calibration failure
	// made the assertion fail on purpose.
	OracleCanFail bool `json:"oracle_can_fail"`
	// MutationsKilled and MutationsTotal quantify the calibration.
	MutationsKilled int `json:"mutations_killed,omitempty"`
	MutationsTotal  int `json:"mutations_total,omitempty"`
	// CorpusRejectRate is the share of inputs the harness refused, which
	// exposes a harness that rejects almost everything it is given.
	CorpusRejectRate float64 `json:"corpus_reject_rate,omitempty"`
	// Notes records anything that qualifies the above.
	Notes string `json:"notes,omitempty"`
}

// ProvesTargetCodeExercised reports whether the health evidence supports a
// claim of target-code reproduction with a working oracle.
func (h HarnessHealth) ProvesTargetCodeExercised() bool {
	return h.EntryPointReached && h.TargetCodeExecuted && h.OracleCanFail
}

// BoundedScope is what a negative result is bounded by. Reading a clean run
// without it invites the "no findings means safe" mistake.
type BoundedScope struct {
	Harness     string   `json:"harness,omitempty"`
	Corpus      string   `json:"corpus,omitempty"`
	Seeds       []string `json:"seeds,omitempty"`
	Bounds      string   `json:"bounds,omitempty"`
	Environment string   `json:"environment,omitempty"`
	// Coverage summarizes semantic coverage - states, transitions, branches
	// or actors exercised - rather than lines.
	Coverage string `json:"coverage,omitempty"`
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func sha256Digest(data []byte) string {
	s := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(s[:])
}

func canonicalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != "security-tool-registry/v1" {
		return fmt.Errorf("unsupported schema_version %q", m.SchemaVersion)
	}
	seen := map[string]bool{}
	for i, t := range m.Tools {
		if t.Name == "" || t.Version == "" || seen[t.Name] {
			return fmt.Errorf("tools[%d]: name/version missing or duplicate", i)
		}
		seen[t.Name] = true
		if !t.Enabled {
			if strings.TrimSpace(t.DisabledReason) == "" {
				return fmt.Errorf("tool %s: disabled catalog entry requires a reason", t.Name)
			}
			continue
		}
		if t.DisabledReason != "" {
			return fmt.Errorf("tool %s: enabled tool must not have a disabled reason", t.Name)
		}
		if !digestPattern.MatchString(t.ImageDigest) {
			return fmt.Errorf("tool %s: image_digest must be immutable sha256", t.Name)
		}
		if !digestPattern.MatchString(t.ToolArtifactDigest) {
			return fmt.Errorf("tool %s: tool_artifact_digest must be immutable sha256", t.Name)
		}
		if !digestPattern.MatchString(t.WrapperDigest) {
			return fmt.Errorf("tool %s: wrapper_digest must be immutable sha256", t.Name)
		}
		if t.OCIRoot != "" {
			if filepath.Base(t.OCIRoot) != t.OCIRoot || !strings.HasPrefix(t.OCIExecutable, "/") || !strings.Contains(t.Image, "@"+t.ImageDigest) {
				return fmt.Errorf("tool %s: invalid pinned OCI root execution contract", t.Name)
			}
			for _, arch := range []string{"amd64", "arm64"} {
				if !digestPattern.MatchString(t.PlatformDigests[arch]) {
					return fmt.Errorf("tool %s: missing %s OCI manifest digest", t.Name, arch)
				}
			}
			if t.OCIPath != "" {
				for entry := range strings.SplitSeq(t.OCIPath, ":") {
					if !strings.HasPrefix(entry, "/") || filepath.Clean(entry) != entry {
						return fmt.Errorf("tool %s: invalid OCI PATH entry %q", t.Name, entry)
					}
				}
			}
		} else if t.OCIExecutable != "" || t.OCIOutputPath != "" || t.OCIPath != "" || t.OCIWritableTarget {
			return fmt.Errorf("tool %s: OCI fields require oci_root", t.Name)
		}
		for name, digest := range t.KnowledgeDigests {
			if !digestPattern.MatchString(digest) {
				return fmt.Errorf("tool %s: %s digest must be immutable sha256", t.Name, name)
			}
		}
		if slices.Contains([]string{"nuclei", "wycheproof", "rfc-nist-vectors", "suricata", "zeek"}, t.Name) && len(t.KnowledgeDigests) == 0 {
			return fmt.Errorf("tool %s: pinned knowledge bundle is required", t.Name)
		}
		if len(t.Invocation) == 0 || len(t.TargetTypes) == 0 || t.OutputMediaType == "" || t.Adapter == "" {
			return fmt.Errorf("tool %s: incomplete execution contract", t.Name)
		}
		if t.Budgets.Timeout <= 0 || t.Budgets.MaxOutputSize <= 0 || t.Budgets.Concurrency <= 0 {
			return fmt.Errorf("tool %s: invalid budgets", t.Name)
		}
		if len(t.ExitCodes) == 0 {
			return fmt.Errorf("tool %s: exit-code mapping is required", t.Name)
		}
		for code, status := range t.ExitCodes {
			if !slices.Contains([]Status{StatusPass, StatusNotFoundUnder, StatusFindings, StatusError, StatusTimeout}, status) {
				return fmt.Errorf("tool %s: exit code %d has invalid execution status %q", t.Name, code, status)
			}
		}
	}
	return nil
}

func stableRecords(records []security.ScannerRecord) {
	sort.Slice(records, func(i, j int) bool {
		a, b := records[i], records[j]
		ak := strings.Join([]string{a.Tool, a.RuleID, a.FilePath, fmt.Sprint(a.StartLine), a.Message}, "\x00")
		bk := strings.Join([]string{b.Tool, b.RuleID, b.FilePath, fmt.Sprint(b.StartLine), b.Message}, "\x00")
		if ak != bk {
			return ak < bk
		}
		aj, _ := canonicalJSON(a)
		bj, _ := canonicalJSON(b)
		return bytes.Compare(aj, bj) < 0
	})
}
