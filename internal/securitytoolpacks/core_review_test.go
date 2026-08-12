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
	// Scanner toolroots ship only in the security-tools runtime image; the
	// injector no longer carries them.
	dockerfiles := ""
	for _, path := range []string{"../../Dockerfile.security-tools"} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		dockerfiles += string(data)
	}
	for _, name := range []string{"owasp-zap", "schemathesis", "sslyze", "nmap", "zeek", "suricata", "slither", "halmos"} {
		tool, ok := registry.Tool(name)
		if !ok || !tool.Enabled {
			t.Fatalf("%s is not executable: %+v", name, tool)
		}
		artifactPinned := tool.ToolArtifactDigest == tool.ImageDigest
		if name == "halmos" {
			artifactPinned = tool.ToolArtifactDigest == "sha256:7ac9f37f8554d8354a7a924eb81393fe30f1bbe851e07c4c35f33a935f53593f"
		}
		if tool.OCIRoot != name || !strings.Contains(tool.Image, "@"+tool.ImageDigest) || !artifactPinned || tool.WrapperDigest != pin || tool.PlatformDigests["amd64"] == "" || tool.PlatformDigests["arm64"] == "" || tool.OCIExecutable == "" {
			t.Fatalf("%s has incomplete OCI provenance: %+v", name, tool)
		}
		if strings.Count(dockerfiles, tool.Image) != 1 {
			t.Fatalf("%s pin is not present exactly once in the security-tools Dockerfile", name)
		}
		if slices.Contains([]string{"slither", "halmos"}, name) && (!tool.OCIWritableTarget || tool.OCIPath == "") {
			t.Fatalf("%s requires an ephemeral writable build target and explicit root PATH: %+v", name, tool)
		}
		if name != "schemathesis" && name != "halmos" && tool.ExitCodes[1] != StatusError {
			t.Fatalf("%s exit 1 must be operational error", name)
		}
	}
}

func TestHalmosClosureDigestMatchesReviewedInputs(t *testing.T) {
	deps, err := os.ReadFile("../../security-tool-requirements/halmos-deps.lock")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := os.ReadFile("../../security-tool-requirements/halmos.lock")
	if err != nil {
		t.Fatal(err)
	}
	material := []byte("python@sha256:d29f48a31a8b408ed19272ca1e7b10ebae13b240a27e862d3d4217c528e2e0c3\n")
	material = append(material, deps...)
	material = append(material, pkg...)
	material = append(material, []byte("forge-amd64=sha256:4f77da0810de94325734855d0ad58d70640aa8a5b2a837608ddf8c26da34355c\nforge-arm64=sha256:a93076d85e013a45b7050c21b26cf05627f1d64f40b99cf0524fa5facf4d3988\n")...)
	material = append(material, []byte("slither-index=sha256:65b53faf87985c6b43a98ac0da9158235715cb767bf1fe68e2e3f94ccb281978\n")...)
	const want = "sha256:7ac9f37f8554d8354a7a924eb81393fe30f1bbe851e07c4c35f33a935f53593f"
	if got := sha256Digest(material); got != want {
		t.Fatalf("Halmos closure digest = %s, want %s; update registry and Docker provenance marker after reviewing lock changes", got, want)
	}
	platforms := map[string]struct{ python, forge, solcRoot, want string }{
		"amd64": {"sha256:77923445c077d8eb971b14b2b114a1d9cd4a87edb4c75654820ca4832ee8cb15", "sha256:4f77da0810de94325734855d0ad58d70640aa8a5b2a837608ddf8c26da34355c", "sha256:28ce0f9b27312f6ed1137495aef70744dc2d6ff8e6d5c9147ec9e31a63ff86a8", "sha256:a80b8016e9a409a38d54ff300af5aa37cbb0ae281faaa37afab7fa6a63c87340"},
		"arm64": {"sha256:ecb0ac954790dd64a0d518d699b9c61a91780c42b0d877c802dbaffd04db66f9", "sha256:a93076d85e013a45b7050c21b26cf05627f1d64f40b99cf0524fa5facf4d3988", "sha256:98b90a826a996507e6b1015a7850b2e8de30a3d80f4ec7deaddbf00e050d5152", "sha256:32bb55c125446b2aa95ac8bb3968701ee05b740ea0c06ccdd5b73e081d5bce98"},
	}
	registry, err := NewRegistry(DefaultManifest(sha256Digest([]byte("wrapper")), nil))
	if err != nil {
		t.Fatal(err)
	}
	halmos, _ := registry.Tool("halmos")
	for arch, platform := range platforms {
		platformMaterial := []byte("halmos-0.3.3\narch=" + arch + "\npython=" + platform.python + "\nforge=" + platform.forge + "\nsolc-root=" + platform.solcRoot + "\n")
		platformMaterial = append(platformMaterial, deps...)
		platformMaterial = append(platformMaterial, pkg...)
		if got := sha256Digest(platformMaterial); got != platform.want {
			t.Fatalf("Halmos %s closure digest = %s, want %s", arch, got, platform.want)
		}
		if halmos.PlatformDigests[arch] != platform.want {
			t.Fatalf("registry Halmos %s digest = %s, want closure %s", arch, halmos.PlatformDigests[arch], platform.want)
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
	for _, name := range []string{"nuclei", "naabu", "aderyn", "forge-security-tests", "echidna"} {
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

func TestEVMToolsHaveReviewedExecutionOrPackagingContracts(t *testing.T) {
	registry, err := NewRegistry(DefaultManifest(sha256Digest([]byte("wrapper")), nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"echidna", "slither", "halmos"} {
		tool, ok := registry.Tool(name)
		if !ok || !tool.Enabled {
			t.Fatalf("%s must be executable: %+v", name, tool)
		}
	}
	if halmos, _ := registry.Tool("halmos"); halmos.Adapter != "halmos-json" {
		t.Fatalf("Halmos adapter = %q", halmos.Adapter)
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
		{
			name:   "echidna",
			config: RunConfig{Tool: "echidna", Target: Target{Type: "solidity_project", Locator: "/workspace/project", Revision: "fixture-v1", Digest: sha256Digest([]byte("echidna")), MediaType: "application/vnd.gratefulagents.solidity-project.v1+directory"}, Seed: &seed},
			want:   []string{"echidna", "/workspace/project", "--format", "json", "--seed", "42", "--workers", "1", "--test-limit", "10000", "--seq-len", "32", "--shrink-limit", "5000", "--disable-slither"},
		},
		{
			name:   "slither",
			config: RunConfig{Tool: "slither", Target: Target{Type: "solidity_project", Locator: "/workspace/project", Revision: "fixture-v1", Digest: sha256Digest([]byte("slither")), MediaType: "application/vnd.gratefulagents.solidity-project.v1+directory"}},
			want:   []string{"slither", "/workspace/project", "--solc", "/home/ethsec/.local/bin/solc", "--json", "/work/slither.json"},
		},
		{
			name:   "halmos",
			config: RunConfig{Tool: "halmos", Target: Target{Type: "foundry_project", Locator: "/workspace/project", Revision: "fixture-v1", Digest: sha256Digest([]byte("halmos")), MediaType: "application/vnd.gratefulagents.foundry-security-project.v1+directory"}},
			want:   []string{"halmos", "--root", "/workspace/project", "--solver", "z3", "--loop", "2", "--width", "64", "--depth", "128", "--json-output", "/work/halmos.json"},
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

func TestEVMToolsRejectMissingSeedsAndMismatchedMediaTypes(t *testing.T) {
	registry, err := NewRegistry(DefaultManifest(sha256Digest([]byte("wrapper")), nil))
	if err != nil {
		t.Fatal(err)
	}
	seed := int64(42)
	base := Target{Locator: "/workspace/target", Revision: "fixture-v1", Digest: sha256Digest([]byte("target"))}
	tests := []RunConfig{
		{Tool: "echidna", Target: Target{Type: "solidity_project", Locator: base.Locator, Revision: base.Revision, Digest: base.Digest, MediaType: "application/gzip"}, Seed: &seed},
		{Tool: "echidna", Target: Target{Type: "solidity_project", Locator: base.Locator, Revision: base.Revision, Digest: base.Digest, MediaType: "application/vnd.gratefulagents.solidity-project.v1+directory"}},
	}
	for _, config := range tests {
		if _, _, err := registry.BuildInvocation(config); err == nil {
			t.Errorf("BuildInvocation accepted invalid %s config: %+v", config.Tool, config)
		}
	}
}
