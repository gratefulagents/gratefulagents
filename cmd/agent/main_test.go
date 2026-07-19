package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunCLIAcceptsRun(t *testing.T) {
	prev := runEntry
	t.Cleanup(func() { runEntry = prev })
	runEntry = func() error { return nil }

	var stderr bytes.Buffer
	if code := runCLI([]string{"agent", "run"}, &stderr); code != 0 {
		t.Fatalf("runCLI(run) code = %d, want 0 (stderr=%q)", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunCLIAcceptsSlack(t *testing.T) {
	prev := slackEntry
	t.Cleanup(func() { slackEntry = prev })
	slackEntry = func() error { return nil }

	var stderr bytes.Buffer
	if code := runCLI([]string{"agent", "slack"}, &stderr); code != 0 {
		t.Fatalf("runCLI(slack) code = %d, want 0 (stderr=%q)", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunCLIPropagatesSlackFailure(t *testing.T) {
	prev := slackEntry
	t.Cleanup(func() { slackEntry = prev })
	slackEntry = func() error { return errors.New("boom") }

	var stderr bytes.Buffer
	if code := runCLI([]string{"agent", "slack"}, &stderr); code == 0 {
		t.Fatal("runCLI(slack) code = 0, want non-zero on connector failure")
	}
}

func TestRunCLIRejectsLegacyPlan(t *testing.T) {
	var stderr bytes.Buffer
	if code := runCLI([]string{"agent", "plan"}, &stderr); code == 0 {
		t.Fatal("runCLI(plan) code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "Usage: agent") {
		t.Fatalf("stderr = %q, want usage hint", stderr.String())
	}
}

func TestRunCLIRejectsLegacyExecute(t *testing.T) {
	var stderr bytes.Buffer
	if code := runCLI([]string{"agent", "execute"}, &stderr); code == 0 {
		t.Fatal("runCLI(execute) code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "Usage: agent") {
		t.Fatalf("stderr = %q, want usage hint", stderr.String())
	}
}

func TestRunCLIPropagatesRunnerFailure(t *testing.T) {
	prev := runEntry
	t.Cleanup(func() { runEntry = prev })
	runEntry = func() error { return errors.New("boom") }

	var stderr bytes.Buffer
	if code := runCLI([]string{"agent", "run"}, &stderr); code == 0 {
		t.Fatal("runCLI(run) code = 0, want non-zero on runner failure")
	}
}
