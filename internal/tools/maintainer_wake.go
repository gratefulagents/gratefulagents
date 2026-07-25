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
	RunName      string `json:"run_name"`
	Message      string `json:"message"`
	DeliveryMode string `json:"delivery_mode,omitempty"`
}

type wakeAgentRunOutput struct {
	MessageID     int64  `json:"message_id"`
	DeliveryMode  string `json:"delivery_mode"`
	WakeRequested bool   `json:"wake_requested"`
}

func (t *wakeAgentRunTool) Name() string { return "wake_agent_run" }
func (t *wakeAgentRunTool) Description() string {
	return "Send maintainer context to an authorized implementer fleet run. delivery_mode=steer (default) injects it at the earliest safe in-flight boundary without stopping the turn; delivery_mode=queue waits for the next turn boundary. Use stop_agent_run_turn separately only when the turn itself must end. Returns the durable message id for edit/cancel."
}
func (t *wakeAgentRunTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_name":{"type":"string"},"message":{"type":"string","maxLength":4000},"delivery_mode":{"type":"string","enum":["steer","queue"],"default":"steer"}},"required":["run_name","message"]}`)
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
	deliveryMode := strings.ToLower(strings.TrimSpace(in.DeliveryMode))
	if deliveryMode == "" {
		deliveryMode = "steer"
	}
	if deliveryMode != "steer" && deliveryMode != "queue" {
		return Result{Content: "delivery_mode must be steer or queue", IsError: true}, nil
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
	storedMode := "immediate"
	if deliveryMode == "queue" {
		storedMode = "enqueue"
	}
	metadata, err := json.Marshal(map[string]string{"mode": storedMode, "source": "maintainer", "maintainer_run": t.currentRunName})
	if err != nil {
		return Result{}, err
	}
	delivered, err := t.stateStore.AppendMessage(ctx, session.ID, "user", "[maintainer] "+message, metadata)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to deliver maintainer message: %v", err), IsError: true}, nil
	}
	if run.Status.Phase == platformv1alpha1.AgentRunPhaseRunning {
		return wakeAgentRunResult(delivered.ID, deliveryMode, false)
	}
	key := client.ObjectKey{Name: run.Name, Namespace: run.Namespace}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := t.k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		if !t.isFleetRunForCurrentRepository(ctx, fresh) || maintainerIsReviewer(fresh) {
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
	return wakeAgentRunResult(delivered.ID, deliveryMode, true)
}

func wakeAgentRunResult(messageID int64, deliveryMode string, wakeRequested bool) (Result, error) {
	encoded, err := json.Marshal(wakeAgentRunOutput{MessageID: messageID, DeliveryMode: deliveryMode, WakeRequested: wakeRequested})
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}
