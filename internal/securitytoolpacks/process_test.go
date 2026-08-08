package securitytoolpacks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessSandboxExecutesArgvWithoutShell(t *testing.T) {
	request := ExecutionRequest{
		Invocation: Invocation{Argv: []string{"/usr/bin/printf", "%s", "target; touch /tmp/must-not-exist"}, Budgets: Budgets{Timeout: time.Second, MaxOutputSize: 1024}},
		Config:     RunConfig{Target: Target{Locator: "fixture"}},
	}
	result := (ProcessSandbox{}).Execute(context.Background(), request)
	if result.Err != nil || string(result.Output) != "target; touch /tmp/must-not-exist" {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat("/tmp/must-not-exist"); !os.IsNotExist(err) {
		t.Fatal("argument was interpreted by a shell")
	}
}

func TestProcessSandboxRejectsPATHShadowForLockedTools(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "executed")
	shadow := filepath.Join(directory, "naabu")
	if err := os.WriteFile(shadow, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+":"+os.Getenv("PATH"))
	request := ExecutionRequest{
		Tool:       Tool{Name: "naabu", ToolArtifactDigest: sha256Digest([]byte("not-the-shadow"))},
		Invocation: Invocation{Argv: []string{"naabu", "-version"}, Budgets: Budgets{Timeout: time.Second, MaxOutputSize: 1024}},
		Config:     RunConfig{Target: Target{Locator: "192.0.2.1"}},
	}
	result := (ProcessSandbox{}).Execute(context.Background(), request)
	if result.Err == nil {
		t.Fatal("expected operator-toolkit lookup failure")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("PATH-shadowed tool was executed")
	}
}

func TestProcessSandboxOutputBudgetIsNotPass(t *testing.T) {
	request := ExecutionRequest{
		Invocation: Invocation{Argv: []string{"/usr/bin/printf", "123456789"}, Budgets: Budgets{Timeout: time.Second, MaxOutputSize: 4}},
		Config:     RunConfig{Target: Target{Locator: "fixture"}},
	}
	result := (ProcessSandbox{}).Execute(context.Background(), request)
	if result.Err == nil || string(result.Output) != "1234" {
		t.Fatalf("result=%+v", result)
	}
}

func TestDigestPathIsStableAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	contract := filepath.Join(root, "src", "Vault.sol")
	if err := os.WriteFile(contract, []byte("contract Vault {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, exists, err := DigestPath(root)
	if err != nil || !exists {
		t.Fatalf("digest=%q exists=%t err=%v", first, exists, err)
	}
	second, _, _ := DigestPath(root)
	if first != second {
		t.Fatal("directory digest is not stable")
	}
	if err := os.WriteFile(contract, []byte("contract Vault { function x() external {} }"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, _, _ := DigestPath(root)
	if first == changed {
		t.Fatal("directory mutation did not change digest")
	}
	if err := os.Symlink(contract, filepath.Join(root, "link.sol")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DigestPath(root); err == nil {
		t.Fatal("expected symlink rejection")
	}
	if err := os.Remove(filepath.Join(root, "link.sol")); err != nil {
		t.Fatal(err)
	}
	digest, _, err := DigestPath(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, cleanup, err := snapshotDirectoryTarget(Target{Locator: root, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := os.WriteFile(filepath.Join(snapshot, "generated"), []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "generated")); !os.IsNotExist(err) {
		t.Fatal("tool output mutated original target")
	}
}

func TestResultExitCodesAreDistinct(t *testing.T) {
	seen := map[int]Status{}
	for _, status := range []Status{StatusPass, StatusFindings, StatusError, StatusTimeout, StatusPartial, StatusNotApplicable} {
		code := ResultExitCode(status)
		if previous, exists := seen[code]; exists {
			t.Fatalf("%s and %s share exit code %d", previous, status, code)
		}
		seen[code] = status
	}
}
