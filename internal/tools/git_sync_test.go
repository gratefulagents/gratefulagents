package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func decodeGitSyncOutput(t *testing.T, content string) gitSyncOutput {
	t.Helper()
	var out gitSyncOutput
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		t.Fatalf("result json: %v (%s)", err, content)
	}
	return out
}

func TestGitPullToolMergesRemoteBranch(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse --abbrev-ref HEAD": "feat/thing\n",
		"merge --no-edit -m Merge origin/feat/thing into feat/thing\n\n" + requiredCoAuthorTrailer + " origin/feat/thing": "Merge made by the 'ort' strategy.\n",
		"rev-parse HEAD": "abc123\n",
	}}
	tool := NewGitPullTool(runner)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	wantCalls := []string{
		"rev-parse --abbrev-ref HEAD",
		"fetch origin +refs/heads/feat/thing:refs/remotes/origin/feat/thing",
		"merge --no-edit -m Merge origin/feat/thing into feat/thing\n\n" + requiredCoAuthorTrailer + " origin/feat/thing",
		"rev-parse HEAD",
	}
	if !reflect.DeepEqual(runner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.gitCalls, wantCalls)
	}
	out := decodeGitSyncOutput(t, result.Content)
	if out.Status != "merged" || out.Branch != "feat/thing" || out.MergedFrom != "origin/feat/thing" || out.CommitSHA != "abc123" {
		t.Fatalf("output = %#v", out)
	}
}

func TestGitPullToolIgnoresLegacyDisabledCoAuthorSetting(t *testing.T) {
	t.Setenv("GRATEFULAGENTS_DISABLE_CO_AUTHOR_TRAILER", "true")
	repo := testGitRepoDir(t)
	mergeCall := "merge --no-edit -m Merge origin/feat/thing into feat/thing\n\n" + requiredCoAuthorTrailer + " origin/feat/thing"
	runner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse --abbrev-ref HEAD": "feat/thing\n",
		mergeCall:                     "Merge made by the 'ort' strategy.\n",
		"rev-parse HEAD":              "abc123\n",
	}}

	result, err := NewGitPullTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%+v, %v)", result, err)
	}
	wantCalls := []string{
		"rev-parse --abbrev-ref HEAD",
		"fetch origin +refs/heads/feat/thing:refs/remotes/origin/feat/thing",
		mergeCall,
		"rev-parse HEAD",
	}
	if !reflect.DeepEqual(runner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.gitCalls, wantCalls)
	}
}

func TestGitPullToolReportsUpToDate(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse --abbrev-ref HEAD": "feat/thing\n",
		"merge --no-edit -m Merge origin/feat/thing into feat/thing\n\n" + requiredCoAuthorTrailer + " origin/feat/thing": "Already up to date.\n",
	}}

	result, err := NewGitPullTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := decodeGitSyncOutput(t, result.Content)
	if out.Status != "up_to_date" {
		t.Fatalf("output = %#v, want up_to_date", out)
	}
}

func TestGitPullToolHandlesMissingRemoteBranch(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{
		gitOut: map[string]string{
			"rev-parse --abbrev-ref HEAD":                                        "feat/thing\n",
			"fetch origin +refs/heads/feat/thing:refs/remotes/origin/feat/thing": "fatal: couldn't find remote ref refs/heads/feat/thing\n",
		},
		gitErr: map[string]error{
			"fetch origin +refs/heads/feat/thing:refs/remotes/origin/feat/thing": errors.New("exit status 128"),
		},
	}

	result, err := NewGitPullTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	out := decodeGitSyncOutput(t, result.Content)
	if out.Status != "no_remote_branch" {
		t.Fatalf("output = %#v, want no_remote_branch", out)
	}
	for _, call := range runner.gitCalls {
		if strings.HasPrefix(call, "merge") {
			t.Fatalf("merge must not run, git calls = %#v", runner.gitCalls)
		}
	}
}

func TestGitPullToolReportsConflicts(t *testing.T) {
	repo := testGitRepoDir(t)
	mergeCall := "merge --no-edit -m Merge origin/feat/thing into feat/thing\n\n" + requiredCoAuthorTrailer + " origin/feat/thing"
	runner := &fakeGitRunner{
		gitOut: map[string]string{
			"rev-parse --abbrev-ref HEAD":      "feat/thing\n",
			mergeCall:                          "CONFLICT (content): Merge conflict in a.go\nAutomatic merge failed; fix conflicts and then commit the result.\n",
			"diff --name-only --diff-filter=U": "a.go\nsub/b.go\n",
		},
		gitErr: map[string]error{mergeCall: errors.New("exit status 1")},
	}

	result, err := NewGitPullTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("conflicts should be a structured non-error result: %s", result.Content)
	}
	out := decodeGitSyncOutput(t, result.Content)
	if out.Status != "conflicts" {
		t.Fatalf("output = %#v, want conflicts", out)
	}
	if !reflect.DeepEqual(out.ConflictedFiles, []string{"a.go", "sub/b.go"}) {
		t.Fatalf("conflicted files = %#v", out.ConflictedFiles)
	}
	if !strings.Contains(out.Guidance, "git_commit") || !strings.Contains(out.Guidance, "git_merge_abort") {
		t.Fatalf("guidance = %q", out.Guidance)
	}
	for _, call := range runner.gitCalls {
		if strings.Contains(call, "--abort") {
			t.Fatalf("conflicted merge must stay in progress, git calls = %#v", runner.gitCalls)
		}
	}
}

func TestGitPullToolRefusesDetachedHead(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse --abbrev-ref HEAD": "HEAD\n",
	}}

	result, err := NewGitPullTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "detached") {
		t.Fatalf("result = %+v, want detached HEAD refusal", result)
	}
}

func TestGitMergeToolMergesFetchedBaseBranch(t *testing.T) {
	repo := testGitRepoDir(t)
	mergeCall := "merge --no-edit -m Merge origin/main into feat/thing\n\n" + requiredCoAuthorTrailer + " origin/main"
	runner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse --abbrev-ref HEAD": "feat/thing\n",
		mergeCall:                     "Merge made by the 'ort' strategy.\n",
		"rev-parse HEAD":              "def456\n",
	}}

	result, err := NewGitMergeTool(runner).Execute(context.Background(), json.RawMessage(`{"branch":"origin/main"}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	wantCalls := []string{
		"rev-parse --abbrev-ref HEAD",
		"fetch origin +refs/heads/main:refs/remotes/origin/main",
		mergeCall,
		"rev-parse HEAD",
	}
	if !reflect.DeepEqual(runner.gitCalls, wantCalls) {
		t.Fatalf("git calls = %#v, want %#v", runner.gitCalls, wantCalls)
	}
	out := decodeGitSyncOutput(t, result.Content)
	if out.Status != "merged" || out.MergedFrom != "origin/main" || out.CommitSHA != "def456" {
		t.Fatalf("output = %#v", out)
	}
}

func TestGitMergeToolFallsBackToLocalBranch(t *testing.T) {
	repo := testGitRepoDir(t)
	mergeCall := "merge --no-edit -m Merge topic into feat/thing\n\n" + requiredCoAuthorTrailer + " topic"
	runner := &fakeGitRunner{
		gitOut: map[string]string{
			"rev-parse --abbrev-ref HEAD":                              "feat/thing\n",
			"fetch origin +refs/heads/topic:refs/remotes/origin/topic": "fatal: couldn't find remote ref refs/heads/topic\n",
			"rev-parse -q --verify refs/heads/topic":                   "0123abc\n",
			mergeCall:                                                  "Merge made by the 'ort' strategy.\n",
			"rev-parse HEAD":                                           "fed789\n",
		},
		gitErr: map[string]error{
			"fetch origin +refs/heads/topic:refs/remotes/origin/topic": errors.New("exit status 128"),
		},
	}

	result, err := NewGitMergeTool(runner).Execute(context.Background(), json.RawMessage(`{"branch":"topic"}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	out := decodeGitSyncOutput(t, result.Content)
	if out.Status != "merged" || out.MergedFrom != "topic" {
		t.Fatalf("output = %#v", out)
	}
}

func TestGitMergeToolRejectsUnknownBranch(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{
		gitOut: map[string]string{
			"rev-parse --abbrev-ref HEAD":                              "feat/thing\n",
			"fetch origin +refs/heads/ghost:refs/remotes/origin/ghost": "fatal: couldn't find remote ref refs/heads/ghost\n",
		},
		gitErr: map[string]error{
			"fetch origin +refs/heads/ghost:refs/remotes/origin/ghost": errors.New("exit status 128"),
			"rev-parse -q --verify refs/heads/ghost":                   errors.New("exit status 1"),
		},
	}

	result, err := NewGitMergeTool(runner).Execute(context.Background(), json.RawMessage(`{"branch":"ghost"}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "not found on origin or locally") {
		t.Fatalf("result = %+v", result)
	}
}

func TestGitMergeToolRequiresBranch(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{}
	result, err := NewGitMergeTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "branch is required") {
		t.Fatalf("result = %+v", result)
	}
	if len(runner.gitCalls) != 0 {
		t.Fatalf("expected no git calls, got %#v", runner.gitCalls)
	}
}

func TestGitMergeToolSurfacesDirtyTreeHint(t *testing.T) {
	repo := testGitRepoDir(t)
	mergeCall := "merge --no-edit -m Merge origin/main into feat/thing\n\n" + requiredCoAuthorTrailer + " origin/main"
	runner := &fakeGitRunner{
		gitOut: map[string]string{
			"rev-parse --abbrev-ref HEAD": "feat/thing\n",
			mergeCall:                     "error: Your local changes to the following files would be overwritten by merge:\n\ta.go\n",
		},
		gitErr: map[string]error{mergeCall: errors.New("exit status 1")},
	}

	result, err := NewGitMergeTool(runner).Execute(context.Background(), json.RawMessage(`{"branch":"main"}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "git_commit") {
		t.Fatalf("result = %+v, want dirty-tree hint", result)
	}
}

func TestGitMergeAbortToolAbortsMerge(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{gitOut: map[string]string{
		"rev-parse -q --verify MERGE_HEAD": "abc123\n",
	}}

	result, err := NewGitMergeAbortTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	out := decodeGitSyncOutput(t, result.Content)
	if out.Status != "aborted" || !strings.Contains(out.Output, "merge") {
		t.Fatalf("output = %#v", out)
	}
	found := false
	for _, call := range runner.gitCalls {
		if call == "merge --abort" {
			found = true
		}
	}
	if !found {
		t.Fatalf("git calls = %#v, want merge --abort", runner.gitCalls)
	}
}

func TestGitMergeAbortToolAbortsRebase(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{
		gitOut: map[string]string{
			"rev-parse -q --verify REBASE_HEAD": "abc123\n",
		},
		gitErr: map[string]error{
			"rev-parse -q --verify MERGE_HEAD": errors.New("exit status 1"),
		},
	}

	result, err := NewGitMergeAbortTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	out := decodeGitSyncOutput(t, result.Content)
	if out.Status != "aborted" || !strings.Contains(out.Output, "rebase") {
		t.Fatalf("output = %#v", out)
	}
}

func TestGitMergeAbortToolErrorsWhenNothingInProgress(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{gitErr: map[string]error{
		"rev-parse -q --verify MERGE_HEAD":       errors.New("exit status 1"),
		"rev-parse -q --verify REBASE_HEAD":      errors.New("exit status 1"),
		"rev-parse -q --verify CHERRY_PICK_HEAD": errors.New("exit status 1"),
	}}

	result, err := NewGitMergeAbortTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "no merge, rebase, or cherry-pick in progress") {
		t.Fatalf("result = %+v", result)
	}
}

func TestGitStatusToolParsesPorcelainV2(t *testing.T) {
	repo := testGitRepoDir(t)
	statusRaw := strings.Join([]string{
		"# branch.oid abc123",
		"# branch.head feat/thing",
		"# branch.upstream origin/feat/thing",
		"# branch.ab +2 -1",
		"1 M. N... 100644 100644 100644 abc def staged.go",
		"1 .M N... 100644 100644 100644 abc def unstaged.go",
		"u UU N... 100644 100644 100644 100644 a b c conflicted.go",
		"? new.txt",
		"",
	}, "\n")
	runner := &fakeGitRunner{
		gitOut: map[string]string{
			"status --porcelain=v2 --branch --no-renames": statusRaw,
			"rev-parse -q --verify MERGE_HEAD":            "abc123\n",
		},
	}

	result, err := NewGitStatusTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	var out gitStatusOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("result json: %v", err)
	}
	if out.Branch != "feat/thing" || out.Upstream != "origin/feat/thing" || out.Ahead != 2 || out.Behind != 1 {
		t.Fatalf("output = %#v", out)
	}
	if !reflect.DeepEqual(out.StagedFiles, []string{"staged.go"}) ||
		!reflect.DeepEqual(out.UnstagedFiles, []string{"unstaged.go"}) ||
		!reflect.DeepEqual(out.UntrackedFiles, []string{"new.txt"}) ||
		!reflect.DeepEqual(out.ConflictedFiles, []string{"conflicted.go"}) {
		t.Fatalf("files = %#v", out)
	}
	if out.Clean {
		t.Fatal("clean should be false")
	}
	if out.Operation != "merge" {
		t.Fatalf("operation = %q, want merge", out.Operation)
	}
}

func TestGitStatusToolReportsCleanTree(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{
		gitOut: map[string]string{
			"status --porcelain=v2 --branch --no-renames": "# branch.oid abc\n# branch.head feat/thing\n",
		},
		gitErr: map[string]error{
			"rev-parse -q --verify MERGE_HEAD":       errors.New("exit status 1"),
			"rev-parse -q --verify REBASE_HEAD":      errors.New("exit status 1"),
			"rev-parse -q --verify CHERRY_PICK_HEAD": errors.New("exit status 1"),
		},
	}

	result, err := NewGitStatusTool(runner).Execute(context.Background(), json.RawMessage(`{}`), repo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var out gitStatusOutput
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("result json: %v", err)
	}
	if !out.Clean || out.Operation != "" || out.Branch != "feat/thing" {
		t.Fatalf("output = %#v", out)
	}
}

func TestGitSyncToolsRejectRepoPathEscapingWorkspace(t *testing.T) {
	repo := testGitRepoDir(t)
	runner := &fakeGitRunner{}
	tools := []Tool{
		NewGitStatusTool(runner),
		NewGitPullTool(runner),
		NewGitMergeAbortTool(runner),
	}
	for _, tool := range tools {
		result, err := tool.Execute(context.Background(), json.RawMessage(`{"repo_path":"../outside"}`), repo)
		if err != nil {
			t.Fatalf("%s Execute() error = %v", tool.Name(), err)
		}
		if !result.IsError || !strings.Contains(result.Content, "repo_path rejected") {
			t.Fatalf("%s result = %+v", tool.Name(), result)
		}
	}
	if len(runner.gitCalls) != 0 {
		t.Fatalf("expected no git calls, got %#v", runner.gitCalls)
	}
}

func TestRegisterGitSyncToolsHonorsReadOnlyClamp(t *testing.T) {
	registry := NewRegistry(t.TempDir(), WithReadOnlyTools())
	RegisterGitSyncTools(registry)
	for _, name := range []string{"git_pull", "git_merge", "git_merge_abort"} {
		if registry.Get(name) != nil {
			t.Fatalf("expected %s to be filtered from read-only registry", name)
		}
	}
	if registry.Get("git_status") == nil {
		t.Fatal("git_status is read-only and must stay registered")
	}
}

func TestRegisterGitSyncToolsRegistersAll(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	RegisterGitSyncTools(registry)
	for _, name := range []string{"git_status", "git_pull", "git_merge", "git_merge_abort"} {
		if registry.Get(name) == nil {
			t.Fatalf("expected %s to be registered", name)
		}
	}
}
