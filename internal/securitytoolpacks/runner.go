package securitytoolpacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"runtime"
	"slices"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/security"
)

var workflowStages = []string{
	"target_classification", "applicability_detection", "registry_selection",
	"sandboxed_execution", "raw_artifact_collection", "normalization_and_evidence_validation",
	"safe_replay_or_corroboration", "deduplication_and_reporting", "coverage_failure_summary",
}

type ExecutionRequest struct {
	Invocation Invocation
	Tool       Tool
	Config     RunConfig
}
type NativeResult struct {
	Output                       []byte
	Artifacts                    []Artifact
	ExitCode                     int
	TimedOut                     bool
	Environment                  map[string]string
	Examined, Skipped, Uncovered []string
	Err                          error
}
type Sandbox interface {
	Execute(context.Context, ExecutionRequest) NativeResult
}
type Adapter interface {
	Normalize(tool Tool, target Target, native []byte, redactor Redactor) ([]securityRecord, error)
}

// securityRecord is an adapter record plus its asset identifier. The alias-like
// wrapper keeps adapters independent from persistence while ToScannerRecord
// still feeds the established scanner finding pipeline.
type securityRecord struct {
	Record    ScannerRecord
	Asset     string
	Examined  bool
	Skipped   bool
	Uncovered bool
}

// local aliases keep adapter signatures readable.
type ScannerRecord = struct {
	Tool        string            `json:"tool"`
	ToolVersion string            `json:"tool_version,omitempty"`
	RuleID      string            `json:"rule_id"`
	RuleName    string            `json:"rule_name,omitempty"`
	Message     string            `json:"message"`
	Severity    string            `json:"severity"`
	Category    string            `json:"category,omitempty"`
	FilePath    string            `json:"file_path"`
	StartLine   int               `json:"start_line,omitempty"`
	EndLine     int               `json:"end_line,omitempty"`
	Symbol      string            `json:"symbol,omitempty"`
	CWE         string            `json:"cwe,omitempty"`
	References  []string          `json:"references,omitempty"`
	RawEvidence string            `json:"raw_evidence,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`
}

// Runner enforces registry selection and resource metadata before delegating
// to a sandbox. Retry is intentionally left to the controller; callers may
// retry only when Tool.Idempotent or Tool.Resettable is true.
type Runner struct {
	registry *Registry
	sandbox  Sandbox
	adapters map[string]Adapter
}

func NewRunner(registry *Registry, sandbox Sandbox) *Runner {
	return &Runner{registry: registry, sandbox: sandbox, adapters: DefaultAdapters()}
}
func (r *Runner) WithAdapter(name string, adapter Adapter) *Runner {
	r.adapters[name] = adapter
	return r
}

func (r *Runner) Run(ctx context.Context, cfg RunConfig) Result {
	cfg = cloneRunConfig(cfg)
	res := Result{Stages: append([]string(nil), workflowStages...)}
	inv, tool, err := r.registry.BuildInvocation(cfg)
	if err != nil {
		var applicability *ApplicabilityError
		if errors.As(err, &applicability) {
			res.Status = StatusNotApplicable
		} else {
			res.Status = StatusError
		}
		res.Errors = []string{NewRedactor(cfg.Sensitive...).Text(err.Error())}
		return res
	}
	cfgJSON, err := canonicalJSON(cfg)
	if err != nil {
		res.Status = StatusError
		res.Errors = []string{err.Error()}
		return res
	}
	publicCfg := redactedRunConfig(cfg)
	publicJSON, _ := canonicalJSON(publicCfg)
	res.Replay = Replay{Target: publicCfg.Target, Tool: tool.Name, ToolVersion: tool.Version, ImageDigest: tool.ImageDigest, ToolArtifactDigest: tool.ToolArtifactDigest, WrapperDigest: tool.WrapperDigest, PlatformDigest: tool.PlatformDigests[runtime.GOARCH], Knowledge: cloneStringMap(tool.KnowledgeDigests), Configuration: publicJSON, ConfigurationID: sha256Digest(cfgJSON), Seed: cloneInt64(cfg.Seed), InputDigests: []string{cfg.Target.Digest}}
	if r.sandbox == nil {
		res.Status = StatusError
		res.Errors = []string{"no sandbox executor configured"}
		return res
	}
	native := r.sandbox.Execute(ctx, ExecutionRequest{Invocation: inv, Tool: cloneTool(tool), Config: cloneRunConfig(cfg)})
	res.Coverage = Coverage{Examined: sortedUnique(native.Examined), Skipped: sortedUnique(native.Skipped), Uncovered: sortedUnique(native.Uncovered)}
	res.Replay.Environment = stableEnvironment(native.Environment)
	res.Replay.SandboxDigest = native.Environment["sandbox_digest"]
	redactor := NewRedactor(cfg.Sensitive...)
	if int64(len(native.Output)) > tool.Budgets.MaxOutputSize {
		native.Err = fmt.Errorf("native output exceeds %d-byte budget", tool.Budgets.MaxOutputSize)
	}
	if len(native.Output) > 0 {
		res.Artifacts = []Artifact{{Name: "raw-output", MediaType: tool.OutputMediaType, Digest: sha256Digest(native.Output), Size: len(native.Output), Data: append([]byte(nil), native.Output...)}}
	}
	for _, artifact := range native.Artifacts {
		artifact.Data = append([]byte(nil), artifact.Data...)
		res.Artifacts = append(res.Artifacts, artifact)
	}
	if native.TimedOut || errors.Is(native.Err, context.DeadlineExceeded) {
		res.Status = StatusTimeout
		if native.Err != nil {
			res.Errors = []string{redactor.Text(native.Err.Error())}
		}
		return res
	}
	err = r.normalizeNative(tool, cfg.Target, native.Output, redactor, &res)
	stableRecords(res.Findings)
	if native.Err != nil {
		res.Errors = append(res.Errors, redactor.Text(native.Err.Error()))
	}
	finalizeStatus(&res, tool, cfg.Target, native, err)
	return res
}

func (r *Runner) normalizeNative(tool Tool, target Target, output []byte, redactor Redactor, res *Result) error {
	adapter, ok := r.adapters[tool.Adapter]
	if !ok {
		res.Errors = append(res.Errors, "normalization adapter not registered: "+tool.Adapter)
		return fmt.Errorf("adapter %s is not registered", tool.Adapter)
	}
	var adapted []securityRecord
	var err error
	if len(output) > 0 {
		adapted, err = adapter.Normalize(cloneTool(tool), target, output, redactor)
	}
	if err != nil {
		res.Errors = append(res.Errors, "normalize: "+redactor.Text(err.Error()))
	}
	artifactDigest := ""
	if len(res.Artifacts) > 0 {
		artifactDigest = res.Artifacts[0].Digest
	}
	for _, item := range adapted {
		if item.Examined {
			res.Coverage.Examined = sortedUnique(append(res.Coverage.Examined, item.Asset))
			continue
		}
		if item.Skipped {
			res.Coverage.Skipped = sortedUnique(append(res.Coverage.Skipped, item.Asset))
			continue
		}
		if item.Uncovered {
			res.Coverage.Uncovered = sortedUnique(append(res.Coverage.Uncovered, item.Asset))
			continue
		}
		record := toPipelineRecord(item.Record)
		if record.Extra == nil {
			record.Extra = map[string]string{}
		}
		record.Extra["raw_artifact_digest"] = artifactDigest
		res.Findings = append(res.Findings, record)
	}
	return err
}

func finalizeStatus(res *Result, tool Tool, target Target, native NativeResult, normalizationErr error) {
	mapped, known := tool.ExitCodes[native.ExitCode]
	if known && mapped == StatusTimeout {
		res.Status = StatusTimeout
		return
	}
	inconsistent := known && mapped == StatusFindings && len(res.Findings) == 0
	if inconsistent {
		res.Errors = append(res.Errors, "exit code indicated findings but normalization produced none")
	}
	if known && isCleanExit(mapped) && len(res.Coverage.Examined) == 0 && len(res.Coverage.Skipped) == 0 && len(res.Coverage.Uncovered) == 0 {
		res.Coverage.Uncovered = []string{target.Locator}
	}
	hasFindings := len(res.Findings) > 0
	if !known {
		res.Errors = append(res.Errors, fmt.Sprintf("unmapped exit code %d", native.ExitCode))
		res.Status = statusForFailedRun(hasFindings)
		return
	}
	if mapped == StatusError || native.Err != nil || normalizationErr != nil || inconsistent {
		res.Status = statusForFailedRun(hasFindings)
		return
	}
	if len(res.Coverage.Skipped) > 0 || len(res.Coverage.Uncovered) > 0 || len(res.Errors) > 0 {
		res.Status = StatusPartial
		return
	}
	if hasFindings {
		res.Status = StatusFindings
		return
	}
	if isCleanExit(mapped) && len(res.Coverage.Examined) > 0 {
		// A clean run is a bounded negative result, never a statement that
		// the target is safe: it says only that this harness, over this
		// coverage, found nothing.
		res.Status = StatusNotFoundUnder
		if res.Bounded == nil {
			res.Bounded = &BoundedScope{}
		}
		if res.Bounded.Coverage == "" {
			res.Bounded.Coverage = summarizeCoverage(res.Coverage.Examined)
		}
		if res.Bounded.Environment == "" {
			res.Bounded.Environment = tool.Name + " " + tool.Version
		}
		if res.Bounded.Bounds == "" && res.Replay.Seed != nil {
			res.Bounded.Bounds = fmt.Sprintf("seed=%d", *res.Replay.Seed)
		}
		return
	}
	res.Status = StatusError
	res.Errors = append(res.Errors, "execution did not satisfy the bounded-negative criteria")
}

// summarizeCoverage renders what a bounded negative result covered without
// growing with the target: a suite with hundreds of assets would otherwise
// produce a coverage string the API server rejects, and a rejected status
// update loses the whole run.
func summarizeCoverage(examined []string) string {
	if len(examined) == 0 {
		return ""
	}
	var b strings.Builder
	shown := 0
	for _, asset := range examined {
		next := asset
		if shown > 0 {
			next = ", " + asset
		}
		if b.Len()+len(next) > maxCoverageSummaryBytes {
			break
		}
		b.WriteString(next)
		shown++
	}
	if shown == len(examined) {
		return fmt.Sprintf("%d examined: %s", len(examined), b.String())
	}
	if shown == 0 {
		return fmt.Sprintf("%d examined", len(examined))
	}
	return fmt.Sprintf("%d examined, first %d: %s", len(examined), shown, b.String())
}

// isCleanExit reports whether a mapped exit code means the tool ran to
// completion without reporting findings. The retired "pass" verdict is still
// honoured here because registered packs map exit code 0 to it.
func isCleanExit(status Status) bool {
	return status == StatusPass || status == StatusNotFoundUnder
}

func statusForFailedRun(hasFindings bool) Status {
	if hasFindings {
		return StatusPartial
	}
	return StatusError
}

func toPipelineRecord(r ScannerRecord) security.ScannerRecord {
	return security.ScannerRecord{Tool: r.Tool, ToolVersion: r.ToolVersion, RuleID: r.RuleID, RuleName: r.RuleName, Message: r.Message, Severity: r.Severity, Category: r.Category, FilePath: r.FilePath, StartLine: r.StartLine, EndLine: r.EndLine, Symbol: r.Symbol, CWE: r.CWE, References: append([]string(nil), r.References...), RawEvidence: r.RawEvidence, Extra: cloneStringMap(r.Extra)}
}

func cloneRunConfig(in RunConfig) RunConfig {
	out := in
	out.Arguments = cloneStringMap(in.Arguments)
	out.Scope = append([]string(nil), in.Scope...)
	out.Sensitive = append([]string(nil), in.Sensitive...)
	out.Seed = cloneInt64(in.Seed)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func cloneInt64(in *int64) *int64 {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func sortedUnique(in []string) []string {
	m := map[string]bool{}
	for _, v := range in {
		if strings.TrimSpace(v) != "" {
			m[v] = true
		}
	}
	out := make([]string, 0, len(m))
	for v := range m {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}
func stableEnvironment(in map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"os", "arch", "kernel", "runtime", "compiler", "build", "assembly_digest"} {
		if v := in[k]; v != "" {
			out[k] = v
		}
	}
	return out
}

func redactedRunConfig(in RunConfig) RunConfig {
	out := cloneRunConfig(in)
	out.Arguments = make(map[string]string, len(in.Arguments))
	sensitive := map[string]bool{"authorization": true, "cookie": true, "token": true, "password": true, "secret": true, "private_key": true}
	for _, name := range in.Sensitive {
		sensitive[strings.ToLower(name)] = true
	}
	redactor := NewRedactor(in.Sensitive...)
	for k, v := range in.Arguments {
		if sensitive[strings.ToLower(k)] {
			out.Arguments[k] = "secret-ref:" + sha256Digest([]byte(v))
			continue
		}
		out.Arguments[k] = redactor.Text(v)
	}
	out.Target.Locator = redactedLocator(out.Target.Locator, redactor)
	for i := range out.Scope {
		out.Scope[i] = redactedLocator(out.Scope[i], redactor)
	}
	return out
}

func redactedLocator(locator string, redactor Redactor) string {
	locator = redactor.Text(locator)
	if u, err := url.Parse(locator); err == nil && u.IsAbs() {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	return locator
}

// MarshalCanonical removes raw-artifact identity and bytes in addition to
// timestamps. Equivalent normalized outputs therefore compare independently
// of volatile native evidence while Replay still retains its protected digest.
func MarshalCanonical(result Result) ([]byte, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var clone Result
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil, err
	}
	result = clone
	result.Artifacts = nil
	for i := range result.Findings {
		if result.Findings[i].Extra == nil {
			continue
		}
		extra := make(map[string]string, len(result.Findings[i].Extra))
		for k, v := range result.Findings[i].Extra {
			if k != "raw_artifact_digest" {
				extra[k] = v
			}
		}
		result.Findings[i].Extra = extra
	}
	return json.Marshal(result)
}
