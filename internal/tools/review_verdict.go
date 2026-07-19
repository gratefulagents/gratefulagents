package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RegisterReviewVerdictTool registers the submit_review_verdict tool, which
// records a structured review outcome on the reviewer's own AgentRun. The
// autonomous PR loop engine reads the verdict when the reviewer run completes
// and either approves the loop or wakes the implementer run to address the
// feedback.
func RegisterReviewVerdictTool(registry *Registry, k8sClient client.Client, taskName, namespace string) {
	if registry == nil || k8sClient == nil {
		return
	}
	registry.Register(&reviewVerdictTool{
		k8sClient: k8sClient,
		taskName:  taskName,
		namespace: namespace,
	})
}

type reviewVerdictTool struct {
	k8sClient client.Client
	taskName  string
	namespace string
}

type reviewVerdictInput struct {
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
}

func (t *reviewVerdictTool) Name() string { return "submit_review_verdict" }

func (t *reviewVerdictTool) Description() string {
	return "Record your final verdict after reviewing a pull request. Use ONLY when " +
		"your task is reviewing a PR (you are a reviewer run in the autonomous PR loop). " +
		"Call this exactly once, after you have posted your review feedback on GitHub " +
		"(submit_pull_request_review): verdict=approve when the PR is ready to ship " +
		"with no blocking findings, verdict=request_changes when the author must " +
		"address findings first. The platform routes request_changes verdicts back " +
		"to the implementer agent automatically."
}

func (t *reviewVerdictTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"verdict": {
				"type": "string",
				"enum": ["approve", "request_changes"],
				"description": "approve = no blocking findings, PR is ready; request_changes = the author must address your feedback"
			},
			"summary": {
				"type": "string",
				"description": "One-paragraph summary of the review outcome and the blocking findings, if any"
			}
		},
		"required": ["verdict", "summary"]
	}`)
}

func (t *reviewVerdictTool) IsReadOnly() bool                      { return false }
func (t *reviewVerdictTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *reviewVerdictTool) NeedsApproval() bool                   { return false }
func (t *reviewVerdictTool) TimeoutSeconds() int                   { return 0 }

func (t *reviewVerdictTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in reviewVerdictInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	verdict := strings.ToLower(strings.TrimSpace(in.Verdict))
	switch verdict {
	case platformv1alpha1.ReviewVerdictApprove, platformv1alpha1.ReviewVerdictRequestChanges:
	default:
		return Result{Content: `verdict must be "approve" or "request_changes"`, IsError: true}, nil
	}
	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		return Result{Content: "summary is required", IsError: true}, nil
	}
	if len(summary) > 4000 {
		summary = summary[:4000] + "… (truncated)"
	}

	key := types.NamespacedName{Name: t.taskName, Namespace: t.namespace}
	var run platformv1alpha1.AgentRun
	if err := t.k8sClient.Get(ctx, key, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return Result{Content: "AgentRun not found", IsError: true}, nil
		}
		return Result{Content: fmt.Sprintf("failed to get AgentRun: %v", err), IsError: true}, nil
	}

	patch := client.MergeFrom(run.DeepCopy())
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	run.Annotations[platformv1alpha1.ReviewVerdictAnnotation] = verdict
	run.Annotations[platformv1alpha1.ReviewSummaryAnnotation] = summary
	if err := t.k8sClient.Patch(ctx, &run, patch); err != nil {
		return Result{Content: fmt.Sprintf("failed to record verdict: %v", err), IsError: true}, nil
	}

	log.Printf("submit_review_verdict: recorded %q", verdict)
	return Result{Content: fmt.Sprintf("Review verdict recorded: %s. Finish the run when your review is complete.", verdict)}, nil
}
