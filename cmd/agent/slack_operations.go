package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// slackRequesterAnnotation records the Slack user who asked for a run, so
// private notifications about it can be routed back to them.
const slackRequesterAnnotation = "triggers.gratefulagents.dev/slack-requester"

// errOperationalRunNotFound is returned for any run the actor may not operate:
// unknown, another agent's, another namespace's, or not Slack-triggered. One
// error for every case keeps the Home controls from revealing which runs exist.
var errOperationalRunNotFound = errors.New("that run is no longer available; refresh Home and try again")

// operationalRun loads a run the user may operate from Home: the actor must be
// the owner or a commander, and the run must belong to this agent.
func (o *slackOrchestrator) operationalRun(
	ctx context.Context,
	userID, name string,
) (*platformv1alpha1.AgentRun, error) {
	if !o.mayStop(userID) {
		return nil, errOperationalRunNotFound
	}
	run := &platformv1alpha1.AgentRun{}
	if err := o.crdClient.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: name}, run); err != nil {
		return nil, errOperationalRunNotFound
	}
	if run.Labels[slackAgentLabel] != o.agentName || !run.Spec.Trigger.MatchesKind(slackTriggerKind) {
		return nil, errOperationalRunNotFound
	}
	return run, nil
}

// operationalRuns lists this agent's Slack-triggered runs, active ones first
// and newest first within each group.
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

// configuredRepositories returns the primary and additional repositories a
// New Run may target, de-duplicated and without blanks, primary first.
func configuredRepositories(defaults triggersv1alpha1.AgentRunDefaults) []string {
	repos := make([]string, 0, 1+len(defaults.AdditionalRepos))
	seen := map[string]bool{}
	for _, repo := range append([]string{defaults.RepoURL}, defaults.AdditionalRepos...) {
		repo = strings.TrimSpace(repo)
		if repo != "" && !seen[repo] {
			repos = append(repos, repo)
			seen[repo] = true
		}
	}
	return repos
}

// homeRunFor projects a run and the PR monitors it owns onto the Home card
// view model. Stop applies only to a running turn; Resume to any run that has
// produced a conversation to continue (a pending or provisioning run has
// nothing to resume yet).
func homeRunFor(
	run *platformv1alpha1.AgentRun,
	monitors []triggersv1alpha1.PullRequestMonitor,
) internalslack.HomeRun {
	card := internalslack.HomeRun{
		Name:       run.Name,
		Phase:      string(run.Status.Phase),
		Repository: run.Spec.Repository.URL,
		BaseBranch: run.Spec.Repository.BaseBranch,
		StartedAt:  run.CreationTimestamp.Time,
	}
	if run.Status.StartedAt != nil && !run.Status.StartedAt.IsZero() {
		card.StartedAt = run.Status.StartedAt.Time
	}
	switch run.Status.Phase {
	case platformv1alpha1.AgentRunPhaseRunning:
		card.CanStop, card.CanResume = true, true
	case platformv1alpha1.AgentRunPhasePaused,
		platformv1alpha1.AgentRunPhaseQuestion,
		platformv1alpha1.AgentRunPhaseBlocked,
		platformv1alpha1.AgentRunPhaseSucceeded,
		platformv1alpha1.AgentRunPhaseFailed,
		platformv1alpha1.AgentRunPhaseCancelled:
		card.CanResume = true
	}
	for i := range monitors {
		m := &monitors[i]
		card.PullRequests = append(card.PullRequests, internalslack.HomePullRequest{
			URL:       m.Spec.URL,
			Title:     m.Status.Title,
			Lifecycle: string(m.Status.Lifecycle),
			Checks:    slackMonitorChecks(m),
		})
	}
	return card
}

// monitorsByRun groups PR monitors by the run that opened them.
func monitorsByRun(
	monitors []triggersv1alpha1.PullRequestMonitor,
) map[string][]triggersv1alpha1.PullRequestMonitor {
	byRun := map[string][]triggersv1alpha1.PullRequestMonitor{}
	for _, m := range monitors {
		byRun[m.Spec.ImplementerRef.Name] = append(byRun[m.Spec.ImplementerRef.Name], m)
	}
	return byRun
}

// slackRunSummary is the one-line mrkdwn description of a run used in DM
// confirmations and notifications: name, repository, and base branch.
func slackRunSummary(run *platformv1alpha1.AgentRun) string {
	base := strings.TrimSpace(run.Spec.Repository.BaseBranch)
	if base == "" {
		base = "repository default"
	}
	return fmt.Sprintf("`%s` · `%s` on `%s`",
		run.Name,
		internalslack.RepositoryLabel(run.Spec.Repository.URL),
		base,
	)
}

var slackBranchPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

// errInvalidBranch is the user-facing rejection for a base branch that is not
// a plausible git ref name.
var errInvalidBranch = errors.New("enter a valid branch name, e.g. main or release/v2")

// validateBranchName rejects anything that is not a plausible git ref name
// before it reaches a checkout. Whether the branch exists is settled by the
// checkout itself.
func validateBranchName(branch string) error {
	if len(branch) > 255 || !slackBranchPattern.MatchString(branch) || strings.Contains(branch, "..") ||
		strings.Contains(branch, "//") ||
		strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") {
		return errInvalidBranch
	}
	for part := range strings.SplitSeq(branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return errInvalidBranch
		}
	}
	return nil
}

// slackSelectedDefaults applies a New Run form's repository and branch choice
// to the live defaults: the chosen repository becomes primary (the others stay
// as additional repositories) and the branch becomes the base branch. The
// repository must still be configured; the branch must be a valid ref name.
func slackSelectedDefaults(
	defaults triggersv1alpha1.AgentRunDefaults,
	repo, branch string,
) (triggersv1alpha1.AgentRunDefaults, error) {
	if repo != "" {
		allowed := configuredRepositories(defaults)
		found := false
		for _, candidate := range allowed {
			if candidate == repo {
				found = true
			}
		}
		if !found {
			return defaults, errors.New("that repository is no longer configured; open New Run again")
		}
		defaults.AdditionalRepos = nil
		for _, candidate := range allowed {
			if candidate != repo {
				defaults.AdditionalRepos = append(defaults.AdditionalRepos, candidate)
			}
		}
		defaults.RepoURL = repo
	}
	if branch != "" {
		if err := validateBranchName(branch); err != nil {
			return defaults, err
		}
		defaults.BaseBranch = branch
	}
	return defaults, nil
}
