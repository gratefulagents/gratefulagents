package agentrun

import (
	"errors"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func TestValidateTeamParent(t *testing.T) {
	newRun := func() *platformv1alpha1.AgentRun {
		return &platformv1alpha1.AgentRun{
			Spec: platformv1alpha1.AgentRunSpec{
				WorkflowMode:  platformv1alpha1.WorkflowModeChat,
				ExecutionMode: platformv1alpha1.ExecutionModeTeam,
				Team: &platformv1alpha1.AgentRunTeamSpec{
					Steps: []platformv1alpha1.AgentRunTeamStep{
						{Name: "parallel-implementers", Type: platformv1alpha1.TeamStepTypeParallel},
					},
					DelegationPolicy: &platformv1alpha1.AgentRunDelegationPolicy{
						ParentOnly: true,
					},
				},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*platformv1alpha1.AgentRun)
		wantErr error
	}{
		{
			name: "accepts chat parent",
		},
		{
			name: "requires team execution mode",
			mutate: func(run *platformv1alpha1.AgentRun) {
				run.Spec.ExecutionMode = platformv1alpha1.ExecutionModeLinear
			},
			wantErr: ErrExecutionModeNotTeam,
		},
		{
			name: "allows ad-hoc chat parent without team mode",
			mutate: func(run *platformv1alpha1.AgentRun) {
				run.Spec.WorkflowMode = platformv1alpha1.WorkflowModeChat
				run.Spec.ExecutionMode = platformv1alpha1.ExecutionModeLinear
				run.Spec.Team = nil
			},
		},
		{
			name: "allows ad-hoc auto parent without team spec",
			mutate: func(run *platformv1alpha1.AgentRun) {
				run.Spec.WorkflowMode = platformv1alpha1.WorkflowModeAuto
				run.Spec.Team = nil
			},
		},
		{
			name: "accepts deprecated plan workflow",
			mutate: func(run *platformv1alpha1.AgentRun) {
				run.Spec.WorkflowMode = platformv1alpha1.WorkflowModeChat
			},
		},
		{
			name: "enforces parent only delegation",
			mutate: func(run *platformv1alpha1.AgentRun) {
				run.Spec.Team.DelegationPolicy.ParentOnly = false
			},
			wantErr: ErrParentOnlyDelegation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := newRun()
			if tc.mutate != nil {
				tc.mutate(run)
			}

			err := ValidateTeamParent(run)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateTeamParent() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateTeamParent() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestRuntimeParentBindingFromEnv(t *testing.T) {
	t.Setenv("AGENTRUN_CURRENT_NAMESPACE", "engg")
	t.Setenv("AGENTRUN_CURRENT_NAME", "child-a")
	t.Setenv("AGENTRUN_CURRENT_UID", "uid-child")
	t.Setenv("AGENTRUN_PARENT_NAMESPACE", "engg")
	t.Setenv("AGENTRUN_PARENT_NAME", "parent-run")
	t.Setenv("AGENTRUN_PARENT_UID", "uid-parent")

	binding, ok, err := RuntimeParentBindingFromEnv()
	if err != nil {
		t.Fatalf("RuntimeParentBindingFromEnv() error = %v", err)
	}
	if !ok {
		t.Fatal("RuntimeParentBindingFromEnv() ok = false, want true")
	}
	if binding.Parent.Namespace != "engg" || binding.Parent.Name != "parent-run" || binding.ParentUID != "uid-parent" {
		t.Fatalf("binding = %#v, want parent engg/parent-run uid-parent", binding)
	}
}

func TestValidateRuntimeParentBinding(t *testing.T) {
	t.Setenv("AGENTRUN_PARENT_NAMESPACE", "engg")
	t.Setenv("AGENTRUN_PARENT_NAME", "parent-run")

	if err := ValidateRuntimeParentBinding(ParentRunRef{Namespace: "engg", Name: "parent-run"}); err != nil {
		t.Fatalf("ValidateRuntimeParentBinding() error = %v", err)
	}
	err := ValidateRuntimeParentBinding(ParentRunRef{Namespace: "default", Name: "main"})
	if !errors.Is(err, ErrParentScopeMismatch) {
		t.Fatalf("ValidateRuntimeParentBinding() error = %v, want %v", err, ErrParentScopeMismatch)
	}
}
