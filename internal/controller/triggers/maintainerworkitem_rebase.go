package triggers

import (
	"context"
	"fmt"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// notifyOpenFleetPullRequestsAfterMerge tells every sibling work item's active
// implementer that the base branch moved, so it merges the base into its own
// branch instead of waiting for the maintainer LLM to notice a conflict.
// Delivery is idempotent per (merged head, open PR); failures are logged only
// because the merge itself is already durably recorded.
func (r *GitHubRepositoryReconciler) notifyOpenFleetPullRequestsAfterMerge(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, mergedItem *triggersv1alpha1.MaintainerWorkItem, mergedRepository string, mergedNumber int32, mergedHead, baseBranch string) {
	if r.StateStore == nil || repository == nil || mergedItem == nil {
		return
	}
	log := logf.FromContext(ctx).WithValues("repository", repository.Name, "mergedPullRequest", mergedNumber, "mergedHead", mergedHead)
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		baseBranch = strings.TrimSpace(repository.Spec.Defaults.BaseBranch)
	}
	if baseBranch == "" {
		baseBranch = "main"
	}
	items := &triggersv1alpha1.MaintainerWorkItemList{}
	if err := r.maintainerReader().List(ctx, items, client.InNamespace(repository.Namespace)); err != nil {
		log.Error(err, "failed to list sibling work items for post-merge rebase notification")
		return
	}
	for i := range items.Items {
		sibling := &items.Items[i]
		if sibling.Name == mergedItem.Name || sibling.Spec.RepositoryRef.Name != repository.Name || !sibling.DeletionTimestamp.IsZero() {
			continue
		}
		for _, run := range sibling.Status.AgentRuns {
			if run.Role != triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer || agentRunPhaseTerminal(run.Phase) {
				continue
			}
			for _, pull := range sibling.Status.PullRequests {
				if pull.Number == 0 || pull.State != triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen || pull.MergedAt != nil {
					continue
				}
				if !strings.EqualFold(pull.Repository, mergedRepository) {
					continue
				}
				deliveryID := fmt.Sprintf("maintainer-rebase-%s-%d", shortSHA(mergedHead), pull.Number)
				message := postMergeRebaseMessage(mergedNumber, mergedHead, baseBranch, pull.Number, pull.HeadSHA)
				if err := orchestration.WakeOrNudgeAgentRunOnce(ctx, r.Client, r.StateStore, sibling.Namespace, run.Name, message, deliveryID); err != nil {
					log.Error(err, "failed to deliver post-merge rebase notification", "workItem", sibling.Name, "agentRun", run.Name, "pullRequest", pull.Number)
				}
			}
		}
	}
}

func postMergeRebaseMessage(mergedNumber int32, mergedHead, baseBranch string, openNumber int32, openHead string) string {
	return fmt.Sprintf("[maintainer] %s moved — PR #%d merged at %s into %s. Your PR #%d (head %s) may now conflict with %s or be stale. Merge origin/%s into your branch now: run git_merge with branch %q, resolve any conflicts, re-run the project's verification, then git_push and finish. Do not open a new PR.",
		baseBranch, mergedNumber, shortSHA(mergedHead), baseBranch, openNumber, shortSHA(openHead), baseBranch, baseBranch, baseBranch)
}

func agentRunPhaseTerminal(phase string) bool {
	switch platformv1alpha1.AgentRunPhase(phase) {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled:
		return true
	default:
		return false
	}
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "unknown"
	}
	return sha
}
