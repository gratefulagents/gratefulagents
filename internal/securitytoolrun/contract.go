/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

// Package securitytoolrun holds the execution contract shared by the
// SecurityToolRun controller, the `ga-security job` entrypoint that runs
// inside the execution Job, and the agent-side tool that stages targets: the
// object-storage layout, the Job environment, and the manifest the Job writes
// back for the controller to read.
package securitytoolrun

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

const (
	// ObjectKeyRoot is the bucket prefix owning every staged target and every
	// produced artifact.
	ObjectKeyRoot      = "security-tool-runs"
	TargetObjectName   = "target.tar.gz"
	ManifestObjectName = "manifest.json"
	ResultObjectName   = "result.json"

	// ConfigFileName is the ConfigMap key and file name of the typed RunConfig.
	ConfigFileName  = "run.json"
	ConfigMountPath = "/ga/config"
	ConfigPath      = ConfigMountPath + "/" + ConfigFileName
	// WorkDir is the writable scratch directory inside the Job container; the
	// container rootfs is read-only, so it doubles as HOME.
	WorkDir = "/work"

	EnvConfig  = "GA_JOB_CONFIG"
	EnvWorkdir = "GA_JOB_WORKDIR"
	// EnvTargetKey is the object key of the staged target archive (empty for
	// network targets).
	EnvTargetKey = "GA_JOB_TARGET_KEY"
	// EnvTargetDigest is the digest the staged archive must match.
	EnvTargetDigest = "GA_JOB_TARGET_DIGEST"
	// EnvOutputPrefix is the object-key prefix the Job writes results under.
	EnvOutputPrefix = "GA_JOB_OUTPUT_PREFIX"

	// ManifestSchemaVersion is the only manifest schema the controller reads.
	ManifestSchemaVersion = "security-tool-job-manifest/v1"
)

// Prefix is the object-key prefix owning one SecurityToolRun.
func Prefix(namespace, name string) string {
	return path.Join(ObjectKeyRoot, namespace, name)
}

// TargetObjectKey is where the requester stages the target archive.
func TargetObjectKey(namespace, name string) string {
	return path.Join(Prefix(namespace, name), TargetObjectName)
}

// OutputPrefix is where the Job writes its manifest, result, and raw artifacts.
func OutputPrefix(namespace, name string) string {
	return path.Join(Prefix(namespace, name), "output")
}

// ManifestObjectKey is the manifest the controller reads on Job completion.
func ManifestObjectKey(namespace, name string) string {
	return path.Join(OutputPrefix(namespace, name), ManifestObjectName)
}

// ResultObjectKey is the normalized result document written by the Job.
func ResultObjectKey(namespace, name string) string {
	return path.Join(OutputPrefix(namespace, name), ResultObjectName)
}

// ManifestArtifact references one raw artifact the Job uploaded.
type ManifestArtifact struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	ObjectKey string `json:"object_key"`
}

// Manifest is the typed summary the Job writes to manifest.json. It is the
// only Job output the controller parses; everything else stays in object
// storage and is referenced by key and digest.
type Manifest struct {
	SchemaVersion   string             `json:"schema_version"`
	Tool            string             `json:"tool"`
	Status          string             `json:"status"`
	FindingCount    int                `json:"finding_count"`
	ResultObjectKey string             `json:"result_object_key"`
	ResultDigest    string             `json:"result_digest"`
	Artifacts       []ManifestArtifact `json:"artifacts,omitempty"`
	Errors          []string           `json:"errors,omitempty"`
	// ReproducibilityClass states how the result can be reproduced. It is
	// optional so older Jobs keep validating, but a Job that reports a
	// non-deterministic class must also report its trials.
	ReproducibilityClass string `json:"reproducibility_class,omitempty"`
	// BoundedScope is what a not_found_under result is bounded by.
	BoundedScope *ManifestBoundedScope `json:"bounded_scope,omitempty"`
	// HarnessHealth records whether the run could have failed at all.
	HarnessHealth *ManifestHarnessHealth `json:"harness_health,omitempty"`
	// Trials preserves the attempts behind a non-deterministic result.
	Trials *ManifestTrials `json:"trials,omitempty"`
}

// ManifestBoundedScope is the scope a bounded negative result is limited to.
type ManifestBoundedScope struct {
	Harness     string   `json:"harness,omitempty"`
	Corpus      string   `json:"corpus,omitempty"`
	Seeds       []string `json:"seeds,omitempty"`
	Bounds      string   `json:"bounds,omitempty"`
	Environment string   `json:"environment,omitempty"`
	Coverage    string   `json:"coverage,omitempty"`
}

// ManifestHarnessHealth is the Job's evidence that its assertions could fail.
type ManifestHarnessHealth struct {
	EntryPointReached     bool   `json:"entry_point_reached,omitempty"`
	TargetCodeExecuted    bool   `json:"target_code_executed,omitempty"`
	NegativeControlRan    bool   `json:"negative_control_ran,omitempty"`
	NegativeControlPassed bool   `json:"negative_control_passed,omitempty"`
	OracleCanFail         bool   `json:"oracle_can_fail,omitempty"`
	MutationsKilled       int    `json:"mutations_killed,omitempty"`
	MutationsTotal        int    `json:"mutations_total,omitempty"`
	Notes                 string `json:"notes,omitempty"`
}

// ManifestTrials preserves every attempt behind a non-deterministic result.
type ManifestTrials struct {
	Attempts            int    `json:"attempts,omitempty"`
	Successes           int    `json:"successes,omitempty"`
	StoppingRule        string `json:"stopping_rule,omitempty"`
	EquivalenceCriteria string `json:"equivalence_criteria,omitempty"`
	TraceDigest         string `json:"trace_digest,omitempty"`
	Topology            string `json:"topology,omitempty"`
	ForkState           string `json:"fork_state,omitempty"`
}

// ManifestStatuses are the verdicts a Job may report. "not_found_under" is the
// bounded negative result; "pass" is its retired spelling, still accepted so
// stored manifests keep validating.
var ManifestStatuses = []string{"not_found_under", "pass", "findings", "error", "timeout", "partial", "not_applicable"}

// ManifestReproducibilityClasses are the classes a Job may report.
var ManifestReproducibilityClasses = []string{
	"deterministic", "seeded_replayable", "schedule_or_environment_dependent",
	"statistical", "observational_only",
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Validate rejects manifests the controller must not trust.
func (m Manifest) Validate() error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("unsupported manifest schema_version %q", m.SchemaVersion)
	}
	if strings.TrimSpace(m.Tool) == "" {
		return fmt.Errorf("manifest tool is required")
	}
	if !slices.Contains(ManifestStatuses, m.Status) {
		return fmt.Errorf("manifest status %q is not a known verdict", m.Status)
	}
	if m.FindingCount < 0 {
		return fmt.Errorf("manifest finding_count must not be negative")
	}
	if m.ReproducibilityClass != "" && !slices.Contains(ManifestReproducibilityClasses, m.ReproducibilityClass) {
		return fmt.Errorf("manifest reproducibility_class %q is not a known class", m.ReproducibilityClass)
	}
	// A non-deterministic result that reports no trials cannot be
	// interpreted: a single lucky reproduction and a reliable one look the
	// same.
	switch m.ReproducibilityClass {
	case "schedule_or_environment_dependent", "statistical":
		if m.Trials == nil || m.Trials.Attempts <= 0 {
			return fmt.Errorf("manifest reproducibility_class %q requires trials with a positive attempt count", m.ReproducibilityClass)
		}
		if m.Trials.Successes > m.Trials.Attempts {
			return fmt.Errorf("manifest trials report more successes than attempts")
		}
	}
	if health := m.HarnessHealth; health != nil {
		if health.MutationsKilled < 0 || health.MutationsTotal < 0 {
			return fmt.Errorf("manifest harness_health mutation counts must not be negative")
		}
		if health.MutationsKilled > health.MutationsTotal {
			return fmt.Errorf("manifest harness_health killed more mutants than it ran")
		}
		// Claiming the oracle can fail is only meaningful when something
		// actually made it fail.
		if health.OracleCanFail && health.MutationsTotal == 0 && strings.TrimSpace(health.Notes) == "" {
			return fmt.Errorf("manifest harness_health claims oracle_can_fail without a mutation result or an explanation")
		}
	}
	if m.ResultObjectKey != "" && !digestPattern.MatchString(m.ResultDigest) {
		return fmt.Errorf("manifest result_digest must be an immutable sha256 digest")
	}
	for i, artifact := range m.Artifacts {
		if strings.TrimSpace(artifact.ObjectKey) == "" || !digestPattern.MatchString(artifact.Digest) {
			return fmt.Errorf("manifest artifacts[%d] requires an object key and sha256 digest", i)
		}
	}
	return nil
}

// ValidateFor additionally confines every object key the manifest reports to
// the run's own output prefix: a Job must not be able to make the controller
// record, and the agent read back, another run's objects.
func (m Manifest) ValidateFor(outputPrefix string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	prefix := strings.TrimSuffix(outputPrefix, "/") + "/"
	if m.ResultObjectKey != "" {
		if err := checkObjectKeyPrefix("result_object_key", m.ResultObjectKey, prefix); err != nil {
			return err
		}
	}
	for i, artifact := range m.Artifacts {
		if err := checkObjectKeyPrefix(fmt.Sprintf("artifacts[%d].object_key", i), artifact.ObjectKey, prefix); err != nil {
			return err
		}
	}
	return nil
}

func checkObjectKeyPrefix(field, key, prefix string) error {
	if !strings.HasPrefix(key, prefix) || slices.Contains(strings.Split(key, "/"), "..") {
		return fmt.Errorf("manifest %s %q is outside the run output prefix %s", field, key, prefix)
	}
	return nil
}
