package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGitCommitToolStagesPathsAndAddsCoAuthor(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse HEAD": "abc123\n",
	}}
	tool := NewGitCommitTool(runner)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{
		"message": "feat: add tool",
		"paths": ["internal/tools/git_commit.go", "internal/tools/git_commit_test.go"],
		"no_verify": true
	}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}

	wantCalls := []string{
		"add -- internal/tools/git_commit.go internal/tools/git_commit_test.go",
		"commit --no-verify -m feat: add tool\n\n" + requiredCoAuthorTrailer,
		"rev-parse HEAD",
	}
	if !reflect.DeepEqual(runner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.gitCalls, wantCalls)
	}

	var out gitCommitOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("result json: %v", err)
	}
	if out.Status != "committed" || out.CommitSHA != "abc123" || !out.CoAuthorAdded {
		t.Fatalf("output = %#v", out)
	}
}

func TestGitCommitToolIgnoresLegacyDisabledCoAuthorSetting(t *testing.T) {
	t.Setenv("GRATEFULAGENTS_DISABLE_CO_AUTHOR_TRAILER", "true")
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{gitOut: map[string]string{"rev-parse HEAD": "abc123\n"}}

	result, err := NewGitCommitTool(runner).Execute(context.Background(), json.RawMessage(`{"message":"feat: mandatory credit"}`), repo)
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%+v, %v)", result, err)
	}
	wantCalls := []string{"commit -m feat: mandatory credit\n\n" + requiredCoAuthorTrailer, "rev-parse HEAD"}
	if !reflect.DeepEqual(runner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.gitCalls, wantCalls)
	}
	var out gitCommitOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("result json: %v", err)
	}
	if !out.CoAuthorAdded {
		t.Fatal("CoAuthorAdded = false, want mandatory credit")
	}
}

func TestGitCommitToolDoesNotDuplicateCoAuthor(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse HEAD": "abc123\n",
	}}
	tool := NewGitCommitTool(runner)
	message := "fix: already signed\n\n" + requiredCoAuthorTrailer

	result, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"message": message,
		"all":     true,
	}), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}

	wantCalls := []string{
		"add -A",
		"commit -m " + message,
		"rev-parse HEAD",
	}
	if !reflect.DeepEqual(runner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.gitCalls, wantCalls)
	}

	var out gitCommitOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("result json: %v", err)
	}
	if out.CoAuthorAdded {
		t.Fatalf("CoAuthorAdded = true, want false")
	}
}

func TestGitCommitToolRejectsRepoPathEscape(t *testing.T) {
	workspace := t.TempDir()
	if result, err := NewGitCommitTool(&fakeGitRunner{}).Execute(context.Background(), json.RawMessage(`{
		"message": "feat: nope",
		"repo_path": "../outside"
	}`), workspace); err != nil {
		t.Fatalf("Execute() error = %v", err)
	} else if !result.IsError || !strings.Contains(result.Content, "outside the workspace") {
		t.Fatalf("result = %+v", result)
	}
}

func TestCoAuthoringGitRunnerAddsTrailerToCommitArgs(t *testing.T) {
	runner := &fakeGitRunner{}
	wrapped := newCoAuthoringGitRunner(runner)

	if _, err := wrapped.RunGit(context.Background(), "/repo", "commit", "--no-verify", "-m", "feat: add"); err != nil {
		t.Fatalf("RunGit() error = %v", err)
	}

	wantCalls := []string{"commit --no-verify -m feat: add -m " + requiredCoAuthorTrailer}
	if !reflect.DeepEqual(runner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.gitCalls, wantCalls)
	}
}

func TestCoAuthoringGitRunnerIgnoresLegacyDisabledSetting(t *testing.T) {
	t.Setenv("GRATEFULAGENTS_DISABLE_CO_AUTHOR_TRAILER", "true")
	runner := &fakeGitRunner{}
	wrapped := newCoAuthoringGitRunner(runner)

	if _, err := wrapped.RunGit(context.Background(), "/repo", "commit", "-m", "feat: add"); err != nil {
		t.Fatalf("RunGit() error = %v", err)
	}
	wantCalls := []string{"commit -m feat: add -m " + requiredCoAuthorTrailer}
	if !reflect.DeepEqual(runner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.gitCalls, wantCalls)
	}
}

func TestCoAuthoringGitRunnerLeavesExistingTrailer(t *testing.T) {
	runner := &fakeGitRunner{}
	wrapped := newCoAuthoringGitRunner(runner)

	if _, err := wrapped.RunGit(context.Background(), "/repo", "commit", "-m", "feat: add", "-m", requiredCoAuthorTrailer); err != nil {
		t.Fatalf("RunGit() error = %v", err)
	}

	wantCalls := []string{"commit -m feat: add -m " + requiredCoAuthorTrailer}
	if !reflect.DeepEqual(runner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.gitCalls, wantCalls)
	}
}

func TestRegisterGitCommitToolHonorsReadOnlyClamp(t *testing.T) {
	registry := NewRegistry(t.TempDir(), WithReadOnlyTools())
	RegisterGitCommitTool(registry)
	if registry.Get("git_commit") != nil {
		t.Fatal("expected git_commit to be filtered from read-only registry")
	}
}

type fakeGitRunner struct {
	gitOut   map[string]string
	gitErr   map[string]error
	gitCalls []string
	gitDirs  []string
	ghCalls  []string
}

func (r *fakeGitRunner) RunGit(_ context.Context, workDir string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	r.gitCalls = append(r.gitCalls, key)
	r.gitDirs = append(r.gitDirs, workDir)
	if r.gitErr != nil {
		if err := r.gitErr[key]; err != nil {
			return r.gitOut[key], err
		}
	}
	if r.gitOut != nil {
		return r.gitOut[key], nil
	}
	return "", nil
}

func (r *fakeGitRunner) RunGH(_ context.Context, _ string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	r.ghCalls = append(r.ghCalls, key)
	if r.gitErr != nil {
		if err := r.gitErr[key]; err != nil {
			return "", err
		}
	}
	return "", nil
}

func testGitRepoDir(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return repo
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return b
}
