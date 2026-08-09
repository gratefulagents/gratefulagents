package platform

import (
	"context"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestManagedSandboxTemplateNameSeparatesLongRunRetries(t *testing.T) {
	first := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: "secscan-securityscan-sxz2ew-generation-1-ps-pipeline-9-53a5085d",
		UID:  types.UID("709192fd-f7b6-4215-818b-b0fa23945c13"),
	}}
	second := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: "secscan-securityscan-sxz2ew-generation-1-ps-pipeline-9-634b314a",
		UID:  types.UID("d7cc4293-486b-4a87-bf98-3b4ce36210f9"),
	}}
	firstName := managedSandboxTemplateName(first)
	secondName := managedSandboxTemplateName(second)
	if firstName == secondName {
		t.Fatalf("long retry template names collided at %q", firstName)
	}
	if len(firstName) > 63 || len(secondName) > 63 {
		t.Fatalf("template names exceed 63 characters: %q (%d), %q (%d)", firstName, len(firstName), secondName, len(secondName))
	}
	if got := managedSandboxTemplateName(&platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "short-run", UID: types.UID("uid")}}); got != "run-tpl-short-run" {
		t.Fatalf("short template name = %q, want backward-compatible name", got)
	}
	collapsed := managedSandboxTemplateName(&platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: "a" + strings.Repeat("-", 54) + "b", UID: types.UID("collapsed-run-uid"),
	}})
	if len(collapsed) > 63 || !strings.HasPrefix(collapsed, "run-tpl-a-b-") {
		t.Fatalf("collapsed long template name = %q (%d), want bounded hashed name", collapsed, len(collapsed))
	}
}

func TestEnsureRunSandboxTemplateRejectsForeignController(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := extensionsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(sandbox extensions): %v", err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: "collision-run", Namespace: "default", UID: types.UID("new-run-uid"),
	}}
	controller := true
	conflicting := &extensionsv1alpha1.SandboxTemplate{ObjectMeta: metav1.ObjectMeta{
		Name:      managedSandboxTemplateName(run),
		Namespace: run.Namespace,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun",
			Name: run.Name, UID: types.UID("old-run-uid"), Controller: &controller,
		}},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(conflicting).Build()
	if _, err := ensureRunSandboxTemplate(context.Background(), c, run, nil, "run-sa", ""); err == nil || !strings.Contains(err.Error(), "belongs to another AgentRun") {
		t.Fatalf("ensureRunSandboxTemplate() error = %v, want owner collision", err)
	}
}

func TestSandboxTemplateOwnershipRequiresControllerReference(t *testing.T) {
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", UID: types.UID("run-uid")}}
	controller := true
	controlled := &extensionsv1alpha1.SandboxTemplate{ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{
		APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: run.Name, UID: run.UID, Controller: &controller,
	}}}}
	if !sandboxTemplateOwnedByRun(controlled, run) {
		t.Fatal("controller-owned template was rejected")
	}
	secondary := controlled.DeepCopy()
	secondary.OwnerReferences[0].Controller = nil
	if sandboxTemplateOwnedByRun(secondary, run) {
		t.Fatal("secondary owner reference authorized template reuse")
	}
}
