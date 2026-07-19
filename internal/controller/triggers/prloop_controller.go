package triggers

import (
	"context"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// PRLoopReconciler advances the autonomous PR review loop on run completions:
// finished reviewer runs deliver their verdict, finished implementer runs in
// the resolving state trigger the next review round.
type PRLoopReconciler struct {
	client.Client
	Engine *PRLoopEngine
}

// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch;create;update;patch

func (r *PRLoopReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if r.Engine == nil || !prLoopRunCompleted(run) {
		return ctrl.Result{}, nil
	}

	if run.Labels[PRLoopRoleLabel] == PRLoopRoleReviewer {
		return ctrl.Result{}, r.Engine.OnReviewerRunCompleted(ctx, run)
	}
	return ctrl.Result{}, r.Engine.OnImplementerRunCompleted(ctx, run)
}

// prLoopRunCompleted reports whether the run both participates in the loop and
// has reached a phase the loop reacts to.
func prLoopRunCompleted(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	inLoop := run.Labels[PRLoopRoleLabel] == PRLoopRoleReviewer || run.Labels[PRLoopStateLabel] != ""
	if !inLoop {
		return false
	}
	switch run.Status.Phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed:
		return true
	default:
		return false
	}
}

// prLoopPredicate filters watch events down to loop-labeled runs so the
// reconciler does not churn on every AgentRun in the cluster.
func prLoopPredicate() predicate.Predicate {
	isLoopRun := func(obj client.Object) bool {
		labels := obj.GetLabels()
		return labels[PRLoopRoleLabel] != "" || labels[PRLoopStateLabel] != ""
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isLoopRun(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isLoopRun(e.ObjectNew) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return isLoopRun(e.Object) },
	}
}

func (r *PRLoopReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.AgentRun{}).
		Named("prloop").
		WithEventFilter(prLoopPredicate()).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
