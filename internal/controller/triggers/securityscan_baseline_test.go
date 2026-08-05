package triggers

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// finalizingSecurityScanFindingStore records baseline-finalization and
// accepted-risk-expiry calls.
type finalizingSecurityScanFindingStore struct {
	store.SecurityFindingStore
	finalizedRuns     []string
	expiredNamespaces []string
}

func (s *finalizingSecurityScanFindingStore) SummarizeSecurityFindings(context.Context, string, string, string) (map[string]int32, error) {
	return map[string]int32{}, nil
}

func (s *finalizingSecurityScanFindingStore) FinalizeSecurityScanBaseline(_ context.Context, _, runName string) (int32, error) {
	s.finalizedRuns = append(s.finalizedRuns, runName)
	return 0, nil
}

func (s *finalizingSecurityScanFindingStore) ExpireAcceptedRisks(_ context.Context, namespace string) (int32, error) {
	s.expiredNamespaces = append(s.expiredNamespaces, namespace)
	return 0, nil
}

// terminalScanWithRun builds a one-shot scan whose last run already exists
// with the given phase, so the reconcile enters the "run already ran for
// this generation" status-refresh branch.
func terminalScanWithRun(t *testing.T, phase platformv1alpha1.AgentRunPhase) (*triggersv1alpha1.SecurityScan, *platformv1alpha1.AgentRun) {
	t.Helper()
	scan := securityScanTestScan()
	scan.Generation = 1
	run := securityScanPriorRun(scan, phase)
	scan.Status.ObservedGeneration = 1
	scan.Status.LastRunName = run.Name
	return scan, run
}

func TestSecurityScanFinalizesBaselineOnSuccessfulTerminalRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan, run := terminalScanWithRun(t, platformv1alpha1.AgentRunPhaseSucceeded)
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, run)
	findings := &finalizingSecurityScanFindingStore{}
	reconciler.Findings = findings

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(findings.finalizedRuns) != 1 || findings.finalizedRuns[0] != run.Name {
		t.Fatalf("finalized runs = %v, want [%s]", findings.finalizedRuns, run.Name)
	}
	if len(findings.expiredNamespaces) != 1 || findings.expiredNamespaces[0] != scan.Namespace {
		t.Fatalf("expiry sweeps = %v, want [%s]", findings.expiredNamespaces, scan.Namespace)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.Phase != "Completed" {
		t.Fatalf("Phase = %q, want Completed", updated.Status.Phase)
	}
}

func TestSecurityScanNeverFinalizesBaselineOnFailedRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, phase := range []platformv1alpha1.AgentRunPhase{
		platformv1alpha1.AgentRunPhaseFailed,
		platformv1alpha1.AgentRunPhaseCancelled,
		platformv1alpha1.AgentRunPhaseRunning,
	} {
		scan, run := terminalScanWithRun(t, phase)
		reconciler, _, _ := newSecurityScanReconciler(t, now, scan, run)
		findings := &finalizingSecurityScanFindingStore{}
		reconciler.Findings = findings

		if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
			t.Fatalf("Reconcile(%s) error = %v", phase, err)
		}
		if len(findings.finalizedRuns) != 0 {
			t.Errorf("phase %s finalized runs = %v, want none (only Succeeded defines a baseline)", phase, findings.finalizedRuns)
		}
		if len(findings.expiredNamespaces) != 0 {
			t.Errorf("phase %s swept expiries = %v, want none", phase, findings.expiredNamespaces)
		}
	}
}

func TestSecurityScanScheduledFinalizesOnlyWhenLastRunSucceeded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.Schedule = "0 2 * * *"
	run := securityScanPriorRun(scan, platformv1alpha1.AgentRunPhaseSucceeded)
	scan.Status.LastRunName = run.Name
	scan.Status.ObservedSchedule = "0 2 * * *"
	scan.Status.ObservedTimeZone = "UTC"
	last := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	scan.Status.LastScanTime = &last
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan, run)
	findings := &finalizingSecurityScanFindingStore{}
	reconciler.Findings = findings

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(findings.finalizedRuns) != 1 || findings.finalizedRuns[0] != run.Name {
		t.Fatalf("finalized runs = %v, want [%s]", findings.finalizedRuns, run.Name)
	}
}
