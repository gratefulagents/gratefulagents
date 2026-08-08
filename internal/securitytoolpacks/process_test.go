package securitytoolpacks

import (
	"context"
	"os"
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
