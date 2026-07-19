package main

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestLiveRuntimeModelAndProviderUsesRunSpec(t *testing.T) {
	cfg := runConfig{Model: "anthropic/claude-sonnet-4-6", Provider: "anthropic"}
	run := &platformv1alpha1.AgentRun{Spec: platformv1alpha1.AgentRunSpec{Model: "openrouter/openai/gpt-5"}}

	model, provider := liveRuntimeModelAndProvider(cfg, run)
	if model != "openrouter/openai/gpt-5" || provider != "openrouter" {
		t.Fatalf("liveRuntimeModelAndProvider() = %q/%q, want openrouter/openai/gpt-5/openrouter", model, provider)
	}
}

func TestLiveRuntimeModelAndProviderPrefixesOpenAIWhenStartupProviderDiffers(t *testing.T) {
	cfg := runConfig{Model: "anthropic/claude-sonnet-4-6", Provider: "anthropic"}
	run := &platformv1alpha1.AgentRun{Spec: platformv1alpha1.AgentRunSpec{Model: "gpt-5.4"}}

	model, provider := liveRuntimeModelAndProvider(cfg, run)
	if model != "openai/gpt-5.4" || provider != "openai" {
		t.Fatalf("liveRuntimeModelAndProvider() = %q/%q, want openai/gpt-5.4/openai", model, provider)
	}
}

func TestLiveRuntimeModelAndProviderKeepsStartupDefaultsWithoutRunModel(t *testing.T) {
	cfg := runConfig{Model: "gpt-5.4", Provider: "openai"}

	model, provider := liveRuntimeModelAndProvider(cfg, nil)
	if model != "gpt-5.4" || provider != "openai" {
		t.Fatalf("liveRuntimeModelAndProvider() = %q/%q, want gpt-5.4/openai", model, provider)
	}
}

func TestAutoModeFromRunAlwaysUsesAutonomousPacing(t *testing.T) {
	runs := []*platformv1alpha1.AgentRun{
		nil,
		{},
		{Spec: platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat}},
		{Status: platformv1alpha1.AgentRunStatus{ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{Autonomous: false}}},
	}
	for _, run := range runs {
		if !autoModeFromRun(run) {
			t.Fatalf("autoModeFromRun(%#v) = false, want true", run)
		}
	}
}

func TestResetAutoLoopForSteeringStartsFreshBudgetAndTracker(t *testing.T) {
	loopCount := agent.DefaultMaxAutoLoops
	original := &agent.AutoTracker{}
	tracker := original

	resetAutoLoopForSteering(&loopCount, &tracker)

	if loopCount != 1 {
		t.Fatalf("loopCount = %d, want 1 for the current steered pass", loopCount)
	}
	if tracker == original {
		t.Fatal("tracker was not replaced; stale continuation state would leak into the steered turn")
	}
}

func TestIsDelegatedChildFromCRD(t *testing.T) {
	tests := []struct {
		name string
		meta metav1.ObjectMeta
		want bool
	}{
		{name: "team parent label", meta: metav1.ObjectMeta{Labels: map[string]string{teamParentLabel: "parent"}}, want: true},
		{name: "AgentRun owner", meta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: "parent"}}}, want: true},
		{name: "wrong API version", meta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "AgentRun", Name: "parent"}}}},
		{name: "wrong kind", meta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "Pod", Name: "parent"}}}},
		{name: "empty owner name", meta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := platformv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("AddToScheme: %v", err)
			}
			tt.meta.Name = "child"
			tt.meta.Namespace = "default"
			run := &platformv1alpha1.AgentRun{ObjectMeta: tt.meta}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
			if got := isDelegatedChildFromCRD(context.Background(), c, run.Name, run.Namespace); got != tt.want {
				t.Fatalf("isDelegatedChildFromCRD() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRestartPending(t *testing.T) {
	if restartPending(nil) {
		t.Fatal("restartPending(nil) = true, want false")
	}
	run := &platformv1alpha1.AgentRun{}
	if restartPending(run) {
		t.Fatal("restartPending(zero run) = true, want false")
	}
	run.Spec.RestartRequests = 2
	run.Status.RestartRequestsHandled = 1
	if !restartPending(run) {
		t.Fatal("restartPending(requests > handled) = false, want true")
	}
	run.Status.RestartRequestsHandled = 2
	if restartPending(run) {
		t.Fatal("restartPending(handled caught up) = true, want false")
	}
}
