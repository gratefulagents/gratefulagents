package securitytoolpacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
	Record  ScannerRecord
	Asset   string
	Skipped bool
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
	res := Result{Stages: append([]string(nil), workflowStages...)}
	inv, tool, err := r.registry.BuildInvocation(cfg)
	if err != nil {
		var applicability notApplicableError
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
	res.Replay = Replay{Target: publicCfg.Target, Tool: tool.Name, ToolVersion: tool.Version, ImageDigest: tool.ImageDigest, Knowledge: tool.KnowledgeDigests, Configuration: publicJSON, ConfigurationID: sha256Digest(cfgJSON), Seed: cfg.Seed, InputDigests: []string{cfg.Target.Digest}}
	if r.sandbox == nil {
		res.Status = StatusError
		res.Errors = []string{"no sandbox executor configured"}
		return res
	}
	native := r.sandbox.Execute(ctx, ExecutionRequest{Invocation: inv, Tool: tool, Config: cfg})
	res.Coverage = Coverage{Examined: sortedUnique(native.Examined), Skipped: sortedUnique(native.Skipped), Uncovered: sortedUnique(native.Uncovered)}
	res.Replay.Environment = stableEnvironment(native.Environment)
	redactor := NewRedactor(cfg.Sensitive...)
	if int64(len(native.Output)) > tool.Budgets.MaxOutputSize {
		native.Err = fmt.Errorf("native output exceeds %d-byte budget", tool.Budgets.MaxOutputSize)
	}
	if len(native.Output) > 0 {
		res.Artifacts = []Artifact{{Name: "raw-output", MediaType: tool.OutputMediaType, Digest: sha256Digest(native.Output), Size: len(native.Output), Data: append([]byte(nil), native.Output...)}}
	}
	if native.TimedOut || errors.Is(native.Err, context.DeadlineExceeded) {
		res.Status = StatusTimeout
		if native.Err != nil {
			res.Errors = []string{redactor.Text(native.Err.Error())}
		}
		return res
	}
	adapter, ok := r.adapters[tool.Adapter]
	if !ok {
		res.Status = StatusError
		res.Errors = []string{"normalization adapter not registered: " + tool.Adapter}
		return res
	}
	var adapted []securityRecord
	if len(native.Output) > 0 {
		adapted, err = adapter.Normalize(tool, cfg.Target, native.Output, redactor)
	}
	if err != nil {
		res.Errors = append(res.Errors, "normalize: "+redactor.Text(err.Error()))
	}
	artifactDigest := ""
	if len(res.Artifacts) > 0 {
		artifactDigest = res.Artifacts[0].Digest
	}
	for _, a := range adapted {
		if a.Skipped {
			res.Coverage.Skipped = sortedUnique(append(res.Coverage.Skipped, a.Asset))
			continue
		}
		rec := toPipelineRecord(a.Record)
		if rec.Extra == nil {
			rec.Extra = map[string]string{}
		}
		rec.Extra["raw_artifact_digest"] = artifactDigest
		res.Findings = append(res.Findings, rec)
	}
	stableRecords(res.Findings)
	if native.Err != nil {
		res.Errors = append(res.Errors, redactor.Text(native.Err.Error()))
	}
	mapped, known := tool.ExitCodes[native.ExitCode]
	if known && mapped == StatusTimeout {
		res.Status = StatusTimeout
		return res
	}
	if known && mapped == StatusFindings && len(res.Findings) == 0 {
		res.Errors = append(res.Errors, "exit code indicated findings but normalization produced none")
	}
	if known && mapped == StatusPass && len(res.Coverage.Examined) == 0 && len(res.Coverage.Skipped) == 0 && len(res.Coverage.Uncovered) == 0 {
		res.Coverage.Uncovered = []string{cfg.Target.Locator}
	}
	incomplete := len(res.Coverage.Skipped) > 0 || len(res.Coverage.Uncovered) > 0 || len(res.Errors) > 0
	switch {
	case !known:
		res.Errors = append(res.Errors, fmt.Sprintf("unmapped exit code %d", native.ExitCode))
		if len(res.Findings) > 0 {
			res.Status = StatusPartial
		} else {
			res.Status = StatusError
		}
	case mapped == StatusError || native.Err != nil || err != nil:
		if len(res.Findings) > 0 {
			res.Status = StatusPartial
		} else {
			res.Status = StatusError
		}
	case incomplete:
		res.Status = StatusPartial
	case len(res.Findings) > 0:
		res.Status = StatusFindings
	case mapped == StatusPass && len(res.Coverage.Examined) > 0:
		res.Status = StatusPass
	default:
		res.Status = StatusError
		res.Errors = append(res.Errors, "execution did not satisfy pass criteria")
	}
	return res
}

func toPipelineRecord(r ScannerRecord) security.ScannerRecord {
	return security.ScannerRecord{Tool: r.Tool, ToolVersion: r.ToolVersion, RuleID: r.RuleID, RuleName: r.RuleName, Message: r.Message, Severity: r.Severity, Category: r.Category, FilePath: r.FilePath, StartLine: r.StartLine, EndLine: r.EndLine, Symbol: r.Symbol, CWE: r.CWE, References: r.References, RawEvidence: r.RawEvidence, Extra: r.Extra}
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
	out := in
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
	if u, err := url.Parse(out.Target.Locator); err == nil && u.IsAbs() {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		out.Target.Locator = u.String()
	}
	return out
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
