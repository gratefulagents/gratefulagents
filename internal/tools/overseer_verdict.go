package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func RegisterOverseerVerdictTool(registry *Registry, k8sClient client.Client, taskName, namespace string) {
	if registry == nil || k8sClient == nil {
		return
	}
	registry.Register(&overseerVerdictTool{k8sClient: k8sClient, taskName: taskName, namespace: namespace})
}

type overseerVerdictTool struct {
	k8sClient client.Client
	taskName  string
	namespace string
}

type overseerVerdictInput struct {
	Verdict   string `json:"verdict"`
	Summary   string `json:"summary"`
	Guidance  string `json:"guidance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	ActionID  string `json:"action_id,omitempty"`
	Response  string `json:"response,omitempty"`
}

func (t *overseerVerdictTool) Name() string { return "submit_overseer_verdict" }

func (t *overseerVerdictTool) Description() string {
	return "Record the overseer's final assessment of the supervised run. Use all_clear when work may continue or complete unchanged, steer to provide corrective direction, reject_completion when the claimed completion is not acceptable, resolve_input to answer a pending input request, or escalate when human intervention is required."
}

func (t *overseerVerdictTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"verdict":{"type":"string","enum":["all_clear","steer","reject_completion","resolve_input","escalate"]},
			"summary":{"type":"string","description":"Concise assessment of the supervised run"},
			"guidance":{"type":"string","description":"Required corrective guidance for steer, reject_completion, and escalate"},
			"request_id":{"type":"string","description":"Pending request ID required for resolve_input"},
			"action_id":{"type":"string","description":"Selected action ID for resolve_input"},
			"response":{"type":"string","description":"Free-form response for resolve_input"}
		},
		"required":["verdict","summary"]
	}`)
}

func (t *overseerVerdictTool) IsReadOnly() bool                      { return false }
func (t *overseerVerdictTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *overseerVerdictTool) NeedsApproval() bool                   { return false }
func (t *overseerVerdictTool) TimeoutSeconds() int                   { return 0 }

func (t *overseerVerdictTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in overseerVerdictInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}

	verdict := strings.ToLower(strings.TrimSpace(in.Verdict))
	requiresGuidance := false
	switch verdict {
	case platformv1alpha1.OverseerVerdictAllClear, platformv1alpha1.OverseerVerdictResolveInput:
	case platformv1alpha1.OverseerVerdictSteer, platformv1alpha1.OverseerVerdictRejectCompletion, platformv1alpha1.OverseerVerdictEscalate:
		requiresGuidance = true
	default:
		return Result{Content: `verdict must be "all_clear", "steer", "reject_completion", "resolve_input", or "escalate"`, IsError: true}, nil
	}

	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		return Result{Content: "summary is required", IsError: true}, nil
	}
	guidance := strings.TrimSpace(in.Guidance)
	if requiresGuidance && guidance == "" {
		return Result{Content: fmt.Sprintf("guidance is required for verdict %q", verdict), IsError: true}, nil
	}
	requestID := strings.TrimSpace(in.RequestID)
	actionID := strings.TrimSpace(in.ActionID)
	response := strings.TrimSpace(in.Response)
	if verdict == platformv1alpha1.OverseerVerdictResolveInput {
		if requestID == "" {
			return Result{Content: "request_id is required for verdict \"resolve_input\"", IsError: true}, nil
		}
		if actionID == "" && response == "" {
			return Result{Content: "action_id or response is required for verdict \"resolve_input\"", IsError: true}, nil
		}
	}
	summary = truncateUTF8(summary, 4000)
	guidance = truncateUTF8(guidance, 4000)
	requestID = truncateUTF8(requestID, 512)
	actionID = truncateUTF8(actionID, 512)
	response = truncateUTF8(response, 4000)

	var inputResponseJSON []byte
	if verdict == platformv1alpha1.OverseerVerdictResolveInput {
		var err error
		inputResponseJSON, err = json.Marshal(platformv1alpha1.OverseerInputResponse{
			RequestID: requestID,
			ActionID:  actionID,
			Response:  response,
		})
		if err != nil {
			return Result{Content: fmt.Sprintf("failed to encode input response: %v", err), IsError: true}, nil
		}
	}

	key := types.NamespacedName{Name: t.taskName, Namespace: t.namespace}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var run platformv1alpha1.AgentRun
		if err := t.k8sClient.Get(ctx, key, &run); err != nil {
			return err
		}
		patch := client.MergeFrom(run.DeepCopy())
		if run.Annotations == nil {
			run.Annotations = map[string]string{}
		}
		run.Annotations[platformv1alpha1.OverseerVerdictAnnotation] = verdict
		run.Annotations[platformv1alpha1.OverseerSummaryAnnotation] = summary
		if guidance == "" {
			delete(run.Annotations, platformv1alpha1.OverseerGuidanceAnnotation)
		} else {
			run.Annotations[platformv1alpha1.OverseerGuidanceAnnotation] = guidance
		}
		if inputResponseJSON == nil {
			delete(run.Annotations, platformv1alpha1.OverseerInputResponseAnnotation)
		} else {
			run.Annotations[platformv1alpha1.OverseerInputResponseAnnotation] = string(inputResponseJSON)
		}
		return t.k8sClient.Patch(ctx, &run, patch)
	}); err != nil {
		if apierrors.IsNotFound(err) {
			return Result{Content: "AgentRun not found", IsError: true}, nil
		}
		return Result{Content: fmt.Sprintf("failed to record overseer verdict: %v", err), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("Overseer verdict recorded: %s", verdict)}, nil
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
