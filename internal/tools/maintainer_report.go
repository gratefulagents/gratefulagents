package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type submitMaintainerReportTool struct{ maintainerToolBase }

type submitMaintainerReportInput struct {
	State     string `json:"state"`
	Summary   string `json:"summary"`
	Decisions string `json:"decisions,omitempty"`
}

type maintainerReport struct {
	Summary   string `json:"summary"`
	State     string `json:"state"`
	Decisions string `json:"decisions"`
	Time      string `json:"time"`
}

func (t *submitMaintainerReportTool) Name() string { return "submit_maintainer_report" }
func (t *submitMaintainerReportTool) Description() string {
	return "Record this maintainer episode's healthy, needs-attention, or blocked report on the standing maintainer AgentRun."
}
func (t *submitMaintainerReportTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"state":{"type":"string","enum":["healthy","needs_attention","blocked"]},"summary":{"type":"string","maxLength":2000},"decisions":{"type":"string","maxLength":4000}},"required":["state","summary"]}`)
}
func (t *submitMaintainerReportTool) IsReadOnly() bool                      { return false }
func (t *submitMaintainerReportTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *submitMaintainerReportTool) NeedsApproval() bool                   { return false }
func (t *submitMaintainerReportTool) TimeoutSeconds() int                   { return 0 }

func (t *submitMaintainerReportTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in submitMaintainerReportInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	state := strings.ToLower(strings.TrimSpace(in.State))
	switch state {
	case triggersv1alpha1.MaintainerReportStateHealthy, triggersv1alpha1.MaintainerReportStateAttention, triggersv1alpha1.MaintainerReportStateBlocked:
	default:
		return Result{Content: `state must be "healthy", "needs_attention", or "blocked"`, IsError: true}, nil
	}
	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		return Result{Content: "summary is required", IsError: true}, nil
	}
	if utf8.RuneCountInString(summary) > 2000 {
		return Result{Content: "summary must be at most 2000 characters", IsError: true}, nil
	}
	if utf8.RuneCountInString(in.Decisions) > 4000 {
		return Result{Content: "decisions must be at most 4000 characters", IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	reportJSON, err := json.Marshal(maintainerReport{Summary: summary, State: state, Decisions: in.Decisions, Time: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return Result{}, err
	}
	key := client.ObjectKey{Name: t.currentRunName, Namespace: t.currentRunNamespace}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &platformv1alpha1.AgentRun{}
		if err := t.k8sClient.Get(ctx, key, run); err != nil {
			return err
		}
		if run.Namespace != t.repositoryNamespace || run.Labels[orchestration.StandingRunRoleLabel] != orchestration.StandingRunRoleMaintainer || run.Labels[orchestration.SupervisedRunLabel] != t.repositoryName {
			return fmt.Errorf("current AgentRun is no longer authorized as a maintainer")
		}
		owned := false
		for _, owner := range run.OwnerReferences {
			if owner.Controller != nil && *owner.Controller && owner.Kind == "GitHubRepository" && owner.Name == t.repositoryName {
				owned = true
				break
			}
		}
		if !owned {
			return fmt.Errorf("current AgentRun is no longer owned by the maintained GitHubRepository")
		}
		patch := client.MergeFrom(run.DeepCopy())
		if run.Annotations == nil {
			run.Annotations = map[string]string{}
		}
		run.Annotations[triggersv1alpha1.MaintainerReportAnnotation] = string(reportJSON)
		return t.k8sClient.Patch(ctx, run, patch)
	}); err != nil {
		return Result{Content: fmt.Sprintf("failed to submit maintainer report: %v", err), IsError: true}, nil
	}
	if session, err := t.stateStore.GetSessionByRun(ctx, t.currentRunName, t.currentRunNamespace); err != nil {
		return Result{Content: fmt.Sprintf("Maintainer report recorded: %s (warning: failed to write report history: %v)", state, err)}, nil
	} else if session == nil {
		return Result{Content: fmt.Sprintf("Maintainer report recorded: %s (warning: failed to write report history: session not found)", state)}, nil
	} else if _, err := t.stateStore.WriteActivityEvent(ctx, session.ID, "maintainer_report", summary, reportJSON); err != nil {
		return Result{Content: fmt.Sprintf("Maintainer report recorded: %s (warning: failed to write report history: %v)", state, err)}, nil
	}
	return Result{Content: fmt.Sprintf("Maintainer report recorded: %s", state)}, nil
}
