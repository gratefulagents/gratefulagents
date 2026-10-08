package platform

import (
	"context"
	"reflect"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func retiredRoleTestObject(name string, seeded bool) *platformv1alpha1.RoleInstruction {
	role := &platformv1alpha1.RoleInstruction{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       platformv1alpha1.RoleInstructionSpec{Instructions: name},
	}
	if seeded {
		role.Annotations = map[string]string{bootstrapDefaultAnnotation: "true"}
	}
	return role
}

func TestRetiredRoleInstructionCleanupDeletesOnlySeededRetiredRoles(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		retiredRoleTestObject("executor", true),
		retiredRoleTestObject("critic", true),
		// A user-created role that happens to reuse a retired name.
		retiredRoleTestObject("planner", false),
		// Shipped roles stay even though they carry the bootstrap annotation.
		retiredRoleTestObject("explore", true),
		retiredRoleTestObject("general", true),
	).Build()

	cleanup := &RetiredRoleInstructionCleanup{Client: c}
	deleted, err := cleanup.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if want := []string{"critic", "executor"}; !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
	for _, name := range []string{"executor", "critic"} {
		err := c.Get(context.Background(), client.ObjectKey{Name: name}, &platformv1alpha1.RoleInstruction{})
		if !k8serrors.IsNotFound(err) {
			t.Fatalf("%s still exists or lookup failed: %v", name, err)
		}
	}
	for _, name := range []string{"planner", "explore", "general"} {
		if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &platformv1alpha1.RoleInstruction{}); err != nil {
			t.Fatalf("%s should be kept: %v", name, err)
		}
	}

	deleted, err = cleanup.Sweep(context.Background())
	if err != nil || len(deleted) != 0 {
		t.Fatalf("second Sweep = %v, %v; want a no-op", deleted, err)
	}
}
