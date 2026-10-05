package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const slackRequesterAnnotation = "triggers.gratefulagents.dev/slack-requester"

func (o *slackOrchestrator) operationalRun(
	ctx context.Context,
	userID, name string,
) (*platformv1alpha1.AgentRun, error) {
	if !o.mayStop(userID) {
		return nil, fmt.Errorf("run not found")
	}
	run := &platformv1alpha1.AgentRun{}
	if err := o.crdClient.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: name}, run); err != nil {
		return nil, fmt.Errorf("run not found")
	}
	if run.Labels[slackAgentLabel] != o.agentName || !run.Spec.Trigger.MatchesKind(slackTriggerKind) {
		return nil, fmt.Errorf("run not found")
	}
	return run, nil
}

func (o *slackOrchestrator) operationalRuns(ctx context.Context) ([]platformv1alpha1.AgentRun, error) {
	list := &platformv1alpha1.AgentRunList{}
	if err := o.crdClient.List(
		ctx,
		list,
		client.InNamespace(o.namespace),
		client.MatchingLabels{slackAgentLabel: o.agentName},
	); err != nil {
		return nil, err
	}
	runs := make([]platformv1alpha1.AgentRun, 0, len(list.Items))
	for _, run := range list.Items {
		if run.Spec.Trigger.MatchesKind(slackTriggerKind) {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if isTerminalPhase(runs[i].Status.Phase) != isTerminalPhase(runs[j].Status.Phase) {
			return !isTerminalPhase(runs[i].Status.Phase)
		}
		return runs[i].CreationTimestamp.After(runs[j].CreationTimestamp.Time)
	})
	return runs, nil
}

func slackRunSummary(run *platformv1alpha1.AgentRun) string {
	phase := string(run.Status.Phase)
	if phase == "" {
		phase = "Pending"
	}
	return fmt.Sprintf(
		"%s — %s\nRepository: %s · Base: %s",
		run.Name,
		phase,
		run.Spec.Repository.URL,
		run.Spec.Repository.BaseBranch,
	)
}

var slackBranchPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

func slackSelectedDefaults(
	defaults triggersv1alpha1.AgentRunDefaults,
	repo, branch string,
) (triggersv1alpha1.AgentRunDefaults, error) {
	if repo != "" {
		allowed := append([]string{defaults.RepoURL}, defaults.AdditionalRepos...)
		found := false
		for _, candidate := range allowed {
			if candidate == repo {
				found = true
			}
		}
		if !found {
			return defaults, fmt.Errorf("repository is no longer configured; open New Run again")
		}
		defaults.AdditionalRepos = nil
		for _, candidate := range allowed {
			if candidate != "" && candidate != repo {
				defaults.AdditionalRepos = append(defaults.AdditionalRepos, candidate)
			}
		}
		defaults.RepoURL = repo
	}
	if branch != "" {
		if len(branch) > 255 || !slackBranchPattern.MatchString(branch) || strings.Contains(branch, "..") ||
			strings.Contains(branch, "//") ||
			strings.HasSuffix(branch, "/") ||
			strings.HasSuffix(branch, ".") {
			return defaults, fmt.Errorf("invalid branch name")
		}
		for part := range strings.SplitSeq(branch, "/") {
			if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
				return defaults, fmt.Errorf("invalid branch name")
			}
		}
		defaults.BaseBranch = branch
	}
	return defaults, nil
}
