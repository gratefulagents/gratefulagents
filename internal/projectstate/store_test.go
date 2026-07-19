package projectstate

import (
	"strings"
	"testing"
	"time"

	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
)

func TestNewStoreValidation(t *testing.T) {
	if _, err := NewStore(Options{}); err == nil {
		t.Fatal("NewStore() with no pool should error")
	}
}

func TestSanitizeProjectID(t *testing.T) {
	tests := []struct {
		in     string
		expect string
	}{
		{"team-a-https://github.com/acme/widgets.git", "team-a-https-github-com-acme-widgets-git"},
		{"  Mixed CASE  ", "mixed-case"},
		{"___", ""},
	}
	for _, tt := range tests {
		if got := SanitizeProjectID(tt.in); got != tt.expect {
			t.Errorf("SanitizeProjectID(%q) = %q, want %q", tt.in, got, tt.expect)
		}
	}
}

func TestProjectIDCanonicalEquivalenceAndCollisionResistance(t *testing.T) {
	canonical := ProjectID("Team-A", "https://github.com/Acme/Widgets.git")
	for _, repository := range []string{
		"https://github.com/acme/widgets.git/",
		"git@github.com:acme/widgets.git",
		"ssh://git@github.com/acme/widgets/",
	} {
		if got := ProjectID("team-a", repository); got != canonical {
			t.Errorf("ProjectID(%q) = %q, want %q", repository, got, canonical)
		}
	}
	if got := ProjectID("team-a", ""); got != "team-a-chat" {
		t.Errorf("repoless ProjectID = %q, want team-a-chat", got)
	}
	if first, second := ProjectID("team-a", "example.com/a/b"), ProjectID("team-a", "example.com/a-b"); first == second {
		t.Fatalf("sanitization collision produced identical ID %q", first)
	}
}

func TestApplyPatchMirrorsEngineSemantics(t *testing.T) {
	now := time.Now().UTC()
	task := sdkprojectstate.Task{
		ID:     "task_abc",
		Title:  "old",
		Status: sdkprojectstate.TaskStatusOpen,
		Labels: []string{"keep"},
	}

	title := "  new title  "
	status := "done"
	priority := 9
	applyPatch(&task, sdkprojectstate.TaskPatch{
		Title:    &title,
		Status:   &status,
		Priority: &priority,
	}, now)

	if task.Title != "new title" {
		t.Errorf("Title = %q, want trimmed", task.Title)
	}
	if task.Status != sdkprojectstate.TaskStatusClosed {
		t.Errorf("Status = %q, want closed (done normalizes)", task.Status)
	}
	if task.ClosedAt == nil || !task.ClosedAt.Equal(now) {
		t.Error("ClosedAt should be set when patched to closed")
	}
	if task.Priority != 4 {
		t.Errorf("Priority = %d, want clamped to 4", task.Priority)
	}

	reopen := "open"
	applyPatch(&task, sdkprojectstate.TaskPatch{Status: &reopen}, now.Add(time.Second))
	if task.ClosedAt != nil {
		t.Error("ClosedAt should clear when reopened")
	}
}

func TestHasOpenBlockerAndBlockedTasks(t *testing.T) {
	dep := sdkprojectstate.Task{ID: "task_dep", Status: sdkprojectstate.TaskStatusOpen}
	blocked := sdkprojectstate.Task{ID: "task_blocked", Status: sdkprojectstate.TaskStatusOpen, DependsOn: []string{"task_dep"}}
	free := sdkprojectstate.Task{ID: "task_free", Status: sdkprojectstate.TaskStatusOpen}
	byID := map[string]sdkprojectstate.Task{dep.ID: dep, blocked.ID: blocked, free.ID: free}

	if hasOpenBlocker(byID, free) {
		t.Error("free task should have no blocker")
	}
	if !hasOpenBlocker(byID, blocked) {
		t.Error("blocked task should report an open blocker")
	}
	// Missing dependency also blocks (fail closed, mirrors engine).
	if !hasOpenBlocker(byID, sdkprojectstate.Task{DependsOn: []string{"missing"}}) {
		t.Error("missing dependency should block")
	}

	out := blockedTasks([]sdkprojectstate.Task{dep, blocked, free}, byID, 5)
	if len(out) != 1 || out[0].ID != "task_blocked" {
		t.Fatalf("blockedTasks() = %#v, want [task_blocked]", out)
	}
}

func TestSplitMemoriesForPrime(t *testing.T) {
	now := time.Now().UTC()
	memories := []sdkprojectstate.Memory{
		{ID: "m1", Kind: sdkprojectstate.MemoryKindPinned, UpdatedAt: now},
		{ID: "m2", Kind: sdkprojectstate.MemoryKindSemantic, UpdatedAt: now.Add(time.Minute)},
		{ID: "m3", Kind: sdkprojectstate.MemoryKindSemantic, UpdatedAt: now},
	}
	pinned, recent := splitMemoriesForPrime(memories, 2)
	if len(pinned) != 1 || pinned[0].ID != "m1" {
		t.Fatalf("pinned = %#v, want [m1]", pinned)
	}
	if len(recent) != 1 || recent[0].ID != "m2" {
		t.Fatalf("recent = %#v, want most recent [m2]", recent)
	}
}

func TestMemoryMatchesQueryAndNormalization(t *testing.T) {
	mem := sdkprojectstate.Memory{Content: "Build uses make web", Tags: []string{"build"}}
	if !memoryMatchesQuery(mem, "make") {
		t.Error("expected lexical term match")
	}
	if memoryMatchesQuery(mem, "unrelated") {
		t.Error("unexpected match")
	}

	if got := normalizeTaskType("FEAT"); got != sdkprojectstate.TaskTypeFeature {
		t.Errorf("normalizeTaskType(FEAT) = %q", got)
	}
	if got := normalizeMemoryKind("weird"); got != sdkprojectstate.MemoryKindSemantic {
		t.Errorf("normalizeMemoryKind(weird) = %q", got)
	}
	if got := normalizeMemoryScope("USER"); got != sdkprojectstate.MemoryScopeUser {
		t.Errorf("normalizeMemoryScope(USER) = %q", got)
	}
}

func TestNewIDShape(t *testing.T) {
	id := newID("task")
	if !strings.HasPrefix(id, "task_") || len(id) != len("task_")+12 {
		t.Fatalf("newID() = %q, want task_ prefix with 12 hex chars", id)
	}
}
