package projectstate

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	agentsdk "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
	sdkprojectstatetools "github.com/gratefulagents/sdk/pkg/agentsdk/tools/projectstate"
	"github.com/jackc/pgx/v5"
)

// pgTestSchema mirrors the post-migration-064 shape of the project state
// tables as session-local TEMP tables, which shadow any public tables of the
// same name. Every test runs inside one transaction that is rolled back, so
// the suite is safe to point at any database with the vector extension.
const pgTestSchema = `
CREATE TEMP TABLE project_state_tasks (
    project_id  TEXT NOT NULL,
    id          TEXT NOT NULL,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL DEFAULT 'task',
    status      TEXT NOT NULL DEFAULT 'open',
    priority    INT  NOT NULL DEFAULT 2,
    assignee    TEXT NOT NULL DEFAULT '',
    depends_on  TEXT[] NOT NULL DEFAULT '{}',
    labels      TEXT[] NOT NULL DEFAULT '{}',
    comments    JSONB NOT NULL DEFAULT '[]',
    source_run  TEXT NOT NULL DEFAULT '',
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at   TIMESTAMPTZ,
    PRIMARY KEY (project_id, id)
);
CREATE TEMP TABLE project_state_memories (
    project_id   TEXT NOT NULL,
    id           TEXT NOT NULL,
    kind         TEXT NOT NULL DEFAULT 'fact',
    source_run   TEXT NOT NULL DEFAULT '',
    embedding    vector(1536),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    title        TEXT NOT NULL DEFAULT '',
    body         TEXT NOT NULL DEFAULT '',
    citations    JSONB NOT NULL DEFAULT '[]',
    commit_sha   TEXT NOT NULL DEFAULT '',
    verified_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    use_count    INT NOT NULL DEFAULT 0,
    last_used_at TIMESTAMPTZ,
    search_tsv   tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(body, '')), 'B')
    ) STORED,
    PRIMARY KEY (project_id, id)
);
`

func newPGTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
		_ = conn.Close(context.Background())
	})
	if _, err := tx.Exec(ctx, pgTestSchema); err != nil {
		t.Fatalf("creating temp schema: %v", err)
	}
	store, err := newStore(tx, Options{ProjectID: "test-project", Actor: "run-a", RunID: "run-a"})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestPGMemoryLifecycleAndSearch(t *testing.T) {
	store := newPGTestStore(t)
	ctx := context.Background()

	compaction, err := store.SaveMemory(ctx, sdkprojectstate.SaveMemoryInput{
		Kind:      "semantic", // legacy kind normalizes to fact
		Title:     "Compaction threshold comes from models.dev",
		Body:      "Context compaction triggers are resolved per model from the models.dev catalog before static defaults.",
		Citations: []sdkprojectstate.Citation{{Path: "cmd/agent/project_state.go"}},
		CommitSHA: "abc1234",
	})
	if err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}
	if compaction.Kind != sdkprojectstate.MemoryKindFact || compaction.SourceRun != "run-a" || len(compaction.Citations) != 1 {
		t.Fatalf("saved memory = %+v", compaction)
	}
	pref, err := store.SaveMemory(ctx, sdkprojectstate.SaveMemoryInput{
		Kind: "preference", Title: "Assessments go in chat, not docs PRs",
		Body: "The repo owner wants review write-ups delivered in chat; engineering effort goes into fixes.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveMemory(ctx, sdkprojectstate.SaveMemoryInput{
		Kind: "procedure", Title: "Type-check Tauri Rust on Linux",
		Body: "Install the aarch64-apple-darwin target and run cargo check with a stub C compiler.",
	}); err != nil {
		t.Fatal(err)
	}

	// Stemmed full-text recall: "compacting thresholds" matches "Compaction threshold".
	hits, err := store.SearchMemories(ctx, sdkprojectstate.MemoryQuery{Query: "how are compacting thresholds chosen"})
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(hits) == 0 || hits[0].ID != compaction.ID || hits[0].Score <= 0 {
		t.Fatalf("search hits = %+v, want %s first", hits, compaction.ID)
	}
	if hits, err := store.SearchMemories(ctx, sdkprojectstate.MemoryQuery{Query: "kubernetes ingress certificates"}); err != nil || len(hits) != 0 {
		t.Fatalf("unrelated query hits = %+v, %v; want none", hits, err)
	}
	if hits, err := store.SearchMemories(ctx, sdkprojectstate.MemoryQuery{Query: "compaction chat", Kinds: []string{"preference"}}); err != nil || len(hits) != 1 || hits[0].ID != pref.ID {
		t.Fatalf("kind-filtered hits = %+v, %v; want only %s", hits, err, pref.ID)
	}
	if _, err := store.SearchMemories(ctx, sdkprojectstate.MemoryQuery{Query: "  "}); err == nil {
		t.Fatal("empty query should fail")
	}

	// Update by id preserves created_at and use bookkeeping.
	if err := store.TouchMemories(ctx, []string{compaction.ID, "mem_missing"}); err != nil {
		t.Fatalf("TouchMemories: %v", err)
	}
	updated, err := store.SaveMemory(ctx, sdkprojectstate.SaveMemoryInput{
		ID: compaction.ID, Kind: "fact", Title: compaction.Title, Body: compaction.Body + " Env overrides win.",
	})
	if err != nil {
		t.Fatalf("SaveMemory update: %v", err)
	}
	if !updated.CreatedAt.Equal(compaction.CreatedAt) || updated.UseCount != 1 || updated.LastUsedAt == nil {
		t.Fatalf("update lost bookkeeping: %+v", updated)
	}
	if _, err := store.SaveMemory(ctx, sdkprojectstate.SaveMemoryInput{ID: "mem_missing", Title: "x", Body: "y"}); err == nil {
		t.Fatal("updating an unknown id should fail")
	}

	assertPGVerifyListDelete(t, store, compaction, pref.ID)
}

func assertPGVerifyListDelete(t *testing.T, store *Store, compaction *sdkprojectstate.Memory, prefID string) {
	t.Helper()
	ctx := context.Background()
	verified, err := store.VerifyMemory(ctx, compaction.ID, "def5678")
	if err != nil || verified.CommitSHA != "def5678" || !verified.VerifiedAt.After(compaction.VerifiedAt) {
		t.Fatalf("VerifyMemory = %+v, %v", verified, err)
	}
	if kept, err := store.VerifyMemory(ctx, compaction.ID, ""); err != nil || kept.CommitSHA != "def5678" {
		t.Fatalf("VerifyMemory without sha should keep the old one: %+v, %v", kept, err)
	}

	list, err := store.ListMemories(ctx, sdkprojectstate.MemoryFilter{})
	if err != nil || len(list) != 3 {
		t.Fatalf("ListMemories = %d, %v", len(list), err)
	}
	if list[0].Kind != sdkprojectstate.MemoryKindPreference || list[1].Kind != sdkprojectstate.MemoryKindProcedure {
		t.Fatalf("list order = %s, %s, %s; want preference, procedure, fact", list[0].Kind, list[1].Kind, list[2].Kind)
	}

	if err := store.DeleteMemory(ctx, prefID); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	if err := store.DeleteMemory(ctx, prefID); err == nil {
		t.Fatal("deleting twice should fail")
	}
	if _, err := store.GetMemory(ctx, prefID); err == nil {
		t.Fatal("deleted memory should not load")
	}
}

func TestPGMemoryValidationAndCap(t *testing.T) {
	store := newPGTestStore(t)
	ctx := context.Background()
	if _, err := store.SaveMemory(ctx, sdkprojectstate.SaveMemoryInput{Title: "t", Body: strings.Repeat("x", sdkprojectstate.MaxMemoryBodyLen+1)}); err == nil {
		t.Fatal("oversized body should fail")
	}
	if _, err := store.SaveMemory(ctx, sdkprojectstate.SaveMemoryInput{Title: "t", Body: "b", Citations: []sdkprojectstate.Citation{{Path: "/etc/passwd"}}}); err == nil {
		t.Fatal("absolute citation path should fail")
	}
	for range sdkprojectstate.DefaultMemoryCap {
		if _, err := store.pool.Exec(ctx, `INSERT INTO project_state_memories (project_id, id, title, body) VALUES ($1, $2, 't', 'b')`,
			store.projectID, newID("mem")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveMemory(ctx, sdkprojectstate.SaveMemoryInput{Title: "one more", Body: "over the cap"}); err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatalf("save over cap error = %v, want memory full", err)
	}
}

func TestPGReleaseClaimsAndPrime(t *testing.T) {
	store := newPGTestStore(t)
	ctx := context.Background()
	mustTask := func(title string) *sdkprojectstate.Task {
		task, err := store.CreateTask(ctx, sdkprojectstate.CreateTaskInput{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	a1, a2, b1, free := mustTask("a one"), mustTask("a two"), mustTask("b one"), mustTask("free")
	for _, claim := range []struct{ id, actor string }{{a1.ID, "run-a"}, {a2.ID, "run-a"}, {b1.ID, "run-b"}} {
		if _, err := store.ClaimTask(ctx, claim.id, claim.actor); err != nil {
			t.Fatal(err)
		}
	}

	released, err := store.ReleaseClaims(ctx, "run-a", "Claim released automatically")
	if err != nil {
		t.Fatalf("ReleaseClaims: %v", err)
	}
	if len(released) != 2 {
		t.Fatalf("released %d tasks, want 2", len(released))
	}
	for _, id := range []string{a1.ID, a2.ID} {
		task, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != sdkprojectstate.TaskStatusOpen || task.Assignee != "" || len(task.Comments) != 1 {
			t.Fatalf("released task = %+v", task)
		}
	}
	if task, _ := store.GetTask(ctx, b1.ID); task.Status != sdkprojectstate.TaskStatusInProgress || task.Assignee != "run-b" {
		t.Fatalf("other actor's claim changed: %+v", task)
	}
	if again, err := store.ReleaseClaims(ctx, "run-a", "x"); err != nil || len(again) != 0 {
		t.Fatalf("second release = %v, %v; want none", again, err)
	}

	mem, err := store.SaveMemory(ctx, sdkprojectstate.SaveMemoryInput{Kind: "decision", Title: "Kill team uses the builder model", Body: "Independence comes from a separate run, not a different model."})
	if err != nil {
		t.Fatal(err)
	}
	prime, err := store.PrimeContext(ctx, sdkprojectstate.PrimeOptions{Actor: "run-b"})
	if err != nil {
		t.Fatalf("PrimeContext: %v", err)
	}
	for _, want := range []string{"### Active Task", b1.ID, "### Ready Work", free.ID, "### Memory Index (1 of 1)", mem.ID + " [decision] Kill team uses the builder model"} {
		if !strings.Contains(prime, want) {
			t.Errorf("prime missing %q:\n%s", want, prime)
		}
	}
	again, err := store.PrimeContext(ctx, sdkprojectstate.PrimeOptions{Actor: "run-b"})
	if err != nil || again != prime {
		t.Fatalf("prime is not deterministic:\n%s\n---\n%s", prime, again)
	}
}

// TestPGMemoryToolsEndToEnd drives the SDK memory tools against the Postgres
// store: duplicate rejection, partial update by id, and get/touch.
func TestPGMemoryToolsEndToEnd(t *testing.T) {
	store := newPGTestStore(t)
	ctx := context.Background()
	tools := map[string]agentsdk.Tool{}
	for _, tool := range sdkprojectstatetools.Tools(store, "run-a") {
		tools[tool.Name()] = tool
	}
	call := func(name string, input any) (string, bool) {
		t.Helper()
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		raw, _ := json.Marshal(input)
		res, err := tool.Execute(ctx, raw, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res.Content, res.IsError
	}

	out, isErr := call("memory_save", map[string]any{
		"kind": "fact", "title": "Prime briefing is rendered into instructions once per run",
		"body": "The operator renders the durable state briefing into the system prompt once per run and again after compaction.",
	})
	if isErr {
		t.Fatalf("memory_save: %s", out)
	}
	var saved struct {
		Memory sdkprojectstate.Memory `json:"memory"`
	}
	if err := json.Unmarshal([]byte(out), &saved); err != nil || saved.Memory.ID == "" {
		t.Fatalf("memory_save output %q: %v", out, err)
	}

	dup, isErr := call("memory_save", map[string]any{
		"kind": "fact", "title": "Prime briefing rendered into instructions once per run",
		"body": "The operator renders the durable state briefing into the system prompt once per run, and again after compaction.",
	})
	if !isErr || !strings.Contains(dup, saved.Memory.ID) {
		t.Fatalf("duplicate save = %q (error=%v), want rejection naming %s", dup, isErr, saved.Memory.ID)
	}

	if out, isErr := call("memory_save", map[string]any{"id": saved.Memory.ID, "kind": "decision"}); isErr {
		t.Fatalf("partial update: %s", out)
	}
	got, err := store.GetMemory(ctx, saved.Memory.ID)
	if err != nil || got.Kind != sdkprojectstate.MemoryKindDecision || got.Body != saved.Memory.Body {
		t.Fatalf("partial update result = %+v, %v", got, err)
	}

	if out, isErr := call("memory_search", map[string]any{"query": "where is the briefing rendered"}); isErr || !strings.Contains(out, saved.Memory.ID) {
		t.Fatalf("memory_search = %q (error=%v)", out, isErr)
	}
	if out, isErr := call("memory_get", map[string]any{"id": saved.Memory.ID}); isErr {
		t.Fatalf("memory_get: %s", out)
	}
	if got, _ := store.GetMemory(ctx, saved.Memory.ID); got.UseCount != 1 {
		t.Fatalf("memory_get should record a use, use_count = %d", got.UseCount)
	}
}
