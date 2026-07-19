package main

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReadModeOverridesUsesResolvedSnapshotNameForLiveInstructions(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-chat", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			ModeRef: &platformv1alpha1.ModeRef{Name: "chat"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				Name:         "autopilot",
				Instructions: "pinned autopilot instructions",
			},
		},
	}
	chat := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "chat"},
		Spec: platformv1alpha1.ModeTemplateSpec{
			Name:         "chat",
			Instructions: "legacy chat instructions",
		},
	}
	autopilot := &platformv1alpha1.ModeTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "autopilot"},
		Spec: platformv1alpha1.ModeTemplateSpec{
			Name:         "autopilot",
			Instructions: "live autopilot instructions that require finish",
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, chat, autopilot).Build()
	modeInstrCache = modeInstructionsCache{}

	overrides := readModeOverrides(context.Background(), client, run.Name, run.Namespace)
	if overrides.ModeInstructions != autopilot.Spec.Instructions {
		t.Fatalf("ModeInstructions = %q, want resolved autopilot instructions %q", overrides.ModeInstructions, autopilot.Spec.Instructions)
	}
}
