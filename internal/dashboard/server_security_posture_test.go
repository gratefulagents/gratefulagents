package dashboard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func TestGetSecurityConfigPosturesAggregates(t *testing.T) {
	sec := newMockSecurityStore()
	started := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	completed := started.Add(30 * time.Minute)
	earlier := completed.Add(-24 * time.Hour)
	sec.postures = []store.SecurityConfigPosture{
		{
			ScanName:        "nightly",
			Counts:          map[string]int32{"total": 7, "open": 5, "open_critical": 1, "open_high": 2, "baseline_new": 3},
			Repository:      "https://github.com/acme/payments.git",
			LastRunName:     "nightly-7",
			LastRunStatus:   "completed",
			LastStartedAt:   &started,
			LastCompletedAt: &completed,
			Activity: []store.SecurityRunActivityPoint{
				{RunName: "nightly-6", CompletedAt: earlier, SeverityCounts: map[string]int32{"high": 2}, Total: 2},
				{RunName: "nightly-7", CompletedAt: completed, SeverityCounts: map[string]int32{"critical": 1, "high": 2}, Total: 3},
			},
		},
		{ScanName: "weekly", Counts: map[string]int32{"total": 0}},
	}

	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.GetSecurityConfigPostures(ctx, &platform.GetSecurityConfigPosturesRequest{Namespace: "default", ActivityLimit: 5})
	if err != nil {
		t.Fatalf("GetSecurityConfigPostures() error = %v", err)
	}
	if !resp.GetStoreSupported() {
		t.Fatal("StoreSupported = false, want true")
	}
	if len(resp.GetWarnings()) != 0 {
		t.Fatalf("warnings = %v, want none", resp.GetWarnings())
	}
	if sec.lastActivityLimit != 5 {
		t.Fatalf("activity limit passed to store = %d, want 5", sec.lastActivityLimit)
	}
	if len(resp.GetPostures()) != 2 {
		t.Fatalf("postures = %d, want 2", len(resp.GetPostures()))
	}
	nightly := resp.GetPostures()[0]
	if nightly.GetScanName() != "nightly" ||
		nightly.GetRepository() != "https://github.com/acme/payments.git" ||
		nightly.GetLastRunName() != "nightly-7" || nightly.GetLastRunStatus() != "completed" {
		t.Fatalf("nightly posture = %+v", nightly)
	}
	if nightly.GetFindingCounts()["open_critical"] != 1 || nightly.GetFindingCounts()["baseline_new"] != 3 {
		t.Fatalf("nightly counts = %+v", nightly.GetFindingCounts())
	}
	if got := nightly.GetLastCompletedAt().AsTime(); !got.Equal(completed) {
		t.Fatalf("last completed = %v, want %v", got, completed)
	}
	if len(nightly.GetActivity()) != 2 ||
		nightly.GetActivity()[0].GetRunName() != "nightly-6" ||
		nightly.GetActivity()[1].GetTotal() != 3 ||
		nightly.GetActivity()[1].GetSeverityCounts()["critical"] != 1 {
		t.Fatalf("nightly activity = %+v", nightly.GetActivity())
	}
	if resp.GetPostures()[1].GetScanName() != "weekly" || resp.GetPostures()[1].GetLastRunName() != "" {
		t.Fatalf("weekly posture = %+v", resp.GetPostures()[1])
	}
}

func TestGetSecurityConfigPosturesWithoutSecurityStore(t *testing.T) {
	srv := newSecurityTestServer(t, newMockStateStore())
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.GetSecurityConfigPostures(ctx, &platform.GetSecurityConfigPosturesRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("GetSecurityConfigPostures() error = %v", err)
	}
	if resp.GetStoreSupported() {
		t.Fatal("StoreSupported = true, want false")
	}
	if len(resp.GetPostures()) != 0 {
		t.Fatalf("postures should be empty without a capable store: %+v", resp.GetPostures())
	}
}

func TestGetSecurityConfigPosturesPartialFailureWarns(t *testing.T) {
	sec := newMockSecurityStore()
	sec.posturesErr = errors.New("posture query offline")
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.GetSecurityConfigPostures(ctx, &platform.GetSecurityConfigPosturesRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("GetSecurityConfigPostures() error = %v", err)
	}
	if !resp.GetStoreSupported() {
		t.Fatal("StoreSupported = false, want true")
	}
	if len(resp.GetWarnings()) != 1 || !strings.Contains(resp.GetWarnings()[0], "posture query offline") {
		t.Fatalf("warnings = %v", resp.GetWarnings())
	}
}

func TestGetSecurityConfigPosturesHiddenFromNonOwner(t *testing.T) {
	sec := newMockSecurityStore()
	ctx := context.Background()
	if err := sec.SetResourceOwner(ctx, securityScanResourceType, "alice-scan", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner(alice-scan): %v", err)
	}
	if err := sec.SetResourceOwner(ctx, securityScanResourceType, "bob-scan", "default", "bob"); err != nil {
		t.Fatalf("SetResourceOwner(bob-scan): %v", err)
	}
	for _, scanName := range []string{"alice-scan", "bob-scan", "unowned-scan"} {
		sec.postures = append(sec.postures, store.SecurityConfigPosture{ScanName: scanName})
	}
	srv := newSecurityTestServer(t, sec)
	bob := actorContext("bob", "member", "", "")

	resp, err := srv.GetSecurityConfigPostures(bob, &platform.GetSecurityConfigPosturesRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("GetSecurityConfigPostures() error = %v", err)
	}
	if len(resp.GetPostures()) != 2 {
		t.Fatalf("postures = %+v, want bob-scan and unowned-scan only", resp.GetPostures())
	}
	for _, p := range resp.GetPostures() {
		if p.GetScanName() == "alice-scan" {
			t.Fatal("alice-scan posture leaked to bob")
		}
	}
	// The exclusion is also pushed into the store query so hidden scans
	// never influence what is fetched.
	if len(sec.lastPosturesExcluded) != 1 || sec.lastPosturesExcluded[0] != "alice-scan" {
		t.Fatalf("excluded scans pushed to store = %v, want [alice-scan]", sec.lastPosturesExcluded)
	}

	// Sharing alice-scan with bob makes its posture visible again and
	// removes it from the exclusion pushed into the store query.
	sec.shares = []store.ResourceShare{{
		ResourceType: securityScanResourceType, ResourceID: "alice-scan",
		ResourceNamespace: "default", SharedWithUserID: "bob", Permission: "viewer",
	}}
	resp, err = srv.GetSecurityConfigPostures(bob, &platform.GetSecurityConfigPosturesRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("GetSecurityConfigPostures(shared) error = %v", err)
	}
	if len(resp.GetPostures()) != 3 {
		t.Fatalf("shared postures = %+v, want all 3", resp.GetPostures())
	}
	if len(sec.lastPosturesExcluded) != 0 {
		t.Fatalf("excluded scans with share = %v, want none", sec.lastPosturesExcluded)
	}
	sec.shares = nil

	// An admin sees every configuration.
	admin := actorContext("root", "admin", "", "")
	resp, err = srv.GetSecurityConfigPostures(admin, &platform.GetSecurityConfigPosturesRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("GetSecurityConfigPostures(admin) error = %v", err)
	}
	if len(resp.GetPostures()) != 3 {
		t.Fatalf("admin postures = %d, want 3", len(resp.GetPostures()))
	}
}
