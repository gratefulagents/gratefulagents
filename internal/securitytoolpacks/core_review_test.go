package securitytoolpacks

import (
	"encoding/json"
	"os"
	"runtime"
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
	_, _, err = registry.BuildInvocation(RunConfig{Tool: "nmap", Target: Target{Type: "address_scope", Locator: "192.0.2.0/24", Revision: "v1", Digest: pin}, Scope: []string{"192.0.2.0/24"}})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err=%v", err)
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
