package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxMaintainerRunTimeout = 72 * time.Hour
	// Matches the canonical controller definition in internal/controller/platform/agentrun_controller.go.
	maintainerPromoteSucceededAnnotation = "platform.gratefulagents.dev/promote-succeeded-requested"
	maintainerPromoteSucceededReason     = "platform.gratefulagents.dev/promote-succeeded-reason"
)

type extendRunTimeoutTool struct{ maintainerToolBase }

type extendRunTimeoutInput struct {
	RunName    string `json:"run_name"`
	MaxRuntime string `json:"max_runtime"`
}

func (t *extendRunTimeoutTool) Name() string { return "extend_run_timeout" }
func (t *extendRunTimeoutTool) Description() string {
	return "Extend an authorized implementer or reviewer fleet run's maximum runtime. Runs without an explicit maximum use the platform default runtime."
}
func (t *extendRunTimeoutTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_name":{"type":"string"},"max_runtime":{"type":"string","description":"Go duration, for example 6h"}},"required":["run_name","max_runtime"]}`)
}
func (t *extendRunTimeoutTool) IsReadOnly() bool                      { return false }
func (t *extendRunTimeoutTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *extendRunTimeoutTool) NeedsApproval() bool                   { return false }
func (t *extendRunTimeoutTool) TimeoutSeconds() int                   { return 0 }

func (t *extendRunTimeoutTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in extendRunTimeoutInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	name := strings.TrimSpace(in.RunName)
	if name == "" || strings.TrimSpace(in.MaxRuntime) == "" {
		return Result{Content: "run_name and max_runtime are required", IsError: true}, nil
	}
	maxRuntime, err := time.ParseDuration(strings.TrimSpace(in.MaxRuntime))
	if err != nil {
		return Result{Content: fmt.Sprintf("max_runtime must be a Go duration: %v", err), IsError: true}, nil
	}
	if maxRuntime <= 0 || maxRuntime > maxMaintainerRunTimeout {
		return Result{Content: "max_runtime must be greater than zero and at most 72h", IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	run, err := t.fleetRun(ctx, name)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to verify fleet AgentRun: %v", err), IsError: true}, nil
	}
	if maintainerTerminal(run.Status.Phase) {
		return Result{Content: fmt.Sprintf("cannot extend terminal AgentRun in phase %s", run.Status.Phase), IsError: true}, nil
	}
	if run.Spec.Limits != nil && run.Spec.Limits.MaxRuntime.Duration > 0 && maxRuntime <= run.Spec.Limits.MaxRuntime.Duration {
		return Result{Content: "max_runtime must extend the AgentRun's existing maximum runtime", IsError: true}, nil
	}

	key := client.ObjectKey{Name: name, Namespace: t.currentRunNamespace}
	oldRuntime := "platform default"
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := t.k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		if !t.isFleetRun(fresh) {
			return fmt.Errorf("AgentRun %q is no longer a fleet run for the maintained repository", name)
		}
		if maintainerTerminal(fresh.Status.Phase) {
			return fmt.Errorf("cannot extend terminal AgentRun in phase %s", fresh.Status.Phase)
		}
		if fresh.Spec.Limits != nil && fresh.Spec.Limits.MaxRuntime.Duration > 0 {
			if maxRuntime <= fresh.Spec.Limits.MaxRuntime.Duration {
				return fmt.Errorf("max_runtime must extend the AgentRun's existing maximum runtime")
			}
			oldRuntime = fresh.Spec.Limits.MaxRuntime.Duration.String()
		} else {
			oldRuntime = "platform default"
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		if fresh.Spec.Limits == nil {
			fresh.Spec.Limits = &platformv1alpha1.AgentRunLimits{}
		}
		fresh.Spec.Limits.MaxRuntime = metav1.Duration{Duration: maxRuntime}
		return t.k8sClient.Patch(ctx, fresh, patch)
	}); err != nil {
		return Result{Content: fmt.Sprintf("failed to extend AgentRun timeout: %v", err), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("AgentRun %q timeout extended: %s → %s.", name, oldRuntime, maxRuntime)}, nil
}

type markRunSucceededTool struct{ maintainerToolBase }

type markRunSucceededInput struct {
	RunName string `json:"run_name"`
	Reason  string `json:"reason"`
}

func (t *markRunSucceededTool) Name() string { return "mark_run_succeeded" }
func (t *markRunSucceededTool) Description() string {
	return "Request that the controller transition a non-terminal implementer run to Succeeded after verifying its linked pull request merged. An agent calling finish may leave its run ready or idle for PR feedback; finish alone is not success. Include the merged PR URL and evidence in reason."
}
func (t *markRunSucceededTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_name":{"type":"string"},"reason":{"type":"string","minLength":10,"maxLength":1000}},"required":["run_name","reason"]}`)
}
func (t *markRunSucceededTool) IsReadOnly() bool                      { return false }
func (t *markRunSucceededTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *markRunSucceededTool) NeedsApproval() bool                   { return false }
func (t *markRunSucceededTool) TimeoutSeconds() int                   { return 0 }

func (t *markRunSucceededTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in markRunSucceededInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	name, reason := strings.TrimSpace(in.RunName), strings.TrimSpace(in.Reason)
	if name == "" || reason == "" {
		return Result{Content: "run_name and reason are required", IsError: true}, nil
	}
	if n := utf8.RuneCountInString(reason); n < 10 || n > 1000 {
		return Result{Content: "reason must be between 10 and 1000 characters", IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	run, err := t.fleetRun(ctx, name)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to verify fleet AgentRun: %v", err), IsError: true}, nil
	}
	if maintainerIsReviewer(run) {
		return Result{Content: "reviewer fleet runs cannot be marked succeeded by the maintainer", IsError: true}, nil
	}
	if maintainerTerminal(run.Status.Phase) {
		return Result{Content: fmt.Sprintf("cannot mark terminal AgentRun in phase %s succeeded", run.Status.Phase), IsError: true}, nil
	}

	key := client.ObjectKey{Name: name, Namespace: t.currentRunNamespace}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := t.k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		if !t.isFleetRun(fresh) {
			return fmt.Errorf("AgentRun %q is no longer a fleet run for the maintained repository", name)
		}
		if maintainerIsReviewer(fresh) {
			return fmt.Errorf("reviewer fleet runs cannot be marked succeeded by the maintainer")
		}
		if maintainerTerminal(fresh.Status.Phase) {
			return fmt.Errorf("cannot mark terminal AgentRun in phase %s succeeded", fresh.Status.Phase)
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[maintainerPromoteSucceededAnnotation] = time.Now().UTC().Format(time.RFC3339)
		fresh.Annotations[maintainerPromoteSucceededReason] = reason
		return t.k8sClient.Patch(ctx, fresh, patch)
	}); err != nil {
		return Result{Content: fmt.Sprintf("failed to mark AgentRun succeeded: %v", err), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("AgentRun %q was marked for success; the controller will transition it to Succeeded.", name)}, nil
}
