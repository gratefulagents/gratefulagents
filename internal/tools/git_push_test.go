package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGitPushToolPushesWorkBranch(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse --abbrev-ref HEAD":                   "feat/thing\n",
		"symbolic-ref --short refs/remotes/origin/HEAD": "origin/main\n",
	}}
	tool := NewGitPushTool(runner)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	wantCalls := []string{
		"rev-parse --abbrev-ref HEAD",
		"symbolic-ref --short refs/remotes/origin/HEAD",
		"push --no-verify -u origin HEAD",
	}
	if !reflect.DeepEqual(runner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.gitCalls, wantCalls)
	}
	var out gitPushOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("result json: %v", err)
	}
	if out.Status != "pushed" || out.Branch != "feat/thing" {
		t.Fatalf("output = %#v", out)
	}
}

func TestGitPushToolHonorsRepoPath(t *testing.T) {
	workspace := testGitRepoDir(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "lib", ".git"), 0o755); err != nil {
		t.Fatalf("mkdir attached repo: %v", err)
	}
	attached, err := resolveLocalGitRepositoryWorkDir(workspace, "repos/lib")
	if err != nil {
		t.Fatalf("resolve attached repo: %v", err)
	}
	runner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse --abbrev-ref HEAD": "feat/thing\n",
	}}
	tool := NewGitPushTool(runner)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"repo_path":"repos/lib"}`), workspace)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	for i, dir := range runner.gitDirs {
		if dir != attached {
			t.Fatalf("git call %d ran in %q, want %q", i, dir, attached)
		}
	}
}

func TestGitPushToolRejectsRepoPathEscapingWorkspace(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{}
	result, err := NewGitPushTool(runner).Execute(context.Background(), json.RawMessage(`{"repo_path":"../outside"}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "repo_path rejected") {
		t.Fatalf("result = %+v", result)
	}
	if len(runner.gitCalls) != 0 {
		t.Fatalf("expected no git calls, got %#v", runner.gitCalls)
	}
}

func TestGitPushToolRefusesProtectedBranches(t *testing.T) {
	tests := []struct {
		name    string
		gitOut  map[string]string
		gitErr  map[string]error
		wantMsg string
	}{
		{
			name:    "main",
			gitOut:  map[string]string{"rev-parse --abbrev-ref HEAD": "main\n"},
			wantMsg: `refusing to push protected branch "main"`,
		},
		{
			name:    "master",
			gitOut:  map[string]string{"rev-parse --abbrev-ref HEAD": "master\n"},
			wantMsg: `refusing to push protected branch "master"`,
		},
		{
			name: "remote default",
			gitOut: map[string]string{
				"rev-parse --abbrev-ref HEAD":                   "develop\n",
				"symbolic-ref --short refs/remotes/origin/HEAD": "origin/develop\n",
			},
			wantMsg: `refusing to push default branch "develop"`,
		},
		{
			name:    "detached HEAD",
			gitOut:  map[string]string{"rev-parse --abbrev-ref HEAD": "HEAD\n"},
			wantMsg: "HEAD is detached",
		},
		{
			name:    "undeterminable branch",
			gitErr:  map[string]error{"rev-parse --abbrev-ref HEAD": errors.New("boom")},
			wantMsg: "cannot determine current branch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testGitRepoDir(t)
			runner := &fakeGitRunner{gitOut: tt.gitOut, gitErr: tt.gitErr}
			result, err := NewGitPushTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if !result.IsError {
				t.Fatalf("result = %+v, want error", result)
			}
			var out gitPushOutput
			if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
				t.Fatalf("result json: %v", err)
			}
			if !strings.Contains(out.Error, tt.wantMsg) {
				t.Fatalf("error = %q, want substring %q", out.Error, tt.wantMsg)
			}
			for _, call := range runner.gitCalls {
				if strings.HasPrefix(call, "push") {
					t.Fatalf("push must not run, git calls = %#v", runner.gitCalls)
				}
			}
		})
	}
}

func TestBranchGuardedGitRunnerBlocksPushOnProtectedBranch(t *testing.T) {
	inner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse --abbrev-ref HEAD": "main\n",
	}}
	guarded := newBranchGuardedGitRunner(inner)

	if _, err := guarded.RunGit(context.Background(), "/repo", "push", "-u", "origin", "HEAD"); err == nil || !strings.Contains(err.Error(), "refusing to push protected branch") {
		t.Fatalf("err = %v, want protected branch refusal", err)
	}
	for _, call := range inner.gitCalls {
		if strings.HasPrefix(call, "push") {
			t.Fatalf("push must not reach inner runner, git calls = %#v", inner.gitCalls)
		}
	}
}

func TestBranchGuardedGitRunnerAllowsWorkBranchPushAndNonPushCommands(t *testing.T) {
	inner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse --abbrev-ref HEAD": "feat/thing\n",
	}}
	guarded := newBranchGuardedGitRunner(inner)

	if _, err := guarded.RunGit(context.Background(), "/repo", "status"); err != nil {
		t.Fatalf("status err = %v", err)
	}
	if _, err := guarded.RunGit(context.Background(), "/repo", "push", "-u", "origin", "HEAD"); err != nil {
		t.Fatalf("push err = %v", err)
	}
	wantCalls := []string{
		"status",
		"rev-parse --abbrev-ref HEAD",
		"symbolic-ref --short refs/remotes/origin/HEAD",
		"push -u origin HEAD",
	}
	if !reflect.DeepEqual(inner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", inner.gitCalls, wantCalls)
	}
}
