package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type wakeAgentRunTool struct{ maintainerToolBase }

type wakeAgentRunInput struct {
	RunName string `json:"run_name"`
	Message string `json:"message"`
}

func (t *wakeAgentRunTool) Name() string { return "wake_agent_run" }
func (t *wakeAgentRunTool) Description() string {
	return "Deliver maintainer context to an authorized implementer fleet run, revalidating the target immediately before delivery and again before requesting a wake."
}
func (t *wakeAgentRunTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_name":{"type":"string"},"message":{"type":"string","maxLength":4000}},"required":["run_name","message"]}`)
}
func (t *wakeAgentRunTool) IsReadOnly() bool                      { return false }
func (t *wakeAgentRunTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *wakeAgentRunTool) NeedsApproval() bool                   { return false }
func (t *wakeAgentRunTool) TimeoutSeconds() int                   { return 0 }

func (t *wakeAgentRunTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in wakeAgentRunInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	name, message := strings.TrimSpace(in.RunName), strings.TrimSpace(in.Message)
	if name == "" || message == "" {
		return Result{Content: "run_name and message are required", IsError: true}, nil
	}
	if utf8.RuneCountInString(message) > 4000 {
		return Result{Content: "message must be at most 4000 characters", IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	run, err := t.fleetRun(ctx, name)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to verify fleet AgentRun: %v", err), IsError: true}, nil
	}
	if maintainerIsReviewer(run) {
		return Result{Content: "reviewer fleet runs cannot be woken by the maintainer", IsError: true}, nil
	}
	if run.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		return Result{Content: "cancelled AgentRuns cannot be woken", IsError: true}, nil
	}
	session, err := t.stateStore.GetSessionByRun(ctx, run.Name, run.Namespace)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to resolve fleet session: %v", err), IsError: true}, nil
	}
	if session == nil {
		return Result{Content: "fleet session not found", IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	run, err = t.fleetRun(ctx, name)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to reverify fleet AgentRun: %v", err), IsError: true}, nil
	}
	if maintainerIsReviewer(run) {
		return Result{Content: "reviewer fleet runs cannot be woken by the maintainer", IsError: true}, nil
	}
	if run.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		return Result{Content: "cancelled AgentRuns cannot be woken", IsError: true}, nil
	}
	metadata, err := json.Marshal(map[string]string{"source": "maintainer", "maintainer_run": t.currentRunName})
	if err != nil {
		return Result{}, err
	}
	if _, err := t.stateStore.AppendMessage(ctx, session.ID, "user", "[maintainer] "+message, metadata); err != nil {
		return Result{Content: fmt.Sprintf("failed to deliver maintainer message: %v", err), IsError: true}, nil
	}
	if run.Status.Phase == platformv1alpha1.AgentRunPhaseRunning {
		return Result{Content: "Maintainer message delivered to running AgentRun; no wake request was needed."}, nil
	}
	key := client.ObjectKey{Name: run.Name, Namespace: run.Namespace}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := t.k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		if !t.isFleetRun(fresh) || maintainerIsReviewer(fresh) {
			return fmt.Errorf("AgentRun %q is no longer an authorized implementer fleet run", name)
		}
		if fresh.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
			return fmt.Errorf("cancelled AgentRuns cannot be woken")
		}
		if fresh.Status.Phase == platformv1alpha1.AgentRunPhaseRunning {
			return nil
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		fresh.Spec.WakeRequests++
		return t.k8sClient.Patch(ctx, fresh, patch)
	}); err != nil {
		return Result{Content: fmt.Sprintf("maintainer message delivered but wake request failed: %v", err), IsError: true}, nil
	}
	return Result{Content: "Maintainer message delivered and wake requested."}, nil
}
