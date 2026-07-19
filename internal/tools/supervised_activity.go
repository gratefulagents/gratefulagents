package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const supervisedActivityStreamBudget = 24 * 1024

func RegisterSupervisedActivityTool(registry *Registry, stateStore store.StateStore, k8sClient client.Client, currentRunName, currentRunNamespace, supervisedRunName, supervisedRunNamespace string) {
	if registry == nil || stateStore == nil || k8sClient == nil || strings.TrimSpace(supervisedRunName) == "" || strings.TrimSpace(supervisedRunNamespace) == "" {
		return
	}
	registry.Register(&supervisedActivityTool{
		stateStore: stateStore, k8sClient: k8sClient,
		currentRunName: currentRunName, currentRunNamespace: currentRunNamespace,
		supervisedRunName: supervisedRunName, supervisedRunNamespace: supervisedRunNamespace,
	})
}

type supervisedActivityTool struct {
	stateStore                                store.StateStore
	k8sClient                                 client.Client
	currentRunName, currentRunNamespace       string
	supervisedRunName, supervisedRunNamespace string
}

type supervisedActivityInput struct {
	MessageCursor  int64 `json:"message_cursor,omitempty"`
	ActivityCursor int64 `json:"activity_cursor,omitempty"`
	Limit          int   `json:"limit,omitempty"`
}

type supervisedMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type supervisedEvent struct {
	ID        int64           `json:"id"`
	EventType string          `json:"event_type"`
	Summary   string          `json:"summary"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type supervisedRunState struct {
	Phase            platformv1alpha1.AgentRunPhase  `json:"phase"`
	Mode             string                          `json:"mode"`
	ModeRevision     int64                           `json:"mode_revision"`
	UserInputRequest *orchestration.PendingUserInput `json:"user_input_request,omitempty"`
}

type supervisedActivityOutput struct {
	State              supervisedRunState  `json:"state"`
	Messages           []supervisedMessage `json:"messages"`
	Activity           []supervisedEvent   `json:"activity"`
	NextMessageCursor  int64               `json:"next_message_cursor"`
	NextActivityCursor int64               `json:"next_activity_cursor"`
	HasMore            bool                `json:"has_more"`
}

func (t *supervisedActivityTool) Name() string { return "get_supervised_activity" }
func (t *supervisedActivityTool) Description() string {
	return "Read the current run state, pending user-input request, and cursor-paged conversation messages and activity events from the AgentRun assigned to this overseer. Run state and pending input are refreshed on every call; use the returned cursors to request subsequent stream pages."
}
func (t *supervisedActivityTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"message_cursor":{"type":"integer","minimum":0},"activity_cursor":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":200}}}`)
}
func (t *supervisedActivityTool) IsReadOnly() bool                      { return true }
func (t *supervisedActivityTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *supervisedActivityTool) NeedsApproval() bool                   { return false }
func (t *supervisedActivityTool) TimeoutSeconds() int                   { return 0 }

func (t *supervisedActivityTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in supervisedActivityInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if in.MessageCursor < 0 || in.ActivityCursor < 0 || in.Limit < 0 {
		return Result{Content: "cursors must be non-negative and limit must be positive", IsError: true}, nil
	}
	limit := in.Limit
	if limit == 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	var current platformv1alpha1.AgentRun
	if err := t.k8sClient.Get(ctx, client.ObjectKey{Name: t.currentRunName, Namespace: t.currentRunNamespace}, &current); err != nil {
		return Result{Content: fmt.Sprintf("failed to verify overseer AgentRun: %v", err), IsError: true}, nil
	}
	var supervised platformv1alpha1.AgentRun
	if err := t.k8sClient.Get(ctx, client.ObjectKey{Name: t.supervisedRunName, Namespace: t.supervisedRunNamespace}, &supervised); err != nil {
		return Result{Content: fmt.Sprintf("failed to verify supervised AgentRun: %v", err), IsError: true}, nil
	}
	if err := t.authorize(&current, &supervised); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}

	session, err := t.stateStore.GetSessionByRun(ctx, t.supervisedRunName, t.supervisedRunNamespace)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to resolve supervised session: %v", err), IsError: true}, nil
	}
	if session == nil {
		return Result{Content: "supervised session not found", IsError: true}, nil
	}
	inputRequest := orchestration.PendingUserInputForSession(session)
	pendingMCP, err := mcppolicy.PendingRequest(&supervised)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to decode supervised MCP request: %v", err), IsError: true}, nil
	}
	if pendingMCP != nil {
		inputRequest = orchestration.BindPendingUserInputContext(inputRequest, pendingMCP.ID)
	}
	messages, err := t.stateStore.GetMessagesSince(ctx, session.ID, in.MessageCursor)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to read supervised messages: %v", err), IsError: true}, nil
	}
	events, err := t.stateStore.GetActivityEventsSince(ctx, session.ID, in.ActivityCursor)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to read supervised activity: %v", err), IsError: true}, nil
	}

	output := supervisedActivityOutput{
		State: supervisedRunState{
			Phase:            supervised.Status.Phase,
			Mode:             supervised.Status.ModeName,
			ModeRevision:     supervised.Status.ModeRevision,
			UserInputRequest: inputRequest,
		},
		Messages:          make([]supervisedMessage, 0, min(limit, len(messages))),
		Activity:          make([]supervisedEvent, 0, min(limit, len(events))),
		NextMessageCursor: in.MessageCursor, NextActivityCursor: in.ActivityCursor,
	}
	messageBudget := supervisedActivityStreamBudget
	for _, message := range messages[:min(limit, len(messages))] {
		content := truncateUTF8(message.Content, 4000)
		cost := len(content) + len(message.Role) + 96
		if len(output.Messages) > 0 && cost > messageBudget {
			break
		}
		if cost > messageBudget {
			content = truncateUTF8(content, max(0, messageBudget-96))
			cost = len(content) + len(message.Role) + 96
		}
		output.Messages = append(output.Messages, supervisedMessage{ID: message.ID, Role: message.Role, Content: content, CreatedAt: message.CreatedAt})
		output.NextMessageCursor = message.ID
		messageBudget -= min(cost, messageBudget)
	}
	activityBudget := supervisedActivityStreamBudget
	for _, event := range events[:min(limit, len(events))] {
		summary := truncateUTF8(event.Summary, 2000)
		detail := event.Detail
		if len(detail) > 4000 {
			detail = json.RawMessage(`{"truncated":true}`)
		}
		cost := len(summary) + len(detail) + len(event.EventType) + 112
		if len(output.Activity) > 0 && cost > activityBudget {
			break
		}
		if cost > activityBudget {
			summary = truncateUTF8(summary, max(0, activityBudget-len(detail)-112))
			cost = len(summary) + len(detail) + len(event.EventType) + 112
		}
		output.Activity = append(output.Activity, supervisedEvent{ID: event.ID, EventType: event.EventType, Summary: summary, Detail: detail, CreatedAt: event.CreatedAt})
		output.NextActivityCursor = event.ID
		activityBudget -= min(cost, activityBudget)
	}
	output.HasMore = len(output.Messages) < len(messages) || len(output.Activity) < len(events)
	encoded, err := json.Marshal(output)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

func (t *supervisedActivityTool) authorize(run, supervised *platformv1alpha1.AgentRun) error {
	if run == nil || supervised == nil || run.Namespace != t.supervisedRunNamespace || supervised.Namespace != t.supervisedRunNamespace || supervised.Name != t.supervisedRunName {
		return fmt.Errorf("current AgentRun is not in the supervised run namespace")
	}
	if run.Labels[orchestration.StandingRunRoleLabel] != orchestration.StandingRunRoleOverseer {
		return fmt.Errorf("current AgentRun is not authorized as an overseer")
	}
	if run.Labels[orchestration.SupervisedRunLabel] != t.supervisedRunName {
		return fmt.Errorf("current AgentRun is not assigned to the requested supervised run")
	}
	if !metav1.IsControlledBy(run, supervised) {
		return fmt.Errorf("current AgentRun is not owned by the requested supervised run")
	}
	return nil
}
