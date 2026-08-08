package securitytoolpacks

import (
	"encoding/json"
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
