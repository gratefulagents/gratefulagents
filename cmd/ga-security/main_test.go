package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
)

func TestPersistPreservesLogicalArtifactName(t *testing.T) {
	dir := t.TempDir()
	data := []byte("corpus")
	if err := persist(dir, securitytoolpacks.Result{Artifacts: []securitytoolpacks.Artifact{{Name: "cargo-fuzz-corpus/abc", MediaType: "application/octet-stream", Data: data}}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result securitytoolpacks.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Name != "cargo-fuzz-corpus/abc" || result.Artifacts[0].StorageName != "raw-00" {
		t.Fatalf("artifact identity was not preserved: %+v", result.Artifacts)
	}
}

func TestRunAuthorizationFixtureEndToEnd(t *testing.T) {
	fixture := filepath.Join("..", "..", "test", "fixtures", "security-toolpacks", "web", "authorization-matrix.json")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	config := securitytoolpacks.RunConfig{
		Tool: "authorization-matrix",
		Target: securitytoolpacks.Target{
			Type: "authorization_matrix", Locator: fixture, Revision: "fixture-v1",
			Digest: "sha256:" + hex.EncodeToString(sum[:]),
		},
		Scope: []string{"http://fixture.invalid"},
	}
	configData, _ := json.Marshal(config)
	temp := t.TempDir()
	configPath := filepath.Join(temp, "config.json")
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--config", configPath, "--output", filepath.Join(temp, "out")}); code != 10 {
		t.Fatalf("exit=%d, want findings (10)", code)
	}
	resultData, err := os.ReadFile(filepath.Join(temp, "out", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result securitytoolpacks.Result
	if err := json.Unmarshal(resultData, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != securitytoolpacks.StatusFindings || len(result.Findings) != 1 || len(result.Artifacts) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(filepath.Join(temp, "out", "raw-00")); err != nil {
		t.Fatal(err)
	}
}
