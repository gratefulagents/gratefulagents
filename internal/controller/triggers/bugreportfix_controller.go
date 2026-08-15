/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package triggers

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// bugReportFixActor attributes automated status transitions made by this
// controller on agent bug reports.
const bugReportFixActor = "bug-squasher"

// BugReportFixReconciler tracks auto-fix AgentRuns launched for agent bug
// reports (runs labeled with platformv1alpha1.BugReportIDLabel). It records
// the fix pull request on the report as soon as the run opens one, resolves
// the report once every fix PR reached a terminal state with at least one
// merge (observed through the run's PullRequestMonitors), and reopens the
// report when the fix attempt ends without a mergeable result — including
// when the fix run itself is deleted mid-flight.
type BugReportFixReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Reports is the durable agent bug report store (Postgres-backed).
	Reports store.AgentBugReportStore
}

// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=pullrequestmonitors,verbs=get;list;watch

func (r *BugReportFixReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	reports := r.Reports
	if reports == nil {
		return ctrl.Result{}, nil
	}

	run := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		if apierrors.IsNotFound(err) {
			// A deleted fix run also garbage-collects its owned
			// PullRequestMonitors, so nothing is tracking or executing the
			// fix anymore: reopen the report instead of leaving it stuck in
			// in_progress.
			return ctrl.Result{}, r.reopenAfterFixRunDeleted(ctx, req.Namespace, req.Name)
		}
		return ctrl.Result{}, err
	}
	rawID := run.Labels[platformv1alpha1.BugReportIDLabel]
	if rawID == "" {
		return ctrl.Result{}, nil
	}
	reportID, err := uuid.Parse(rawID)
	if err != nil {
		log.Info("ignoring AgentRun with invalid bug-report-id label", "run", run.Name, "value", rawID)
		return ctrl.Result{}, nil
	}
	rec, err := reports.GetAgentBugReport(ctx, run.Namespace, reportID)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting bug report %s: %w", reportID, err)
	}
	// Only the report's current fix run drives its lifecycle; superseded runs
	// (a human re-triggered the fix) are ignored.
	if rec == nil || rec.FixRunName != run.Name {
		return ctrl.Result{}, nil
	}

	prs := artifactPullRequests(run)
	outcome, err := r.assessFixPullRequests(ctx, run, prs)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Keep the recorded fix PR fresh: a merged PR wins, otherwise the run's
	// most recently opened PR (a later PR may replace an earlier abandoned
	// one).
	if want := recordablePRURL(run, prs, outcome.mergedURLs); want != "" && want != rec.FixPRURL {
		if err := reports.SetAgentBugReportFix(ctx, run.Namespace, reportID, store.AgentBugReportFixUpdate{
			FixPRURL: &want,
		}); err != nil {
			return ctrl.Result{}, fmt.Errorf("recording fix PR on bug report %s: %w", reportID, err)
		}
		rec.FixPRURL = want
		log.Info("recorded bug report fix PR", "report", reportID, "run", run.Name, "pr", want)
	}

	// Reports the fix already finished with (or a human re-triaged) need no
	// further lifecycle transitions.
	if rec.Status != store.AgentBugReportStatusInProgress {
		return ctrl.Result{}, nil
	}

	if len(prs) > 0 {
		switch {
		case outcome.pending:
			// At least one fix PR is still open or not yet observed: wait.
			return ctrl.Result{}, nil
		case len(outcome.mergedURLs) > 0:
			note := "auto-fixed by " + strings.Join(outcome.mergedURLs, ", ") + " (merged)"
			if !outcome.mergedAt.IsZero() {
				note = fmt.Sprintf("auto-fixed by %s (merged %s)", strings.Join(outcome.mergedURLs, ", "), outcome.mergedAt.UTC().Format("2006-01-02 15:04 UTC"))
			}
			if err := reports.SetAgentBugReportFix(ctx, run.Namespace, reportID, store.AgentBugReportFixUpdate{
				Status:      store.AgentBugReportStatusResolved,
				StatusActor: bugReportFixActor,
				StatusNote:  note,
			}); err != nil {
				return ctrl.Result{}, fmt.Errorf("resolving bug report %s: %w", reportID, err)
			}
			log.Info("resolved bug report after fix PR merge", "report", reportID, "prs", outcome.mergedURLs)
		default:
			// Every fix PR closed without merging.
			if err := reports.SetAgentBugReportFix(ctx, run.Namespace, reportID, store.AgentBugReportFixUpdate{
				Status:      store.AgentBugReportStatusOpen,
				StatusActor: bugReportFixActor,
				StatusNote:  fmt.Sprintf("fix run %s's pull requests were closed without merging", run.Name),
			}); err != nil {
				return ctrl.Result{}, fmt.Errorf("reopening bug report %s: %w", reportID, err)
			}
			log.Info("reopened bug report after fix PRs closed unmerged", "report", reportID, "run", run.Name)
		}
		return ctrl.Result{}, nil
	}

	// No PR: reopen the report once the fix run reaches a terminal phase, so
	// it never sticks in in_progress with nothing working on it.
	if isCronRunTerminal(run.Status.Phase) {
		note := fmt.Sprintf("auto-fix run %s finished without opening a pull request", run.Name)
		if run.Status.Phase != platformv1alpha1.AgentRunPhaseSucceeded {
			note = fmt.Sprintf("auto-fix run %s ended with phase %s without opening a pull request", run.Name, run.Status.Phase)
			if run.Status.LastError != "" {
				note += ": " + run.Status.LastError
			}
		}
		if err := reports.SetAgentBugReportFix(ctx, run.Namespace, reportID, store.AgentBugReportFixUpdate{
			Status:      store.AgentBugReportStatusOpen,
			StatusActor: bugReportFixActor,
			StatusNote:  note,
		}); err != nil {
			return ctrl.Result{}, fmt.Errorf("reopening bug report %s: %w", reportID, err)
		}
		log.Info("reopened bug report after fix run ended without a PR", "report", reportID, "run", run.Name, "phase", run.Status.Phase)
	}
	return ctrl.Result{}, nil
}

// fixPullRequestOutcome aggregates the lifecycle of every fix PR the run
// opened. pending is true while any PR is still open, drafted, or not yet
// observed by its monitor.
type fixPullRequestOutcome struct {
	mergedURLs []string
	mergedAt   metav1.Time
	pending    bool
}

// assessFixPullRequests inspects the PullRequestMonitor of every PR the run
// recorded. Monitors are keyed deterministically by (run UID, PR URL), the
// same scheme the artifact reconciler uses to create them.
func (r *BugReportFixReconciler) assessFixPullRequests(ctx context.Context, run *platformv1alpha1.AgentRun, prs []artifactPullRequest) (fixPullRequestOutcome, error) {
	var out fixPullRequestOutcome
	for _, pr := range prs {
		monitor := &triggersv1alpha1.PullRequestMonitor{}
		key := client.ObjectKey{Namespace: run.Namespace, Name: pullRequestMonitorName(run.UID, pr.URL)}
		if err := r.Get(ctx, key, monitor); err != nil {
			if apierrors.IsNotFound(err) {
				// The artifact reconciler has not created (or GitHub has not
				// been polled for) this monitor yet.
				out.pending = true
				continue
			}
			return out, err
		}
		switch monitor.Status.Lifecycle {
		case triggersv1alpha1.PullRequestLifecycleMerged:
			out.mergedURLs = append(out.mergedURLs, pr.URL)
			if out.mergedAt.IsZero() || monitor.Status.MergedAt.Before(&out.mergedAt) {
				out.mergedAt = monitor.Status.MergedAt
			}
		case triggersv1alpha1.PullRequestLifecycleClosed:
			// Terminal without merge.
		default:
			// open, draft, or not yet observed.
			out.pending = true
		}
	}
	return out, nil
}

// recordablePRURL picks the PR URL worth storing on the report: a merged PR
// wins, otherwise the run's most recently opened PR, falling back to the
// first canonical artifact PR.
func recordablePRURL(run *platformv1alpha1.AgentRun, prs []artifactPullRequest, mergedURLs []string) string {
	if len(mergedURLs) > 0 {
		return mergedURLs[0]
	}
	if len(prs) == 0 {
		return ""
	}
	if run.Status.Artifacts != nil {
		if latest, ok := parseArtifactPullRequestURL(run.Status.Artifacts.PullRequestURL); ok {
			return latest.URL
		}
	}
	return prs[0].URL
}

// reopenAfterFixRunDeleted reopens the in_progress report whose current fix
// run was deleted before finishing.
func (r *BugReportFixReconciler) reopenAfterFixRunDeleted(ctx context.Context, namespace, runName string) error {
	rec, err := r.Reports.GetAgentBugReportByFixRun(ctx, namespace, runName)
	if err != nil {
		return fmt.Errorf("looking up bug report for deleted fix run %s/%s: %w", namespace, runName, err)
	}
	if rec == nil || rec.Status != store.AgentBugReportStatusInProgress {
		return nil
	}
	if err := r.Reports.SetAgentBugReportFix(ctx, namespace, rec.ID, store.AgentBugReportFixUpdate{
		Status:      store.AgentBugReportStatusOpen,
		StatusActor: bugReportFixActor,
		StatusNote:  fmt.Sprintf("auto-fix run %s was deleted before finishing", runName),
	}); err != nil {
		return fmt.Errorf("reopening bug report %s after fix run deletion: %w", rec.ID, err)
	}
	logf.FromContext(ctx).Info("reopened bug report after fix run deletion", "report", rec.ID, "run", runName)
	return nil
}

// mapMonitorToImplementerRun requeues the implementer AgentRun whenever one of
// its pull-request monitors changes, so merge/close transitions propagate to
// the linked bug report promptly.
func mapMonitorToImplementerRun(_ context.Context, obj client.Object) []reconcile.Request {
	monitor, ok := obj.(*triggersv1alpha1.PullRequestMonitor)
	if !ok || monitor.Spec.ImplementerRef.Name == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: client.ObjectKey{Namespace: monitor.Namespace, Name: monitor.Spec.ImplementerRef.Name},
	}}
}

// bugReportRunPredicate keeps this controller quiet for the overwhelming
// majority of AgentRuns, which carry no bug-report linkage. Delete events pass
// so a removed fix run reopens its report.
func bugReportRunPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetLabels()[platformv1alpha1.BugReportIDLabel] != ""
	})
}

func (r *BugReportFixReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.AgentRun{}, builder.WithPredicates(bugReportRunPredicate())).
		Watches(&triggersv1alpha1.PullRequestMonitor{}, handler.EnqueueRequestsFromMapFunc(mapMonitorToImplementerRun)).
		Named("bug-report-fix").
		Complete(r)
}
