/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package triggers

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// fakeBugReportFixStore is an in-memory store.AgentBugReportStore covering the
// methods the fix controller uses.
type fakeBugReportFixStore struct {
	reports map[uuid.UUID]*store.AgentBugReportRecord
}

func newFakeBugReportFixStore() *fakeBugReportFixStore {
	return &fakeBugReportFixStore{reports: map[uuid.UUID]*store.AgentBugReportRecord{}}
}

func (f *fakeBugReportFixStore) UpsertAgentBugReport(_ context.Context, rec *store.AgentBugReportRecord) (*store.AgentBugReportRecord, bool, error) {
	stored := *rec
	if stored.ID == uuid.Nil {
		stored.ID = uuid.New()
	}
	f.reports[stored.ID] = &stored
	out := stored
	return &out, true, nil
}

func (f *fakeBugReportFixStore) GetAgentBugReport(_ context.Context, namespace string, id uuid.UUID) (*store.AgentBugReportRecord, error) {
	rec, ok := f.reports[id]
	if !ok || rec.Namespace != namespace {
		return nil, nil
	}
	out := *rec
	return &out, nil
}

func (f *fakeBugReportFixStore) ListAgentBugReports(_ context.Context, filter store.AgentBugReportFilter) ([]store.AgentBugReportRecord, error) {
	var out []store.AgentBugReportRecord
	for _, rec := range f.reports {
		if rec.Namespace == filter.Namespace {
			out = append(out, *rec)
		}
	}
	return out, nil
}

func (f *fakeBugReportFixStore) SetAgentBugReportStatus(_ context.Context, namespace string, id uuid.UUID, status, actor, note string) error {
	rec, ok := f.reports[id]
	if !ok || rec.Namespace != namespace {
		return store.ErrAgentBugReportNotFound
	}
	rec.Status, rec.StatusActor, rec.StatusNote = status, actor, note
	return nil
}

func (f *fakeBugReportFixStore) SetAgentBugReportFix(_ context.Context, namespace string, id uuid.UUID, fix store.AgentBugReportFixUpdate) error {
	rec, ok := f.reports[id]
	if !ok || rec.Namespace != namespace {
		return store.ErrAgentBugReportNotFound
	}
	if fix.FixRunName != nil {
		rec.FixRunName = *fix.FixRunName
	}
	if fix.FixPRURL != nil {
		rec.FixPRURL = *fix.FixPRURL
	}
	if fix.Status != "" {
		rec.Status, rec.StatusActor, rec.StatusNote = fix.Status, fix.StatusActor, fix.StatusNote
	}
	return nil
}

func bugFixTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding platform scheme: %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding triggers scheme: %v", err)
	}
	return scheme
}

func seedFixReport(t *testing.T, s *fakeBugReportFixStore, namespace, fixRunName, fixPRURL, status string) *store.AgentBugReportRecord {
	t.Helper()
	rec, _, err := s.UpsertAgentBugReport(context.Background(), &store.AgentBugReportRecord{
		ID:          uuid.New(),
		Namespace:   namespace,
		Title:       "ApplyPatch fails on rename hunks",
		Body:        "expected X, got Y",
		Category:    store.AgentBugReportCategoryBug,
		Fingerprint: uuid.NewString(),
		Status:      status,
		FixRunName:  fixRunName,
		FixPRURL:    fixPRURL,
	})
	if err != nil {
		t.Fatalf("seeding report: %v", err)
	}
	return rec
}

func bugFixRun(namespace, name string, reportID uuid.UUID) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			UID:       types.UID("uid-" + name),
			Labels:    map[string]string{platformv1alpha1.BugReportIDLabel: reportID.String()},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/acme/platform"},
		},
	}
}

func reconcileBugFix(t *testing.T, r *BugReportFixReconciler, run *platformv1alpha1.AgentRun) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

func TestBugReportFixRecordsPullRequestURL(t *testing.T) {
	reports := newFakeBugReportFixStore()
	run := bugFixRun("default", "auto-bugfix-1", uuid.Nil)
	rec := seedFixReport(t, reports, "default", run.Name, "", store.AgentBugReportStatusInProgress)
	run.Labels[platformv1alpha1.BugReportIDLabel] = rec.ID.String()
	run.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{
		PullRequestURL: "https://github.com/acme/platform/pull/12",
	}

	k8sClient := fake.NewClientBuilder().WithScheme(bugFixTestScheme(t)).WithObjects(run).Build()
	r := &BugReportFixReconciler{Client: k8sClient, Scheme: bugFixTestScheme(t), Reports: reports}
	reconcileBugFix(t, r, run)

	got, _ := reports.GetAgentBugReport(context.Background(), "default", rec.ID)
	if got.FixPRURL != "https://github.com/acme/platform/pull/12" {
		t.Fatalf("FixPRURL = %q, want recorded PR", got.FixPRURL)
	}
	if got.Status != store.AgentBugReportStatusInProgress {
		t.Fatalf("Status = %q, want still in_progress until merge", got.Status)
	}
}

func TestBugReportFixResolvesOnMergedMonitor(t *testing.T) {
	reports := newFakeBugReportFixStore()
	prURL := "https://github.com/acme/platform/pull/12"
	run := bugFixRun("default", "auto-bugfix-1", uuid.Nil)
	rec := seedFixReport(t, reports, "default", run.Name, prURL, store.AgentBugReportStatusInProgress)
	run.Labels[platformv1alpha1.BugReportIDLabel] = rec.ID.String()
	run.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{PullRequestURL: prURL}

	monitor := &triggersv1alpha1.PullRequestMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      pullRequestMonitorName(run.UID, prURL),
		},
		Spec: triggersv1alpha1.PullRequestMonitorSpec{
			ImplementerRef: corev1.LocalObjectReference{Name: run.Name},
			Repository:     "acme/platform",
			Number:         12,
			URL:            prURL,
			DiscoveredAt:   metav1.Now(),
		},
		Status: triggersv1alpha1.PullRequestMonitorStatus{
			Lifecycle: triggersv1alpha1.PullRequestLifecycleMerged,
			MergedAt:  metav1.Now(),
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(bugFixTestScheme(t)).WithObjects(run, monitor).Build()
	r := &BugReportFixReconciler{Client: k8sClient, Scheme: bugFixTestScheme(t), Reports: reports}
	reconcileBugFix(t, r, run)

	got, _ := reports.GetAgentBugReport(context.Background(), "default", rec.ID)
	if got.Status != store.AgentBugReportStatusResolved {
		t.Fatalf("Status = %q, want resolved after merge", got.Status)
	}
	if got.StatusActor != bugReportFixActor {
		t.Fatalf("StatusActor = %q, want %q", got.StatusActor, bugReportFixActor)
	}
	if !strings.Contains(got.StatusNote, prURL) {
		t.Fatalf("StatusNote = %q, want PR URL attached", got.StatusNote)
	}
}

func TestBugReportFixReopensOnClosedUnmergedPR(t *testing.T) {
	reports := newFakeBugReportFixStore()
	prURL := "https://github.com/acme/platform/pull/12"
	run := bugFixRun("default", "auto-bugfix-1", uuid.Nil)
	rec := seedFixReport(t, reports, "default", run.Name, prURL, store.AgentBugReportStatusInProgress)
	run.Labels[platformv1alpha1.BugReportIDLabel] = rec.ID.String()

	monitor := &triggersv1alpha1.PullRequestMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      pullRequestMonitorName(run.UID, prURL),
		},
		Spec: triggersv1alpha1.PullRequestMonitorSpec{
			ImplementerRef: corev1.LocalObjectReference{Name: run.Name},
			Repository:     "acme/platform",
			Number:         12,
			URL:            prURL,
			DiscoveredAt:   metav1.Now(),
		},
		Status: triggersv1alpha1.PullRequestMonitorStatus{
			Lifecycle: triggersv1alpha1.PullRequestLifecycleClosed,
		},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(bugFixTestScheme(t)).WithObjects(run, monitor).Build()
	r := &BugReportFixReconciler{Client: k8sClient, Scheme: bugFixTestScheme(t), Reports: reports}
	reconcileBugFix(t, r, run)

	got, _ := reports.GetAgentBugReport(context.Background(), "default", rec.ID)
	if got.Status != store.AgentBugReportStatusOpen {
		t.Fatalf("Status = %q, want open after PR closed unmerged", got.Status)
	}
}

func TestBugReportFixReopensWhenRunEndsWithoutPR(t *testing.T) {
	reports := newFakeBugReportFixStore()
	run := bugFixRun("default", "auto-bugfix-1", uuid.Nil)
	rec := seedFixReport(t, reports, "default", run.Name, "", store.AgentBugReportStatusInProgress)
	run.Labels[platformv1alpha1.BugReportIDLabel] = rec.ID.String()
	run.Status.Phase = platformv1alpha1.AgentRunPhaseFailed
	run.Status.LastError = "sandbox crashed"

	k8sClient := fake.NewClientBuilder().WithScheme(bugFixTestScheme(t)).WithObjects(run).Build()
	r := &BugReportFixReconciler{Client: k8sClient, Scheme: bugFixTestScheme(t), Reports: reports}
	reconcileBugFix(t, r, run)

	got, _ := reports.GetAgentBugReport(context.Background(), "default", rec.ID)
	if got.Status != store.AgentBugReportStatusOpen {
		t.Fatalf("Status = %q, want open after failed fix run", got.Status)
	}
	if !strings.Contains(got.StatusNote, "sandbox crashed") {
		t.Fatalf("StatusNote = %q, want failure reason", got.StatusNote)
	}
}

func TestBugReportFixIgnoresSupersededRun(t *testing.T) {
	reports := newFakeBugReportFixStore()
	run := bugFixRun("default", "auto-bugfix-old", uuid.Nil)
	// The report's current fix run is a different (newer) run.
	rec := seedFixReport(t, reports, "default", "auto-bugfix-new", "", store.AgentBugReportStatusInProgress)
	run.Labels[platformv1alpha1.BugReportIDLabel] = rec.ID.String()
	run.Status.Phase = platformv1alpha1.AgentRunPhaseFailed

	k8sClient := fake.NewClientBuilder().WithScheme(bugFixTestScheme(t)).WithObjects(run).Build()
	r := &BugReportFixReconciler{Client: k8sClient, Scheme: bugFixTestScheme(t), Reports: reports}
	reconcileBugFix(t, r, run)

	got, _ := reports.GetAgentBugReport(context.Background(), "default", rec.ID)
	if got.Status != store.AgentBugReportStatusInProgress {
		t.Fatalf("Status = %q, want untouched in_progress", got.Status)
	}
}
