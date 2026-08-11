package triggers

import (
	"context"
	"testing"
	"time"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSecurityProgramReconcilerStatus(t *testing.T) {
	program := &triggersv1alpha1.SecurityProgram{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-bounty", Namespace: "tenant", Generation: 3},
		Spec: triggersv1alpha1.SecurityProgramSpec{
			Provider:    "HackerOne",
			DisplayName: "Acme Bug Bounty",
			ProgramURL:  "https://hackerone.com/acme",
			ScopePolicy: "Only acme/widget is in scope.",
			VerifiedAt:  metav1.NewTime(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)),
		},
	}
	scan := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "tenant"},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL:            "https://github.com/acme/widget.git",
			SecurityProgramRef: &triggersv1alpha1.SecurityResourceRef{Name: program.Name},
		},
	}
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.SecurityProgram{}).
		WithObjects(program, scan).Build()
	r := &SecurityProgramReconciler{SecurityLibraryReconciler: SecurityLibraryReconciler{Client: c}}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(program)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	got := &triggersv1alpha1.SecurityProgram{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(program), got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ObservedGeneration != program.Generation || got.Status.ReferencedBy != 1 || got.Status.ContentDigest != securitySpecHash(program.Spec) {
		t.Fatalf("status = %+v", got.Status)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, triggersv1alpha1.ConditionSecurityLibraryReady)
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != program.Generation {
		t.Fatalf("Ready condition = %+v", ready)
	}
	settledVersion := got.ResourceVersion
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(program)}); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(program), got); err != nil {
		t.Fatal(err)
	}
	if got.ResourceVersion != settledVersion {
		t.Fatalf("unchanged reconciliation wrote status: resourceVersion %q -> %q", settledVersion, got.ResourceVersion)
	}
}
