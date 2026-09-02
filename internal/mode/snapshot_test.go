package mode

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func TestStatusSnapshotOmitsInstructionsButKeepsPinnedFields(t *testing.T) {
	spec := &platformv1alpha1.ModeTemplateSpec{
		Name:           "autopilot",
		Version:        "v3",
		Category:       platformv1alpha1.ModeCategory("execution"),
		PermissionMode: platformv1alpha1.PermissionModeReadOnly,
		Instructions:   "AUTOPILOT MODE — long prompt text",
		Constraints:    &platformv1alpha1.ModeConstraints{MaxTurns: 40},
	}
	got := StatusSnapshot(spec)
	if got == spec {
		t.Fatal("StatusSnapshot must return a copy")
	}
	if got.Instructions != "" {
		t.Fatalf("Instructions = %q, want omitted", got.Instructions)
	}
	if got.Name != "autopilot" || got.Version != "v3" || got.PermissionMode != platformv1alpha1.PermissionModeReadOnly || got.Constraints == nil || got.Constraints.MaxTurns != 40 {
		t.Fatalf("pinned fields lost: %+v", got)
	}
	if spec.Instructions == "" {
		t.Fatal("StatusSnapshot mutated its input")
	}
	if StatusSnapshot(nil) != nil {
		t.Fatal("StatusSnapshot(nil) must be nil")
	}
}

func TestAgentRunCacheTransformStripsInstructionsAfterNext(t *testing.T) {
	nextCalled := false
	transform := AgentRunCacheTransform(func(obj any) (any, error) {
		nextCalled = true
		return obj, nil
	})
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run"},
		Status: platformv1alpha1.AgentRunStatus{
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{Name: "autopilot", Instructions: "legacy pinned prompt"},
		},
	}
	out, err := transform(run)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if !nextCalled {
		t.Fatal("wrapped transform was not applied")
	}
	if got := out.(*platformv1alpha1.AgentRun); got.Status.ModeSnapshot.Instructions != "" || got.Status.ModeSnapshot.Name != "autopilot" {
		t.Fatalf("snapshot after transform = %+v, want instructions stripped, name kept", got.Status.ModeSnapshot)
	}

	// Other kinds and runs without a snapshot pass through untouched.
	tmpl := &platformv1alpha1.ModeTemplate{Spec: platformv1alpha1.ModeTemplateSpec{Instructions: "keep"}}
	if out, err := transform(tmpl); err != nil || out.(*platformv1alpha1.ModeTemplate).Spec.Instructions != "keep" {
		t.Fatalf("ModeTemplate was altered: %v %v", out, err)
	}
	if StripSnapshotInstructions(&platformv1alpha1.AgentRun{}) {
		t.Fatal("StripSnapshotInstructions reported a change on a run without a snapshot")
	}
}
