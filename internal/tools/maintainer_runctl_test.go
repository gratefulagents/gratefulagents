package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestExtendRunTimeout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		run         *platformv1alpha1.AgentRun
		input       string
		wantError   bool
		wantRuntime time.Duration
	}{
		{
			name:        "platform default",
			run:         fleetRun("target", platformv1alpha1.AgentRunPhaseRunning),
			input:       `{"run_name":"target","max_runtime":"6h"}`,
			wantRuntime: 6 * time.Hour,
		},
		{
			name: "reviewer allowed",
			run: func() *platformv1alpha1.AgentRun {
				run := fleetRun("target", platformv1alpha1.AgentRunPhaseRunning)
				run.Labels = map[string]string{triggersv1alpha1.PRLoopRoleLabelKey: triggersv1alpha1.PRLoopRoleReviewerValue}
				return run
			}(),
			input:       `{"run_name":"target","max_runtime":"6h"}`,
			wantRuntime: 6 * time.Hour,
		},
		{
			name:      "reject zero",
			run:       fleetRun("target", platformv1alpha1.AgentRunPhaseRunning),
			input:     `{"run_name":"target","max_runtime":"0s"}`,
			wantError: true,
		},
		{
			name:      "reject over maximum",
			run:       fleetRun("target", platformv1alpha1.AgentRunPhaseRunning),
			input:     `{"run_name":"target","max_runtime":"73h"}`,
			wantError: true,
		},
		{
			name: "reject shrink",
			run: func() *platformv1alpha1.AgentRun {
				run := fleetRun("target", platformv1alpha1.AgentRunPhaseRunning)
				run.Spec.Limits = &platformv1alpha1.AgentRunLimits{MaxRuntime: metav1.Duration{Duration: 6 * time.Hour}}
				return run
			}(),
			input:     `{"run_name":"target","max_runtime":"5h"}`,
			wantError: true,
		},
		{
			name:      "reject terminal",
			run:       fleetRun("target", platformv1alpha1.AgentRunPhaseSucceeded),
			input:     `{"run_name":"target","max_runtime":"6h"}`,
			wantError: true,
		},
		{
			name: "reject foreign run",
			run: func() *platformv1alpha1.AgentRun {
				run := fleetRun("target", platformv1alpha1.AgentRunPhaseRunning)
				run.Spec.Trigger.Name = "other"
				return run
			}(),
			input:     `{"run_name":"target","max_runtime":"6h"}`,
			wantError: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun(), tc.run)
			result, err := (&extendRunTimeoutTool{maintainerToolBase: base}).Execute(context.Background(), json.RawMessage(tc.input), "")
			if err != nil || result.IsError != tc.wantError {
				t.Fatalf("Execute() = (%#v, %v), want error %t", result, err, tc.wantError)
			}
			updated := &platformv1alpha1.AgentRun{}
			if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: tc.run.Name, Namespace: tc.run.Namespace}, updated); err != nil {
				t.Fatal(err)
			}
			if tc.wantError {
				if tc.run.Spec.Limits == nil {
					if updated.Spec.Limits != nil {
						t.Fatalf("limits changed after rejected request: %#v", updated.Spec.Limits)
					}
				} else if updated.Spec.Limits == nil || updated.Spec.Limits.MaxRuntime.Duration != tc.run.Spec.Limits.MaxRuntime.Duration {
					t.Fatalf("timeout changed after rejected request: %#v", updated.Spec.Limits)
				}
				return
			}
			if updated.Spec.Limits == nil || updated.Spec.Limits.MaxRuntime.Duration != tc.wantRuntime {
				t.Fatalf("limits = %#v, want max runtime %s", updated.Spec.Limits, tc.wantRuntime)
			}
			if !strings.Contains(result.Content, "platform default → 6h0m0s") {
				t.Fatalf("result = %q, want old and new timeouts", result.Content)
			}
		})
	}
}

func TestMarkRunSucceeded(t *testing.T) {
	reason := "The implementation pull request has been merged."
	for _, tc := range []struct {
		name      string
		run       *platformv1alpha1.AgentRun
		input     string
		wantError bool
	}{
		{
			name:  "sets controller and reason annotations",
			run:   fleetRun("target", platformv1alpha1.AgentRunPhaseRunning),
			input: `{"run_name":"target","reason":"The implementation pull request has been merged."}`,
		},
		{
			name: "reject reviewer",
			run: func() *platformv1alpha1.AgentRun {
				run := fleetRun("target", platformv1alpha1.AgentRunPhaseRunning)
				run.Labels = map[string]string{triggersv1alpha1.PRLoopRoleLabelKey: triggersv1alpha1.PRLoopRoleReviewerValue}
				return run
			}(),
			input:     `{"run_name":"target","reason":"The implementation pull request has been merged."}`,
			wantError: true,
		},
		{
			name:      "reject terminal",
			run:       fleetRun("target", platformv1alpha1.AgentRunPhaseCancelled),
			input:     `{"run_name":"target","reason":"The implementation pull request has been merged."}`,
			wantError: true,
		},
		{
			name:      "reject short reason",
			run:       fleetRun("target", platformv1alpha1.AgentRunPhaseRunning),
			input:     `{"run_name":"target","reason":"too short"}`,
			wantError: true,
		},
		{
			name:      "reject long reason",
			run:       fleetRun("target", platformv1alpha1.AgentRunPhaseRunning),
			input:     `{"run_name":"target","reason":"` + strings.Repeat("x", 1001) + `"}`,
			wantError: true,
		},
		{
			name: "reject foreign run",
			run: func() *platformv1alpha1.AgentRun {
				run := fleetRun("target", platformv1alpha1.AgentRunPhaseRunning)
				run.Spec.Trigger.Name = "other"
				return run
			}(),
			input:     `{"run_name":"target","reason":"The implementation pull request has been merged."}`,
			wantError: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, k8sClient, _ := newMaintainerToolBase(t, maintainerRun(), tc.run)
			result, err := (&markRunSucceededTool{maintainerToolBase: base}).Execute(context.Background(), json.RawMessage(tc.input), "")
			if err != nil || result.IsError != tc.wantError {
				t.Fatalf("Execute() = (%#v, %v), want error %t", result, err, tc.wantError)
			}
			updated := &platformv1alpha1.AgentRun{}
			if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: tc.run.Name, Namespace: tc.run.Namespace}, updated); err != nil {
				t.Fatal(err)
			}
			if tc.wantError {
				if updated.Annotations[maintainerPromoteSucceededAnnotation] != "" || updated.Annotations[maintainerPromoteSucceededReason] != "" {
					t.Fatalf("annotations changed after rejected request: %#v", updated.Annotations)
				}
				return
			}
			if updated.Annotations[maintainerPromoteSucceededReason] != reason {
				t.Fatalf("reason annotation = %q, want %q", updated.Annotations[maintainerPromoteSucceededReason], reason)
			}
			if _, err := time.Parse(time.RFC3339, updated.Annotations[maintainerPromoteSucceededAnnotation]); err != nil {
				t.Fatalf("controller annotation = %q, want RFC3339 timestamp: %v", updated.Annotations[maintainerPromoteSucceededAnnotation], err)
			}
			if !strings.Contains(result.Content, "controller will transition it to Succeeded") {
				t.Fatalf("result = %q", result.Content)
			}
		})
	}
}
