package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// posturesTestSeed seeds three configurations in "default": "alpha" with two
// completed runs and two findings, "beta" with one clean completed run, and
// "hidden" with one run and one finding (to be excluded by visibility).
func posturesTestSeed(ctx context.Context, t *testing.T, s *Store) (alpha1Done, alpha2Done time.Time) {
	t.Helper()
	base := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	alpha1Done = base.Add(30 * time.Minute)
	alpha2Done = base.Add(24*time.Hour + 30*time.Minute)

	scanIDs := map[string]*store.SecurityScanRecord{}
	upsertScan := func(scanName, runName, repo string, started, completed time.Time) {
		t.Helper()
		rec, err := s.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
			Namespace: "default", ScanName: scanName, RunName: runName, Repository: repo,
			Status: "completed", StartedAt: &started, CompletedAt: &completed,
		})
		if err != nil {
			t.Fatalf("UpsertSecurityScan(%s): %v", runName, err)
		}
		scanIDs[runName] = rec
	}
	upsertFinding := func(scanName, runName, fingerprint, severity string) {
		t.Helper()
		if _, _, err := s.UpsertSecurityFinding(ctx, &store.SecurityFindingRecord{
			ScanID:    scanIDs[runName].ID,
			Namespace: "default", ScanName: scanName, RunName: runName,
			Fingerprint: fingerprint, Title: "finding " + fingerprint,
			Severity: severity, Repository: "org/repo",
		}); err != nil {
			t.Fatalf("UpsertSecurityFinding(%s/%s): %v", runName, fingerprint, err)
		}
	}

	upsertScan("alpha", "alpha-1", "https://github.com/acme/api.git", base, alpha1Done)
	upsertFinding("alpha", "alpha-1", "fp-crit", "critical")
	upsertScan("alpha", "alpha-2", "https://github.com/acme/api.git", base.Add(24*time.Hour), alpha2Done)
	upsertFinding("alpha", "alpha-2", "fp-crit", "critical") // recurring
	upsertFinding("alpha", "alpha-2", "fp-crit", "critical") // duplicate submission (retry) in the same run
	upsertFinding("alpha", "alpha-2", "fp-high", "high")     // new in run 2

	upsertScan("beta", "beta-1", "https://github.com/acme/web.git", base, base.Add(time.Hour))

	upsertScan("hidden", "hidden-1", "https://github.com/acme/secret.git", base, base.Add(time.Hour))
	upsertFinding("hidden", "hidden-1", "fp-hidden", "high")
	return alpha1Done, alpha2Done
}

func verifyAlphaPosture(t *testing.T, alpha store.SecurityConfigPosture, alpha1Done, alpha2Done time.Time) {
	t.Helper()
	if alpha.LastRunName != "alpha-2" || alpha.LastRunStatus != "completed" ||
		alpha.Repository != "https://github.com/acme/api.git" {
		t.Errorf("alpha last run = %q %q %q", alpha.LastRunName, alpha.LastRunStatus, alpha.Repository)
	}
	if alpha.LastCompletedAt == nil || !alpha.LastCompletedAt.Equal(alpha2Done) {
		t.Errorf("alpha last completed = %v, want %v", alpha.LastCompletedAt, alpha2Done)
	}
	for key, want := range map[string]int32{
		"total": 2, "open": 2, "open_critical": 1, "open_high": 1,
		"baseline_recurring": 1, "baseline_new": 1,
	} {
		if alpha.Counts[key] != want {
			t.Errorf("alpha counts[%q] = %d, want %d", key, alpha.Counts[key], want)
		}
	}
	if len(alpha.Activity) != 2 {
		t.Fatalf("alpha activity = %+v, want 2 points", alpha.Activity)
	}
	if alpha.Activity[0].RunName != "alpha-1" || alpha.Activity[0].Total != 1 ||
		alpha.Activity[0].SeverityCounts["critical"] != 1 ||
		!alpha.Activity[0].CompletedAt.Equal(alpha1Done) {
		t.Errorf("alpha activity[0] = %+v", alpha.Activity[0])
	}
	// The duplicate fp-crit submission in alpha-2 must not inflate the run's
	// series: observations are deduplicated by (repository, fingerprint).
	if alpha.Activity[1].RunName != "alpha-2" || alpha.Activity[1].Total != 2 ||
		alpha.Activity[1].SeverityCounts["critical"] != 1 || alpha.Activity[1].SeverityCounts["high"] != 1 {
		t.Errorf("alpha activity[1] = %+v", alpha.Activity[1])
	}
}

func verifyBetaPosture(t *testing.T, beta store.SecurityConfigPosture) {
	t.Helper()
	// A configuration with runs but no findings still gets a posture row,
	// and its clean run stays in the series with an explicit zero.
	if beta.Counts["total"] != 0 || beta.LastRunName != "beta-1" {
		t.Errorf("beta posture = %+v", beta)
	}
	if len(beta.Activity) != 1 || beta.Activity[0].Total != 0 || len(beta.Activity[0].SeverityCounts) != 0 {
		t.Errorf("beta activity = %+v, want one zero point", beta.Activity)
	}
}

func TestListSecurityConfigPostures(t *testing.T) {
	s := setupSecurityTestStore(t)
	ctx := context.Background()
	alpha1Done, alpha2Done := posturesTestSeed(ctx, t, s)

	if _, err := s.ListSecurityConfigPostures(ctx, "", 0, nil); err == nil {
		t.Error("ListSecurityConfigPostures(empty namespace) = nil error, want error")
	}

	postures, err := s.ListSecurityConfigPostures(ctx, "default", 10, []string{"hidden"})
	if err != nil {
		t.Fatalf("ListSecurityConfigPostures: %v", err)
	}
	if len(postures) != 2 || postures[0].ScanName != "alpha" || postures[1].ScanName != "beta" {
		t.Fatalf("postures = %+v, want [alpha beta]", postures)
	}
	verifyAlphaPosture(t, postures[0], alpha1Done, alpha2Done)
	verifyBetaPosture(t, postures[1])

	// The per-configuration activity series honors the limit, keeping the
	// NEWEST runs.
	limited, err := s.ListSecurityConfigPostures(ctx, "default", 1, []string{"hidden"})
	if err != nil {
		t.Fatalf("ListSecurityConfigPostures(limit=1): %v", err)
	}
	if len(limited[0].Activity) != 1 || limited[0].Activity[0].RunName != "alpha-2" {
		t.Errorf("limited alpha activity = %+v, want [alpha-2]", limited[0].Activity)
	}

	// Without the exclusion the hidden configuration appears.
	all, err := s.ListSecurityConfigPostures(ctx, "default", 10, nil)
	if err != nil {
		t.Fatalf("ListSecurityConfigPostures(no exclusions): %v", err)
	}
	if len(all) != 3 || all[2].ScanName != "hidden" || all[2].Counts["open_high"] != 1 {
		t.Fatalf("all postures = %+v, want alpha/beta/hidden", all)
	}
}
