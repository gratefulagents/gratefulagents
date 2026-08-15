/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package triggers

import (
	"context"
	"fmt"

	"github.com/google/uuid"
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
// the report when that PR merges (observed through the run's
// PullRequestMonitor), and reopens the report when the fix attempt ends
// without a mergeable result.
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
		return ctrl.Result{}, client.IgnoreNotFound(err)
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

	// Record the fix PR as soon as the run opens one.
	if prs := artifactPullRequests(run); len(prs) > 0 && rec.FixPRURL == "" {
		fixPRURL := prs[0].URL
		if err := reports.SetAgentBugReportFix(ctx, run.Namespace, reportID, store.AgentBugReportFixUpdate{
			FixPRURL: &fixPRURL,
		}); err != nil {
			return ctrl.Result{}, fmt.Errorf("recording fix PR on bug report %s: %w", reportID, err)
		}
		rec.FixPRURL = fixPRURL
		log.Info("recorded bug report fix PR", "report", reportID, "run", run.Name, "pr", fixPRURL)
	}

	// Reports the fix already finished with (or a human re-triaged) need no
	// further lifecycle transitions.
	if rec.Status != store.AgentBugReportStatusInProgress {
		return ctrl.Result{}, nil
	}

	if rec.FixPRURL != "" {
		monitor, err := r.fixPullRequestMonitor(ctx, run, rec.FixPRURL)
		if err != nil {
			return ctrl.Result{}, err
		}
		if monitor != nil {
			switch monitor.Status.Lifecycle {
			case triggersv1alpha1.PullRequestLifecycleMerged:
				note := fmt.Sprintf("auto-fixed by %s (merged)", rec.FixPRURL)
				if !monitor.Status.MergedAt.IsZero() {
					note = fmt.Sprintf("auto-fixed by %s (merged %s)", rec.FixPRURL, monitor.Status.MergedAt.UTC().Format("2006-01-02 15:04 UTC"))
				}
				if err := reports.SetAgentBugReportFix(ctx, run.Namespace, reportID, store.AgentBugReportFixUpdate{
					Status:      store.AgentBugReportStatusResolved,
					StatusActor: bugReportFixActor,
					StatusNote:  note,
				}); err != nil {
					return ctrl.Result{}, fmt.Errorf("resolving bug report %s: %w", reportID, err)
				}
				log.Info("resolved bug report after fix PR merge", "report", reportID, "pr", rec.FixPRURL)
			case triggersv1alpha1.PullRequestLifecycleClosed:
				if err := reports.SetAgentBugReportFix(ctx, run.Namespace, reportID, store.AgentBugReportFixUpdate{
					Status:      store.AgentBugReportStatusOpen,
					StatusActor: bugReportFixActor,
					StatusNote:  fmt.Sprintf("fix PR %s was closed without merging", rec.FixPRURL),
				}); err != nil {
					return ctrl.Result{}, fmt.Errorf("reopening bug report %s: %w", reportID, err)
				}
				log.Info("reopened bug report after fix PR closed unmerged", "report", reportID, "pr", rec.FixPRURL)
			}
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

// fixPullRequestMonitor returns the run's PullRequestMonitor for the recorded
// fix PR URL, or nil when it does not exist (yet).
func (r *BugReportFixReconciler) fixPullRequestMonitor(ctx context.Context, run *platformv1alpha1.AgentRun, fixPRURL string) (*triggersv1alpha1.PullRequestMonitor, error) {
	monitor := &triggersv1alpha1.PullRequestMonitor{}
	key := client.ObjectKey{Namespace: run.Namespace, Name: pullRequestMonitorName(run.UID, fixPRURL)}
	if err := r.Get(ctx, key, monitor); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return monitor, nil
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
// majority of AgentRuns, which carry no bug-report linkage.
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
