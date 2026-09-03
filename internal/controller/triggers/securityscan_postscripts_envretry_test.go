package triggers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// A post-script run that finishes successfully but records an inconclusive
// environment disposition did not test anything: the job is re-run within
// spec.execution.maxEnvRetries, and once that budget is spent the result
// stands as a disclosed coverage gap while the finding remains actionable.
func TestSecurityScanPostScriptRetriesUnreproducibleEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name          string
		disposition   string
		maxEnvRetries *int32
		wantRetry     bool
	}{
		{name: "unreproducible_env retries by default", disposition: "unreproducible_env", wantRetry: true},
		{name: "not_ready retries by default", disposition: "not_ready", wantRetry: true},
		{name: "maxEnvRetries zero stands", disposition: "unreproducible_env", maxEnvRetries: new(int32), wantRetry: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			clock := now
			scan := postScriptSecurityScan([]triggersv1alpha1.SecurityScanPostScript{{
				Name: "poc-validator", Prompt: "Rerun the PoC.", RunOn: "low-and-above-actionable",
			}}, 2)
			scan.Spec.Execution.RetryBackoff = metav1.Duration{Duration: time.Second}
			scan.Spec.Execution.MaxEnvRetries = tc.maxEnvRetries
			reconciler, k8sClient, _ := newDeterministicSecurityScanReconciler(t, now, scan)
			finding := postScriptTestFinding("00000000-0000-0000-0000-0000000000a1", "fp-alpha", "critical")
			finding.Status = store.SecurityFindingStatusTriaged
			findings := &postScriptFindingStore{findings: []store.SecurityFindingRecord{finding}}
			reconciler.Findings = findings
			reconciler.Now = func() time.Time { return clock }

			reconcileDeterministicSecurityScan(t, reconciler, scan)
			research := taskRunByTask(t, securityScanRuns(t, k8sClient, scan.Namespace), "research")
			markSecurityScanTaskRun(t, k8sClient, scan.Namespace, research.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
			reconcileDeterministicSecurityScan(t, reconciler, scan)
			reconcileDeterministicSecurityScan(t, reconciler, scan)

			exec := getSecurityScan(t, k8sClient, scan).Status.LastExecution
			run := postScriptRun(t, securityScanRuns(t, k8sClient, scan.Namespace), "poc-validator", finding.Fingerprint)
			findings.events = []store.SecurityFindingEvent{{
				FindingID: finding.ID, EventType: "policy_disposition", Actor: run.Name,
				Detail: json.RawMessage(`{"execution_id":"` + exec.ID + `","policy_check":"reproduction","policy_disposition":"` + tc.disposition + `"}`),
			}}
			markSecurityScanTaskRun(t, k8sClient, scan.Namespace, run.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
			reconcileDeterministicSecurityScan(t, reconciler, scan)

			exec = getSecurityScan(t, k8sClient, scan).Status.LastExecution
			job := postScriptJob(t, exec, finding.Fingerprint)
			if !tc.wantRetry {
				if job.State != triggersv1alpha1.SecurityScanPostScriptStateSucceeded || job.Attempts != 1 || !strings.Contains(job.Result, tc.disposition) {
					t.Fatalf("job = %#v, want Succeeded with the disposition in the result", job)
				}
				if !strings.Contains(strings.Join(exec.CoverageGaps, "\n"), tc.disposition) {
					t.Fatalf("coverage gaps = %#v, want the environment failure disclosed", exec.CoverageGaps)
				}
				return
			}
			// The fake client stamps no creation time on the finished run, so the
			// backoff derived from it has already elapsed and the retry may
			// have been launched within the same pass.
			if !strings.Contains(job.LastError, tc.disposition) {
				t.Fatalf("job = %#v, want the disposition recorded as the retry reason", job)
			}
			if job.State == triggersv1alpha1.SecurityScanPostScriptStatePending {
				if job.Attempts != 1 {
					t.Fatalf("job = %#v, want a queued retry", job)
				}
				clock = clock.Add(2 * time.Second)
				reconcileDeterministicSecurityScan(t, reconciler, scan)
				job = postScriptJob(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, finding.Fingerprint)
			}
			if job.State != triggersv1alpha1.SecurityScanPostScriptStateRunning || job.Attempts != 2 || job.RunName == run.Name {
				t.Fatalf("job = %#v, want a second attempt running", job)
			}
			if len(exec.CoverageGaps) != 0 {
				t.Fatalf("coverage gaps = %#v, want none while a retry is pending", exec.CoverageGaps)
			}

			// A verdict from the retry ends the job even though the older
			// environment disposition is still in the audit trail.
			retry := getAgentRun(t, k8sClient, scan.Namespace, job.RunName)
			findings.events = append([]store.SecurityFindingEvent{{
				FindingID: finding.ID, EventType: "policy_disposition", Actor: retry.Name,
				Detail: json.RawMessage(`{"execution_id":"` + exec.ID + `","policy_check":"reproduction","policy_disposition":"reproduced"}`),
			}}, findings.events...)
			markSecurityScanTaskRun(t, k8sClient, scan.Namespace, retry.Name, platformv1alpha1.AgentRunPhaseSucceeded, "", "")
			reconcileDeterministicSecurityScan(t, reconciler, scan)
			job = postScriptJob(t, getSecurityScan(t, k8sClient, scan).Status.LastExecution, finding.Fingerprint)
			if job.State != triggersv1alpha1.SecurityScanPostScriptStateSucceeded || job.Attempts != 2 {
				t.Fatalf("job after retry verdict = %#v, want Succeeded", job)
			}
		})
	}
}

type reopenResearchTestStore struct {
	*seedTestStore
	store.SecurityResearchStore
	revision *store.SecurityResearchRevision
	lookups  []string
	reopened []uuid.UUID
}

func (s *reopenResearchTestStore) GetSecurityResearchRevision(_ context.Context, _, targetKey, revision string) (*store.SecurityResearchRevision, error) {
	s.lookups = append(s.lookups, targetKey+"@"+revision)
	return s.revision, nil
}

func (s *reopenResearchTestStore) ReopenBlockedSecurityResearchHypotheses(_ context.Context, _ string, revisionID uuid.UUID) (int, error) {
	s.reopened = append(s.reopened, revisionID)
	return 1, nil
}

// Starting a new execution for a revision reopens hypotheses an earlier
// execution left blocked, and quietly does nothing for a revision the research
// tools never bound.
func TestSecurityScanNewExecutionReopensBlockedHypotheses(t *testing.T) {
	for _, bound := range []bool{true, false} {
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		scan := deterministicSecurityScan([]triggersv1alpha1.SecurityScanTask{{Name: "research", Objective: "inspect"}}, 1)
		scan.Spec.Revision = "0123456789abcdef0123456789abcdef01234567"
		reconciler, _, seed := newDeterministicSecurityScanReconciler(t, now, scan)
		research := &reopenResearchTestStore{seedTestStore: seed}
		revisionID := uuid.New()
		if bound {
			research.revision = &store.SecurityResearchRevision{ID: revisionID, Revision: scan.Spec.Revision}
		}
		reconciler.StateStore = research

		reconcileDeterministicSecurityScan(t, reconciler, scan)

		if len(research.lookups) != 1 || research.lookups[0] != scan.Name+"@"+scan.Spec.Revision {
			t.Fatalf("revision lookups = %#v, want exactly the scan's pinned revision", research.lookups)
		}
		if bound && (len(research.reopened) != 1 || research.reopened[0] != revisionID) {
			t.Fatalf("reopened = %#v, want the bound revision %s", research.reopened, revisionID)
		}
		if !bound && len(research.reopened) != 0 {
			t.Fatalf("reopened = %#v, want none for an unbound revision", research.reopened)
		}
	}
}
