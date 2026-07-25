package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	internaltools "github.com/gratefulagents/gratefulagents/internal/tools"
	"github.com/gratefulagents/sdk/pkg/agentsdk/sandbox"
	sdkbrowser "github.com/gratefulagents/sdk/pkg/agentsdk/tools/browser"
)

// A failed model call (provider outage, rate limit) must produce a notice
// that carries the underlying error and tells the user the session survived
// and how to retry — this is what keeps LLM failures from reading as a
// crashed run.
func TestTurnFailureNotice(t *testing.T) {
	err := errors.New("model call failed on turn 0: API request failed")
	notice := turnFailureNotice(3, err)

	for _, want := range []string{
		"Turn 3 failed",
		"model call failed on turn 0: API request failed",
		"send a message to try again",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("turnFailureNotice() = %q, want it to contain %q", notice, want)
		}
	}
}

// A durable stop applies to the in-flight prompt, but an actually newer user
// message is an explicit request to resume after that stop.
func TestInterruptAppliesOnlyUntilNewerUserMessage(t *testing.T) {
	requestedAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	req := &sessionclient.InterruptRequest{RequestedAt: requestedAt}

	if !interruptAppliesToMessage(req, requestedAt.Add(-time.Second)) {
		t.Fatal("stop requested after a prompt must apply to that prompt")
	}
	if !interruptAppliesToMessage(req, requestedAt) {
		t.Fatal("stop requested at prompt time must apply")
	}
	if interruptAppliesToMessage(req, requestedAt.Add(time.Second)) {
		t.Fatal("newer user message must resume past an older stop")
	}
	if interruptAppliesToMessage(nil, requestedAt) {
		t.Fatal("nil stop request must not apply")
	}
}

// A turn that exhausts its budget parks the session with the transcript
// preserved — the notice must say so explicitly.
func TestTurnBudgetNotice(t *testing.T) {
	notice := turnBudgetNotice(2, 100)

	for _, want := range []string{
		"Turn 2",
		"100-turn budget",
		"progress is preserved",
		"pick up exactly where it left off",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("turnBudgetNotice() = %q, want it to contain %q", notice, want)
		}
	}
}

func TestShouldPublishStartupIdle(t *testing.T) {
	emptySession := &store.Session{}
	tests := []struct {
		name       string
		session    *store.Session
		sessionErr error
		messages   []sessionclient.UserMessage
		queueErr   error
		want       bool
	}{
		{name: "empty session", session: emptySession, want: true},
		{
			name:     "seeded kickoff is pending",
			session:  emptySession,
			messages: []sessionclient.UserMessage{{Mode: sessionclient.UserMessageModeEnqueue}},
			want:     false,
		},
		{
			name:     "ordinary user message is pending",
			session:  emptySession,
			messages: []sessionclient.UserMessage{{}},
			want:     false,
		},
		{name: "question is pending", session: &store.Session{PendingInputType: "question", PendingRequestID: "request-1"}, want: false},
		{name: "approval is pending", session: &store.Session{PendingInputType: "approval", PendingRequestID: "request-2"}, want: false},
		{name: "request ID is pending without type", session: &store.Session{PendingRequestID: "request-3"}, want: false},
		{name: "legacy question is pending", session: &store.Session{PendingQuestion: "Proceed?"}, want: false},
		{name: "legacy actions are pending", session: &store.Session{PendingActions: []byte(`[{"id":"approve"}]`)}, want: false},
		{name: "session state is unavailable", sessionErr: errors.New("session unavailable"), want: false},
		{name: "session state is missing", want: false},
		{name: "queue state is unknown", session: emptySession, queueErr: errors.New("queue unavailable"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPublishStartupIdle(tt.session, tt.sessionErr, tt.messages, tt.queueErr); got != tt.want {
				t.Fatalf("shouldPublishStartupIdle() = %v, want %v", got, tt.want)
			}
		})
	}
}

type screenshotExecutor struct {
	data []byte
}

func (e *screenshotExecutor) Build(context.Context, sandbox.Request) (*exec.Cmd, error) {
	return nil, nil
}

func (e *screenshotExecutor) Run(_ context.Context, req sandbox.Request) (sandbox.Result, error) {
	for _, arg := range req.Argv {
		if path, ok := strings.CutPrefix(arg, "--screenshot="); ok {
			if err := os.WriteFile(path, e.data, 0o600); err != nil {
				return sandbox.Result{ExitCode: -1}, err
			}
		}
	}
	return sandbox.Result{}, nil
}

func TestBrowserRegistryUsesWorkspaceScratchAndKeepsExplicitOutputInWorkspace(t *testing.T) {
	workDir := t.TempDir()
	registry := internaltools.NewRegistry(workDir, browserRegistryOptions()...)
	browserTool, ok := registry.Get("Browser").(*sdkbrowser.Tool)
	if !ok {
		t.Fatalf("Browser tool = %T, want *browser.Tool", registry.Get("Browser"))
	}
	if browserTool.ScreenshotDir != workspaceScratchDir {
		t.Fatalf("Browser ScreenshotDir = %q, want %q", browserTool.ScreenshotDir, workspaceScratchDir)
	}

	binDir := t.TempDir()
	chromium := filepath.Join(binDir, "chromium")
	if err := os.WriteFile(chromium, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake Chromium: %v", err)
	}
	t.Setenv("PATH", binDir)
	browserTool.Executor = &screenshotExecutor{data: []byte("png-data")}

	result, err := browserTool.Execute(
		context.Background(),
		json.RawMessage(`{"action":"screenshot","url":"http://127.0.0.1","output_path":"artifacts/browser.png"}`),
		workDir,
	)
	if err != nil || result.IsError {
		t.Fatalf("Browser.Execute() result=%#v err=%v", result, err)
	}
	got, err := os.ReadFile(filepath.Join(workDir, "artifacts", "browser.png"))
	if err != nil {
		t.Fatalf("read explicit workspace screenshot: %v", err)
	}
	if string(got) != "png-data" {
		t.Fatalf("explicit workspace screenshot = %q, want png-data", got)
	}
	if !strings.Contains(result.Content, filepath.Join("artifacts", "browser.png")) {
		t.Fatalf("Browser result = %q, want workspace-relative artifact path", result.Content)
	}
}

func TestBrowserToolsEnabledRequiresChromiumAndSupportsOptOut(t *testing.T) {
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	t.Setenv("ENABLE_BROWSER_TOOLS", "")
	if browserToolsEnabled() {
		t.Fatal("browser tools should be unavailable when the runtime image has no Chromium executable")
	}
	chromium := filepath.Join(binDir, "chromium")
	if err := os.WriteFile(chromium, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake Chromium: %v", err)
	}
	if !browserToolsEnabled() {
		t.Fatal("browser tools should default on when Chromium is available")
	}
	t.Setenv("ENABLE_BROWSER_TOOLS", "false")
	if browserToolsEnabled() {
		t.Fatal("browser tools should be disabled by ENABLE_BROWSER_TOOLS=false")
	}
}
