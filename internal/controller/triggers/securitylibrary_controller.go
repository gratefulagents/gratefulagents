package triggers

import (
	"context"
	"slices"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// SecurityLibraryReconciler validates the reusable security library resources
// (SecurityWorkflow, SecurityRanker, SecurityPostScript, SecurityPolicyPack,
// SecurityProgram) and keeps their
// status current: observedGeneration, a Ready condition, and how many
// SecurityScans in the namespace reference them.
type SecurityLibraryReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=securityworkflows/status;securityrankers/status;securitypostscripts/status;securitypolicypacks/status;securityprograms/status,verbs=get;update;patch

// securityScanRefNames extracts the names a scan references for one library
// kind.
func securityScanRefNames(scan *triggersv1alpha1.SecurityScan, kind string) []string {
	switch kind {
	case "SecurityWorkflow":
		if scan.Spec.WorkflowRef != nil {
			return []string{scan.Spec.WorkflowRef.Name}
		}
	case "SecurityRanker":
		names := make([]string, 0, len(scan.Spec.RankerRefs))
		for _, ref := range scan.Spec.RankerRefs {
			names = append(names, ref.Name)
		}
		return names
	case "SecurityPostScript":
		names := make([]string, 0, len(scan.Spec.PostScriptRefs))
		for _, ref := range scan.Spec.PostScriptRefs {
			names = append(names, ref.Name)
		}
		return names
	case "SecurityPolicyPack":
		if scan.Spec.PolicyPackRef != nil {
			return []string{scan.Spec.PolicyPackRef.Name}
		}
	case "SecurityProgram":
		if scan.Spec.SecurityProgramRef != nil {
			return []string{scan.Spec.SecurityProgramRef.Name}
		}
	}
	return nil
}

// countSecurityLibraryReferences returns how many SecurityScans in the
// namespace reference the named resource of the given kind, plus the
// referencing scan names.
func countSecurityLibraryReferences(
	ctx context.Context, c client.Reader, namespace, kind, name string,
) ([]string, error) {
	scans := &triggersv1alpha1.SecurityScanList{}
	if err := c.List(ctx, scans, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	var referencing []string
	for i := range scans.Items {
		scan := &scans.Items[i]
		if slices.Contains(securityScanRefNames(scan, kind), name) {
			referencing = append(referencing, scan.Name)
		}
	}
	return referencing, nil
}

func (r *SecurityLibraryReconciler) reconcileStatus(
	ctx context.Context, obj client.Object, kind string,
	status *triggersv1alpha1.SecurityLibraryResourceStatus,
	validationErrs []triggersv1alpha1.SecurityWorkflowFieldError,
) error {
	referencing, err := countSecurityLibraryReferences(ctx, r.Client, obj.GetNamespace(), kind, obj.GetName())
	if err != nil {
		return err
	}
	status.ObservedGeneration = obj.GetGeneration()
	status.ReferencedBy = int32(len(referencing)) //nolint:gosec // scan counts stay far below int32 bounds
	condition := metav1.Condition{
		Type:               triggersv1alpha1.ConditionSecurityLibraryReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: obj.GetGeneration(),
		Reason:             "Validated",
		Message:            "spec is valid",
	}
	if len(validationErrs) != 0 {
		condition.Status = metav1.ConditionFalse
		condition.Reason = securityScanReasonInvalidSpec
		condition.Message = validationErrs[0].Error()
	}
	meta.SetStatusCondition(&status.Conditions, condition)
	return r.Status().Update(ctx, obj)
}

// mapScanToSecurityLibrary enqueues the library resources a SecurityScan
// references, so referencedBy counts follow scan edits.
func mapScanToSecurityLibrary(kind string) handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		scan, ok := obj.(*triggersv1alpha1.SecurityScan)
		if !ok {
			return nil
		}
		var requests []reconcile.Request
		for _, name := range securityScanRefNames(scan, kind) {
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKey{Namespace: scan.Namespace, Name: name},
			})
		}
		return requests
	}
}

// SecurityWorkflowReconciler validates SecurityWorkflow resources.
type SecurityWorkflowReconciler struct{ SecurityLibraryReconciler }

func (r *SecurityWorkflowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	workflow := &triggersv1alpha1.SecurityWorkflow{}
	if err := r.Get(ctx, req.NamespacedName, workflow); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	errs := triggersv1alpha1.ValidateSecurityWorkflowTasks(workflow.Spec.Tasks)
	errs = append(errs, triggersv1alpha1.ValidateSecurityWorkflowParameters(workflow.Spec.Parameters)...)
	return ctrl.Result{}, client.IgnoreNotFound(
		r.reconcileStatus(ctx, workflow, "SecurityWorkflow", &workflow.Status, errs))
}

func (r *SecurityWorkflowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.SecurityWorkflow{}).
		Watches(&triggersv1alpha1.SecurityScan{}, handler.EnqueueRequestsFromMapFunc(mapScanToSecurityLibrary("SecurityWorkflow"))).
		Named("securityworkflow").
		Complete(r)
}

// SecurityRankerReconciler validates SecurityRanker resources.
type SecurityRankerReconciler struct{ SecurityLibraryReconciler }

func (r *SecurityRankerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ranker := &triggersv1alpha1.SecurityRanker{}
	if err := r.Get(ctx, req.NamespacedName, ranker); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	errs := triggersv1alpha1.ValidateSecurityRankerRules(ranker.Spec.Rules)
	return ctrl.Result{}, client.IgnoreNotFound(
		r.reconcileStatus(ctx, ranker, "SecurityRanker", &ranker.Status, errs))
}

func (r *SecurityRankerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.SecurityRanker{}).
		Watches(&triggersv1alpha1.SecurityScan{}, handler.EnqueueRequestsFromMapFunc(mapScanToSecurityLibrary("SecurityRanker"))).
		Named("securityranker").
		Complete(r)
}

// SecurityPostScriptReconciler validates SecurityPostScript resources.
type SecurityPostScriptReconciler struct{ SecurityLibraryReconciler }

func (r *SecurityPostScriptReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	script := &triggersv1alpha1.SecurityPostScript{}
	if err := r.Get(ctx, req.NamespacedName, script); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	errs := triggersv1alpha1.ValidateSecurityPostScriptSpec(script.Spec)
	return ctrl.Result{}, client.IgnoreNotFound(
		r.reconcileStatus(ctx, script, "SecurityPostScript", &script.Status, errs))
}

func (r *SecurityPostScriptReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.SecurityPostScript{}).
		Watches(&triggersv1alpha1.SecurityScan{}, handler.EnqueueRequestsFromMapFunc(mapScanToSecurityLibrary("SecurityPostScript"))).
		Named("securitypostscript").
		Complete(r)
}

// SecurityPolicyPackReconciler validates SecurityPolicyPack resources.
type SecurityPolicyPackReconciler struct{ SecurityLibraryReconciler }

func (r *SecurityPolicyPackReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pack := &triggersv1alpha1.SecurityPolicyPack{}
	if err := r.Get(ctx, req.NamespacedName, pack); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	errs := triggersv1alpha1.ValidateSecurityPolicyPackSpec(pack.Spec)
	return ctrl.Result{}, client.IgnoreNotFound(
		r.reconcileStatus(ctx, pack, "SecurityPolicyPack", &pack.Status, errs))
}

func (r *SecurityPolicyPackReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.SecurityPolicyPack{}).
		Watches(&triggersv1alpha1.SecurityScan{}, handler.EnqueueRequestsFromMapFunc(mapScanToSecurityLibrary("SecurityPolicyPack"))).
		Named("securitypolicypack").
		Complete(r)
}

// SecurityProgramReconciler validates SecurityProgram resources and records
// the digest scans must match before dispatch.
type SecurityProgramReconciler struct{ SecurityLibraryReconciler }

func (r *SecurityProgramReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	program := &triggersv1alpha1.SecurityProgram{}
	if err := r.Get(ctx, req.NamespacedName, program); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	referencing, err := countSecurityLibraryReferences(ctx, r.Client, program.Namespace, "SecurityProgram", program.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	previousStatus := program.Status.DeepCopy()
	errs := triggersv1alpha1.ValidateSecurityProgramSpec(program.Spec)
	program.Status.ObservedGeneration = program.Generation
	program.Status.ContentDigest = securitySpecHash(program.Spec)
	program.Status.ReferencedBy = int32(len(referencing)) //nolint:gosec // scan counts stay far below int32 bounds
	condition := metav1.Condition{
		Type:               triggersv1alpha1.ConditionSecurityLibraryReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: program.Generation,
		Reason:             "Validated",
		Message:            "spec is valid",
	}
	if len(errs) != 0 {
		condition.Status = metav1.ConditionFalse
		condition.Reason = securityScanReasonInvalidSpec
		condition.Message = errs[0].Error()
	}
	meta.SetStatusCondition(&program.Status.Conditions, condition)
	if apiequality.Semantic.DeepEqual(previousStatus, &program.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, client.IgnoreNotFound(r.Status().Update(ctx, program))
}

func (r *SecurityProgramReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.SecurityProgram{}).
		Watches(&triggersv1alpha1.SecurityScan{}, handler.EnqueueRequestsFromMapFunc(mapScanToSecurityLibrary("SecurityProgram"))).
		Named("securityprogram").
		Complete(r)
}
