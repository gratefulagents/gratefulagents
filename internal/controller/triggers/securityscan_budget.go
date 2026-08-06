package triggers

import (
	"context"
	"fmt"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// securityScanReasonBudgetExceeded is the Ready=False reason reported when a
// hard scan budget is exceeded. The active run is cancelled; completed work
// (persisted findings, reports, scan status) is preserved.
const securityScanReasonBudgetExceeded = "BudgetExceeded"

// securityScanModelJobs counts the sub-agent runs a scan run has spawned,
// from platform-observed run status only.
func securityScanModelJobs(run *platformv1alpha1.AgentRun) int32 {
	jobs := int32(len(run.Status.Children)) //nolint:gosec // child lists are small
	if ts := run.Status.TeamSummary; ts != nil && ts.TotalChildren > jobs {
		jobs = ts.TotalChildren
	}
	if run.Status.AgentCount > jobs {
		jobs = run.Status.AgentCount
	}
	return jobs
}

// securityScanValidationJobs counts the scan run's children whose step or
// role names one of the scan's post-scripts (validation / proof-of-concept
// jobs), from platform-observed run status only.
func securityScanValidationJobs(run *platformv1alpha1.AgentRun, postScriptNames map[string]bool) int32 {
	if len(postScriptNames) == 0 {
		return 0
	}
	var jobs int32
	for _, child := range run.Status.Children {
		if postScriptNames[child.Step] || postScriptNames[child.Role] {
			jobs++
		}
	}
	return jobs
}

// securityScanPostScriptNames collects the scan's post-script names (inline,
// referenced, and pack defaults) for validation-job counting.
func securityScanPostScriptNames(scan *triggersv1alpha1.SecurityScan, pack *triggersv1alpha1.SecurityPolicyPack) map[string]bool {
	names := map[string]bool{}
	for _, script := range scan.Spec.PostScripts {
		names[script.Name] = true
	}
	for _, ref := range scan.Spec.PostScriptRefs {
		names[ref.Name] = true
	}
	if pack != nil {
		for _, ref := range pack.Spec.DefaultPostScriptRefs {
			names[ref.Name] = true
		}
	}
	return names
}

// enforceSecurityBudgets publishes the scan's effective budgets to
// status.budget and enforces the limits the created run cannot self-limit,
// entirely from platform-side data: persisted finding counts and the run's
// status/usage metrics. Model output can never relax a limit — every limit
// derives from the CRD spec merged with the policy pack, and usage comes
// from the AgentRun resource, never from session content. When a hard limit
// is exceeded and the run is still active, the run is cancelled the same way
// the dashboard cancel path does (the cancel-requested annotation), the scan
// reports Ready=False reason BudgetExceeded plus an event, and all completed
// work is preserved. Best-effort: errors are logged, never failing the
// reconcile.
func (r *SecurityScanReconciler) enforceSecurityBudgets(ctx context.Context, scan *triggersv1alpha1.SecurityScan) {
	log := logf.FromContext(ctx)
	pack := r.scanPolicyPack(ctx, scan)
	effective := effectiveSecurityScanBudgets(scan, pack)
	if effective == nil || effective.IsZero() {
		if scan.Status.Budget == nil {
			return
		}
		if err := retrySecurityScanStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.Budget = nil
		}); err != nil {
			log.Error(err, "failed to clear budget status", "scan", scan.Name)
		}
		return
	}

	var exceeded []string
	// Findings budget: persisted counts only. Evaluated even without an
	// active run so an already-exceeded budget warns before the next launch.
	if effective.MaxFindings > 0 && r.Findings != nil {
		counts, err := r.Findings.SummarizeSecurityFindings(ctx, scan.Namespace, scan.Name, "", true)
		if err != nil {
			log.Error(err, "failed to summarize findings for budget enforcement", "scan", scan.Name)
		} else if total := counts["total"]; total > effective.MaxFindings {
			exceeded = append(exceeded, fmt.Sprintf("persisted findings %d exceed budgets.maxFindings %d", total, effective.MaxFindings))
		}
	}

	var run *platformv1alpha1.AgentRun
	runActive := false
	if scan.Status.LastRunName != "" {
		candidate := &platformv1alpha1.AgentRun{}
		err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: scan.Status.LastRunName}, candidate)
		switch {
		case err == nil:
			run = candidate
			runActive = !isCronRunTerminal(run.Status.Phase)
		case !apierrors.IsNotFound(err):
			log.Error(err, "failed to get scan AgentRun for budget enforcement", "run", scan.Status.LastRunName)
		}
	}
	if run != nil {
		if metrics := run.Status.Metrics; metrics != nil {
			if limit := securityBudgetCostUSD(effective.MaxCostUSD); limit >= 0 {
				if cost := securityBudgetCostUSD(metrics.CostUsd); cost > limit {
					exceeded = append(exceeded, fmt.Sprintf("run cost $%s exceeds budgets.maxCostUSD %s", metrics.CostUsd, effective.MaxCostUSD))
				}
			}
			if effective.MaxTokens > 0 {
				if tokens := metrics.InputTokens + metrics.OutputTokens; tokens > effective.MaxTokens {
					exceeded = append(exceeded, fmt.Sprintf("run tokens %d exceed budgets.maxTokens %d", tokens, effective.MaxTokens))
				}
			}
		}
		if effective.MaxModelJobs > 0 {
			if jobs := securityScanModelJobs(run); jobs > effective.MaxModelJobs {
				exceeded = append(exceeded, fmt.Sprintf("run spawned %d sub-agent jobs, exceeding budgets.maxModelJobs %d", jobs, effective.MaxModelJobs))
			}
		}
		if effective.MaxValidationJobs > 0 {
			if jobs := securityScanValidationJobs(run, securityScanPostScriptNames(scan, pack)); jobs > effective.MaxValidationJobs {
				exceeded = append(exceeded, fmt.Sprintf("run spawned %d validation jobs, exceeding budgets.maxValidationJobs %d", jobs, effective.MaxValidationJobs))
			}
		}
	}

	msg := strings.Join(exceeded, "; ")
	if len(exceeded) > 0 && runActive {
		cancelled, err := r.cancelScanRun(ctx, run)
		if err != nil {
			log.Error(err, "failed to cancel scan AgentRun over budget", "run", run.Name)
		} else if cancelled {
			log.Info("cancelled scan AgentRun: budget exceeded", "run", run.Name, "reason", msg)
			if r.Recorder != nil {
				r.Recorder.Eventf(scan, nil, corev1.EventTypeWarning, securityScanReasonBudgetExceeded,
					securityScanReasonBudgetExceeded, "cancelled run %s: %s (completed work is preserved)", run.Name, msg)
			}
		}
	}

	checked := metav1.NewTime(r.now())
	if err := retrySecurityScanStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.Budget = &triggersv1alpha1.SecurityScanBudgetStatus{
			Effective:       effective.DeepCopy(),
			Exceeded:        len(exceeded) > 0,
			Message:         msg,
			LastCheckedTime: &checked,
		}
		if len(exceeded) > 0 {
			setSecurityScanCondition(fresh, metav1.ConditionFalse, securityScanReasonBudgetExceeded, msg)
		}
	}); err != nil {
		log.Error(err, "failed to record budget status", "scan", scan.Name)
	}
}

// cancelScanRun requests graceful cancellation of a scan AgentRun exactly
// like the dashboard cancel path: the cancel-requested annotation is stamped
// under optimistic-concurrency retry. It returns false when the annotation
// was already present or the run reached a terminal phase meanwhile.
func (r *SecurityScanReconciler) cancelScanRun(ctx context.Context, run *platformv1alpha1.AgentRun) (bool, error) {
	cancelled := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(run), fresh); err != nil {
			return err
		}
		if isCronRunTerminal(fresh.Status.Phase) || strings.TrimSpace(fresh.Annotations[cancelRequestedAnnotation]) != "" {
			cancelled = false
			return nil
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[cancelRequestedAnnotation] = time.Now().UTC().Format(time.RFC3339)
		if err := r.Patch(ctx, fresh, patch); err != nil {
			return err
		}
		cancelled = true
		return nil
	})
	return cancelled, client.IgnoreNotFound(err)
}
