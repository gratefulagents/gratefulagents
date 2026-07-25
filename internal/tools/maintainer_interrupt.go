package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

type stopAgentRunTurnTool struct{ maintainerToolBase }

type stopAgentRunTurnInput struct {
	RunName string `json:"run_name"`
	Reason  string `json:"reason"`
}

func (t *stopAgentRunTurnTool) Name() string { return "stop_agent_run_turn" }
func (t *stopAgentRunTurnTool) Description() string {
	return "Explicitly stop the current turn of an authorized running implementer without cancelling the AgentRun. This is independent of steer/queue delivery; send a new steering or queued message separately when the run should continue."
}
func (t *stopAgentRunTurnTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_name":{"type":"string","minLength":1},"reason":{"type":"string","minLength":1,"maxLength":1000}},"required":["run_name","reason"]}`)
}
func (t *stopAgentRunTurnTool) IsReadOnly() bool                      { return false }
func (t *stopAgentRunTurnTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *stopAgentRunTurnTool) NeedsApproval() bool                   { return false }
func (t *stopAgentRunTurnTool) TimeoutSeconds() int                   { return 0 }

func (t *stopAgentRunTurnTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in stopAgentRunTurnInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	name, reason := strings.TrimSpace(in.RunName), strings.TrimSpace(in.Reason)
	if name == "" || reason == "" {
		return Result{Content: "run_name and reason are required", IsError: true}, nil
	}
	if len([]rune(reason)) > 1000 {
		return Result{Content: "reason must be at most 1000 characters", IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	run, err := t.fleetRun(ctx, name)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to verify fleet AgentRun: %v", err), IsError: true}, nil
	}
	if maintainerIsReviewer(run) {
		return Result{Content: "reviewer fleet runs cannot be stopped by the maintainer", IsError: true}, nil
	}
	repository, err := t.repository(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if run.Status.Phase != platformv1alpha1.AgentRunPhaseRunning {
		return Result{Content: fmt.Sprintf("AgentRun %q is not running; current phase is %s", name, run.Status.Phase), IsError: true}, nil
	}
	session, err := t.stateStore.GetSessionByRun(ctx, run.Name, run.Namespace)
	if err != nil || session == nil {
		return Result{Content: fmt.Sprintf("failed to resolve active fleet session: %v", err), IsError: true}, nil
	}
	// Close the authorization race immediately before persisting the interrupt.
	caller, err := t.currentRun(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	run, err = t.fleetRun(ctx, name)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to reverify fleet AgentRun: %v", err), IsError: true}, nil
	}
	repository, err = t.repository(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if !maintainerFleetRunOwnedByRepository(caller, repository) {
		return Result{Content: "calling maintainer is no longer owned by the maintained repository UID", IsError: true}, nil
	}
	if run.Status.Phase != platformv1alpha1.AgentRunPhaseRunning || maintainerIsReviewer(run) || !t.isFleetRunForRepository(ctx, run, repository) {
		return Result{Content: "target AgentRun is no longer an authorized running implementer", IsError: true}, nil
	}
	requestedBy := "maintainer:" + t.currentRunName
	if err := sessionclient.RequestInterrupt(ctx, t.stateStore, session.ID, requestedBy); err != nil {
		return Result{Content: fmt.Sprintf("failed to request turn stop: %v", err), IsError: true}, nil
	}
	detail, _ := json.Marshal(map[string]string{"reason": reason, "requested_by": requestedBy})
	_, _ = t.stateStore.WriteActivityEvent(ctx, session.ID, "interrupt_requested", "Maintainer requested that the current turn stop: "+reason, detail)
	return Result{Content: fmt.Sprintf("Stop requested for the current turn of AgentRun %q; the run remains resumable.", name)}, nil
}
