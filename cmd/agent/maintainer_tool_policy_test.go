package main

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	internaltools "github.com/gratefulagents/gratefulagents/internal/tools"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	maintainerPolicyTestNamespace = "default"
	maintainerPolicyTypedTool     = "request_merge"
)

func TestEffectiveRuntimeAllowedMutatingToolsFollowsLiveMaintainerCutover(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	repository := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: maintainerPolicyTestNamespace},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Maintainer: &triggersv1alpha1.MaintainerSpec{
				WorkItemCutover: triggersv1alpha1.MaintainerWorkItemCutoverController,
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(repository).Build()
	run := maintainerPolicyTestRun()

	assertLegacyTools := func(want bool) {
		t.Helper()
		allowed := effectiveRuntimeAllowedMutatingTools(
			context.Background(), k8sClient, run, repository.Name, repository.Namespace,
		)
		set := make(map[string]bool, len(allowed))
		for _, name := range allowed {
			set[name] = true
		}
		if !set[maintainerPolicyTypedTool] {
			t.Fatalf("normal mode exception was lost: %v", allowed)
		}
		for _, name := range internaltools.MaintainerLegacyMutationToolNames() {
			if set[name] != want {
				t.Fatalf("legacy tool %s present=%v, want %v in %v", name, set[name], want, allowed)
			}
		}
	}

	assertLegacyTools(false)
	rollbackModes := []triggersv1alpha1.MaintainerWorkItemCutoverMode{
		triggersv1alpha1.MaintainerWorkItemCutoverLegacy,
		triggersv1alpha1.MaintainerWorkItemCutoverDualRead,
	}
	for _, mode := range rollbackModes {
		current := &triggersv1alpha1.GitHubRepository{}
		if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(repository), current); err != nil {
			t.Fatal(err)
		}
		current.Spec.Maintainer.WorkItemCutover = mode
		if err := k8sClient.Update(context.Background(), current); err != nil {
			t.Fatal(err)
		}
		assertLegacyTools(true)
	}
	current := &triggersv1alpha1.GitHubRepository{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(repository), current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Maintainer.WorkItemCutover = triggersv1alpha1.MaintainerWorkItemCutoverController
	if err := k8sClient.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	assertLegacyTools(false)
}

func TestEffectiveRuntimeAllowedMutatingToolsFailsClosedOnRepositoryReadError(t *testing.T) {
	allowed := effectiveRuntimeAllowedMutatingTools(
		context.Background(), fake.NewClientBuilder().Build(), maintainerPolicyTestRun(),
		"missing", maintainerPolicyTestNamespace,
	)
	if len(allowed) != 1 || allowed[0] != maintainerPolicyTypedTool {
		t.Fatalf("repository read failure authorized unexpected tools: %v", allowed)
	}
}

func maintainerPolicyTestRun() *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		Status: platformv1alpha1.AgentRunStatus{
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				AllowedMutatingTools: []string{maintainerPolicyTypedTool},
			},
		},
	}
}
