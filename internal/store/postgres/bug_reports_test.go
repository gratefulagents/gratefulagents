package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/store"
	pgstore "github.com/gratefulagents/gratefulagents/internal/store/postgres"
)

func setupBugReportStore(t *testing.T) store.AgentBugReportStore {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to test db: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("running migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM agent_bug_reports"); err != nil {
		t.Fatalf("cleaning agent_bug_reports: %v", err)
	}
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAgentBugReportLifecycle(t *testing.T) {
	s := setupBugReportStore(t)
	ctx := context.Background()

	rec := &store.AgentBugReportRecord{
		Namespace:   "test-ns",
		RunName:     "run-1",
		Category:    store.AgentBugReportCategoryBug,
		ToolName:    "ApplyPatch",
		Title:       "ApplyPatch fails on rename hunks",
		Body:        "expected the patch to apply, got invalid hunk header",
		Fingerprint: "fp-1",
	}
	created, isNew, err := s.UpsertAgentBugReport(ctx, rec)
	if err != nil {
		t.Fatalf("UpsertAgentBugReport() error = %v", err)
	}
	if !isNew || created.Occurrences != 1 || created.Status != store.AgentBugReportStatusOpen {
		t.Fatalf("first upsert: isNew=%t occurrences=%d status=%q", isNew, created.Occurrences, created.Status)
	}

	// Reoccurrence from another run merges and keeps the latest run identity.
	rec2 := *rec
	rec2.RunName = "run-2"
	rec2.Body = "still broken in run-2"
	merged, isNew, err := s.UpsertAgentBugReport(ctx, &rec2)
	if err != nil {
		t.Fatalf("UpsertAgentBugReport() merge error = %v", err)
	}
	if isNew || merged.ID != created.ID || merged.Occurrences != 2 || merged.RunName != "run-2" || merged.Body != "still broken in run-2" {
		t.Fatalf("merge: isNew=%t id=%s occurrences=%d run=%q", isNew, merged.ID, merged.Occurrences, merged.RunName)
	}

	// A retry from the same run must not inflate the distinct-run count.
	retried, isNew, err := s.UpsertAgentBugReport(ctx, &rec2)
	if err != nil {
		t.Fatalf("UpsertAgentBugReport() same-run retry error = %v", err)
	}
	if isNew || retried.Occurrences != 2 {
		t.Fatalf("same-run retry: isNew=%t occurrences=%d, want false/2", isNew, retried.Occurrences)
	}

	// Same fingerprint in another namespace is a separate report.
	rec3 := *rec
	rec3.Namespace = "other-ns"
	other, isNew, err := s.UpsertAgentBugReport(ctx, &rec3)
	if err != nil {
		t.Fatalf("UpsertAgentBugReport() other-ns error = %v", err)
	}
	if !isNew || other.ID == created.ID {
		t.Fatalf("cross-namespace upsert merged: isNew=%t", isNew)
	}

	// Status transitions: resolved regresses to open only when a different
	// run reports again; dismissed stays dismissed.
	if err := s.SetAgentBugReportStatus(ctx, "test-ns", created.ID, store.AgentBugReportStatusResolved, "alice", "fixed"); err != nil {
		t.Fatalf("SetAgentBugReportStatus() error = %v", err)
	}
	// Same run (run-2 is the last recorded reporter): stays resolved.
	stillResolved, _, err := s.UpsertAgentBugReport(ctx, &rec2)
	if err != nil {
		t.Fatalf("UpsertAgentBugReport() same-run after resolve error = %v", err)
	}
	if stillResolved.Status != store.AgentBugReportStatusResolved || stillResolved.Occurrences != 2 {
		t.Fatalf("same-run after resolve: status=%q occurrences=%d, want resolved/2", stillResolved.Status, stillResolved.Occurrences)
	}
	// Different run: reopens and counts.
	reopened, _, err := s.UpsertAgentBugReport(ctx, rec)
	if err != nil {
		t.Fatalf("UpsertAgentBugReport() reoccurrence error = %v", err)
	}
	if reopened.Status != store.AgentBugReportStatusOpen || reopened.Occurrences != 3 {
		t.Fatalf("resolved reoccurrence: status=%q occurrences=%d, want open/3", reopened.Status, reopened.Occurrences)
	}
	if err := s.SetAgentBugReportStatus(ctx, "test-ns", created.ID, store.AgentBugReportStatusDismissed, "alice", "noise"); err != nil {
		t.Fatalf("SetAgentBugReportStatus() dismiss error = %v", err)
	}
	dismissed, _, err := s.UpsertAgentBugReport(ctx, &rec2)
	if err != nil {
		t.Fatalf("UpsertAgentBugReport() dismissed reoccurrence error = %v", err)
	}
	if dismissed.Status != store.AgentBugReportStatusDismissed || dismissed.Occurrences != 4 {
		t.Fatalf("dismissed reoccurrence: status=%q occurrences=%d, want dismissed/4", dismissed.Status, dismissed.Occurrences)
	}

	// Get / list / filters.
	got, err := s.GetAgentBugReport(ctx, "test-ns", created.ID)
	if err != nil || got == nil || got.StatusActor != "alice" {
		t.Fatalf("GetAgentBugReport() = %+v, err %v", got, err)
	}
	if missing, err := s.GetAgentBugReport(ctx, "test-ns", uuid.New()); err != nil || missing != nil {
		t.Fatalf("GetAgentBugReport(missing) = %+v, err %v", missing, err)
	}
	list, err := s.ListAgentBugReports(ctx, store.AgentBugReportFilter{Namespace: "test-ns"})
	if err != nil || len(list) != 1 {
		t.Fatalf("ListAgentBugReports() = %d reports, err %v; want 1", len(list), err)
	}
	list, err = s.ListAgentBugReports(ctx, store.AgentBugReportFilter{Namespace: "test-ns", Status: store.AgentBugReportStatusOpen})
	if err != nil || len(list) != 0 {
		t.Fatalf("ListAgentBugReports(open) = %d reports, err %v; want 0", len(list), err)
	}

	// Invalid inputs fail closed.
	if err := s.SetAgentBugReportStatus(ctx, "test-ns", created.ID, "bogus", "alice", ""); err == nil {
		t.Fatal("SetAgentBugReportStatus() accepted invalid status")
	}
	if err := s.SetAgentBugReportStatus(ctx, "test-ns", uuid.New(), store.AgentBugReportStatusOpen, "alice", ""); err != store.ErrAgentBugReportNotFound {
		t.Fatalf("SetAgentBugReportStatus(missing) error = %v, want ErrAgentBugReportNotFound", err)
	}
	if _, _, err := s.UpsertAgentBugReport(ctx, &store.AgentBugReportRecord{Namespace: "test-ns", Fingerprint: "fp-x", Category: "bogus", Title: "t", Body: "b"}); err == nil {
		t.Fatal("UpsertAgentBugReport() accepted invalid category")
	}
}
