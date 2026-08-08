package securitytoolpacks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/security"
)

func TestRedactedRunConfigSanitizesScopeURLs(t *testing.T) {
	cfg := RunConfig{
		Target:    Target{Locator: "https://user:pass@example.test/api?token=secret#fragment"},
		Scope:     []string{"https://scope-user:scope-pass@example.test/private?authorization=secret#x"},
		Arguments: map[string]string{"token": "argument-secret"},
	}
	redacted := redactedRunConfig(cfg)
	data, _ := json.Marshal(redacted)
	for _, secret := range []string{"user", "pass", "token=secret", "scope-user", "scope-pass", "authorization=secret", "argument-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("redacted config leaks %q: %s", secret, data)
		}
	}
	if cfg.Scope[0] == redacted.Scope[0] {
		t.Fatal("scope was not redacted")
	}
}

func TestRegistryDefensivelyCopiesManifest(t *testing.T) {
	manifest := fixtureManifest()
	registry, err := NewRegistry(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Tools[0].Invocation[0] = "attacker-command"
	copy := registry.Manifest()
	copy.Tools[0].Invocation[0] = "other-command"
	tool, _ := registry.Tool("playwright")
	if tool.Invocation[0] == "attacker-command" || tool.Invocation[0] == "other-command" {
		t.Fatal("registry was mutated through an alias")
	}
}

func TestDisabledCatalogToolCannotBuildInvocation(t *testing.T) {
	pin := sha256Digest([]byte("fixture"))
	registry, err := NewRegistry(DefaultManifest(pin, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = registry.BuildInvocation(RunConfig{Tool: "playwright", Target: Target{Type: "base_url", Locator: "https://example.test", Revision: "v1", Digest: pin}, Scope: []string{"https://example.test"}})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err=%v", err)
	}
}

func TestZAPPlanIsScopeBoundAndUsesFixedReport(t *testing.T) {
	pin := sha256Digest([]byte("fixture"))
	registry, err := NewRegistry(DefaultManifest(pin, nil))
	if err != nil {
		t.Fatal(err)
	}
	valid := `env:
  contexts:
    - name: fixture
      urls: ["https://api.example.test/v1"]
jobs:
  - type: spider
    parameters:
      context: fixture
      maxDuration: 1
  - type: passiveScan-wait
  - type: report
    parameters:
      template: traditional-json
      reportDir: /work
      reportFile: zap-report
`
	path := filepath.Join(t.TempDir(), "plan.yaml")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{Tool: "owasp-zap", Target: Target{Type: "zap_plan", Locator: path, Revision: "v1", Digest: sha256Digest([]byte(valid))}, Arguments: map[string]string{"base_url": "https://api.example.test/v1"}, Scope: []string{"https://api.example.test/v1"}}
	if _, _, err := registry.BuildInvocation(cfg); err != nil {
		t.Fatalf("valid plan: %v", err)
	}
	unsafe := strings.Replace(valid, "https://api.example.test/v1", "https://outside.example.test", 1)
	if err := os.WriteFile(path, []byte(unsafe), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.BuildInvocation(cfg); err == nil || !strings.Contains(err.Error(), "outside configured scope") {
		t.Fatalf("unsafe plan err=%v", err)
	}
	uppercase := strings.Replace(valid, "https://api.example.test/v1", "HTTPS://outside.example.test", 1)
	if err := os.WriteFile(path, []byte(uppercase), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.BuildInvocation(cfg); err == nil || !strings.Contains(err.Error(), "outside configured scope") {
		t.Fatalf("uppercase scheme err=%v", err)
	}
	noOp := strings.Replace(valid, "  - type: spider\n    parameters:\n      context: fixture\n      maxDuration: 1\n", "", 1)
	if err := os.WriteFile(path, []byte(noOp), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.BuildInvocation(cfg); err == nil || !strings.Contains(err.Error(), "request-producing") {
		t.Fatalf("no-op plan err=%v", err)
	}
	disabled := strings.Replace(valid, "  - type: spider", "  - type: spider\n    enabled: false", 1)
	if err := os.WriteFile(path, []byte(disabled), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.BuildInvocation(cfg); err == nil || !strings.Contains(err.Error(), "cannot be disabled") {
		t.Fatalf("disabled scan err=%v", err)
	}
	proxy := strings.Replace(valid, "env:\n", "env:\n  proxy:\n    hostname: outside.example.test\n", 1)
	if err := os.WriteFile(path, []byte(proxy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.BuildInvocation(cfg); err == nil || !strings.Contains(err.Error(), "field \"proxy\"") {
		t.Fatalf("proxy plan err=%v", err)
	}
	duplicate := valid + "\nenv:\n  proxy:\n    hostname: outside.example.test\n"
	if err := os.WriteFile(path, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.BuildInvocation(cfg); err == nil || !strings.Contains(err.Error(), "duplicate mapping key") {
		t.Fatalf("duplicate key err=%v", err)
	}
}

func TestExecutableOCIToolsUseImmutableRuntimeClosures(t *testing.T) {
	pin := sha256Digest([]byte("fixture"))
	registry, err := NewRegistry(DefaultManifest(pin, nil))
	if err != nil {
		t.Fatal(err)
	}
	dockerfiles := ""
	for _, path := range []string{"../../Dockerfile.security-tools", "../../Dockerfile.injector"} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		dockerfiles += string(data)
	}
	for _, name := range []string{"owasp-zap", "schemathesis", "sslyze", "nmap", "zeek", "suricata"} {
		tool, ok := registry.Tool(name)
		if !ok || !tool.Enabled {
			t.Fatalf("%s is not executable: %+v", name, tool)
		}
		if tool.OCIRoot != name || !strings.Contains(tool.Image, "@"+tool.ImageDigest) || tool.ToolArtifactDigest != tool.ImageDigest || tool.WrapperDigest != pin || tool.PlatformDigests["amd64"] == "" || tool.PlatformDigests["arm64"] == "" || tool.OCIExecutable == "" {
			t.Fatalf("%s has incomplete OCI provenance: %+v", name, tool)
		}
		if strings.Count(dockerfiles, tool.Image) != 2 {
			t.Fatalf("%s pin is not present exactly once in each runtime Dockerfile", name)
		}
		if name != "schemathesis" && tool.ExitCodes[1] != StatusError {
			t.Fatalf("%s exit 1 must be operational error", name)
		}
	}
}

func TestExecutableToolArtifactPinsMatchRuntimeLock(t *testing.T) {
	data, err := os.ReadFile("../../security-tools.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Tools []struct {
			Name      string `json:"name"`
			Platforms map[string]struct {
				BinarySHA256 string `json:"binary_sha256"`
			} `json:"platforms"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	locked := map[string]string{}
	for _, tool := range lock.Tools {
		locked[tool.Name] = "sha256:" + tool.Platforms["linux/"+runtime.GOARCH].BinarySHA256
	}
	manifest := DefaultManifest(sha256Digest([]byte("image")), map[string]string{"nuclei": sha256Digest([]byte("templates"))})
	aliases := map[string]string{"forge-security-tests": "forge"}
	for _, name := range []string{"nuclei", "naabu", "aderyn", "forge-security-tests"} {
		lockName := name
		if aliases[name] != "" {
			lockName = aliases[name]
		}
		var got string
		for _, tool := range manifest.Tools {
			if tool.Name == name {
				got = tool.ToolArtifactDigest
			}
		}
		if got == "" || got != locked[lockName] {
			t.Fatalf("%s manifest pin %q != lock pin %q", name, got, locked[lockName])
		}
	}
	knowledge, err := os.ReadFile("../../security-knowledge/nuclei-reviewed.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range manifest.Tools {
		if tool.Name == "nuclei" && tool.KnowledgeDigests["bundle"] != sha256Digest(knowledge) {
			t.Fatal("compiled Nuclei knowledge pin does not match reviewed template")
		}
	}
}

func TestScopeMustContainLiveTarget(t *testing.T) {
	if !scopeAllowsTarget("https://api.example.test/v1/users", []string{"https://api.example.test/v1"}) {
		t.Fatal("expected URL subpath in scope")
	}
	if !scopeAllowsTarget("192.0.2.10", []string{"192.0.2.0/24"}) {
		t.Fatal("expected address in prefix")
	}
	if scopeAllowsTarget("https://other.example.test", []string{"https://api.example.test"}) {
		t.Fatal("unrelated host was accepted")
	}
	if scopeAllowsTarget("http://api.example.test:8443/v10", []string{"https://api.example.test:443/v1"}) {
		t.Fatal("different scheme, port, and path segment were accepted")
	}
	if scopeAllowsTarget("https://api.example.test/v10", []string{"https://api.example.test/v1"}) {
		t.Fatal("raw path prefix was accepted")
	}
	for _, target := range []string{"https://api.example.test/v1/../admin", "https://api.example.test/v1/%2e%2e/admin", "https://api.example.test/v1/%2Fadmin", "https://api.example.test/v1/%252e%252e/admin", "https://api.example.test/v1/%252fadmin", "https://api.example.test/v1\\..\\admin"} {
		if scopeAllowsTarget(target, []string{"https://api.example.test/v1"}) {
			t.Fatalf("ambiguous traversal target accepted: %s", target)
		}
	}
	if scopeAllowsTarget("198.51.100.10", []string{"192.0.2.0/24"}) {
		t.Fatal("out-of-prefix address was accepted")
	}
}

func TestStableRecordsUsesTotalSerializedTieBreaker(t *testing.T) {
	a := ScannerRecord{Tool: "x", RuleID: "R", FilePath: "a", Message: "same", Severity: "low"}
	b := a
	b.Severity = "high"
	records := []ScannerRecord{a, b}
	pipeline := []security.ScannerRecord{toPipelineRecord(records[0]), toPipelineRecord(records[1])}
	stableRecords(pipeline)
	first, _ := canonicalJSON(pipeline)
	pipeline[0], pipeline[1] = pipeline[1], pipeline[0]
	stableRecords(pipeline)
	second, _ := canonicalJSON(pipeline)
	if string(first) != string(second) {
		t.Fatalf("ordering is not total:\n%s\n%s", first, second)
	}
}

func TestEnabledExternalToolsHaveExactArgv(t *testing.T) {
	registry, err := NewRegistry(DefaultManifest(sha256Digest([]byte("wrapper")), nil))
	if err != nil {
		t.Fatal(err)
	}
	seed := int64(42)
	cases := []struct {
		name   string
		config RunConfig
		want   []string
	}{
		{
			name:   "nuclei",
			config: RunConfig{Tool: "nuclei", Target: Target{Type: "base_url", Locator: "https://api.example.test/v1", Revision: "fixture-v1", Digest: sha256Digest([]byte("nuclei"))}, Arguments: map[string]string{"rate": "5"}, Scope: []string{"https://api.example.test/v1"}},
			want:   []string{"nuclei", "-u", "https://api.example.test/v1", "-templates", "@operator/nuclei-reviewed.yaml", "-rate-limit", "5", "-concurrency", "1", "-bulk-size", "1", "-jsonl", "-silent", "-disable-update-check", "-no-interactsh"},
		},
		{
			name:   "naabu",
			config: RunConfig{Tool: "naabu", Target: Target{Type: "address_scope", Locator: "192.0.2.10", Revision: "fixture-v1", Digest: sha256Digest([]byte("naabu"))}, Arguments: map[string]string{"ports": "80,443", "rate": "25"}, Scope: []string{"192.0.2.10"}},
			want:   []string{"naabu", "-host", "192.0.2.10", "-p", "80,443", "-rate", "25", "-c", "4", "-scan-type", "c", "-retries", "1", "-json", "-silent", "-disable-update-check"},
		},
		{
			name:   "aderyn",
			config: RunConfig{Tool: "aderyn", Target: Target{Type: "solidity_project", Locator: "/workspace/project", Revision: "fixture-v1", Digest: sha256Digest([]byte("aderyn")), MediaType: "application/vnd.gratefulagents.solidity-project.v1+directory"}},
			want:   []string{"aderyn", "/workspace/project", "--output", "report.sarif", "--stdout", "--skip-update-check"},
		},
		{
			name:   "forge-security-tests",
			config: RunConfig{Tool: "forge-security-tests", Target: Target{Type: "foundry_project", Locator: "/workspace/project", Revision: "fixture-v1", Digest: sha256Digest([]byte("forge")), MediaType: "application/vnd.gratefulagents.foundry-security-project.v1+directory"}, Seed: &seed},
			want:   []string{"forge", "test", "--root", "/workspace/project", "--junit", "--fuzz-seed", "42", "--offline", "--threads", "1"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			invocation, _, err := registry.BuildInvocation(test.config)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(invocation.Argv, test.want) {
				t.Fatalf("argv=%q, want %q", invocation.Argv, test.want)
			}
		})
	}
}
