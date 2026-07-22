package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultMaintainerConcurrentDispatches int32 = 2
	defaultMaintainerDispatchesPerDay     int32 = 10
	maintainerPRLoopStateLabel                  = "triggers.gratefulagents.dev/pr-loop"
	maintainerPRLoopRoundAnnotation             = "triggers.gratefulagents.dev/review-round"
)

type maintainerToolBase struct {
	stateStore                          store.StateStore
	k8sClient                           client.Client
	currentRunName, currentRunNamespace string
	repositoryName, repositoryNamespace string
}

type maintainerDispatchLedger struct {
	Day     string                          `json:"day"`
	Count   int                             `json:"count"`
	Issues  []int                           `json:"issues"`
	Pending []maintainerDispatchReservation `json:"pending,omitempty"`
}

type maintainerDispatchReservation struct {
	Issue int    `json:"issue"`
	Mode  string `json:"mode"`
}

func (b maintainerToolBase) requireLegacyMutationAuthority(ctx context.Context) error {
	repository, err := b.repository(ctx)
	if err != nil {
		return fmt.Errorf("cannot prove legacy mutation authority: %w", err)
	}
	mode := triggersv1alpha1.MaintainerWorkItemCutoverController
	if repository.Spec.Maintainer != nil && repository.Spec.Maintainer.WorkItemCutover != "" {
		mode = repository.Spec.Maintainer.WorkItemCutover
	}
	if mode == triggersv1alpha1.MaintainerWorkItemCutoverController {
		return fmt.Errorf("generic maintainer mutation is denied in Controller cutover")
	}
	return nil
}

func (b maintainerToolBase) currentRun(ctx context.Context) (*platformv1alpha1.AgentRun, error) {
	current := &platformv1alpha1.AgentRun{}
	if err := b.k8sClient.Get(ctx, client.ObjectKey{Name: b.currentRunName, Namespace: b.currentRunNamespace}, current); err != nil {
		return nil, fmt.Errorf("failed to verify maintainer AgentRun: %w", err)
	}
	if current.Namespace != b.repositoryNamespace {
		return nil, fmt.Errorf("current AgentRun is not in the maintained repository namespace")
	}
	if current.Labels[orchestration.StandingRunRoleLabel] != orchestration.StandingRunRoleMaintainer {
		return nil, fmt.Errorf("current AgentRun is not authorized as a maintainer")
	}
	if current.Labels[orchestration.SupervisedRunLabel] != b.repositoryName {
		return nil, fmt.Errorf("current AgentRun is not assigned to the maintained repository")
	}
	for _, owner := range current.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.Kind == "GitHubRepository" && owner.Name == b.repositoryName {
			return current, nil
		}
	}
	return nil, fmt.Errorf("current AgentRun is not controller-owned by the maintained GitHubRepository")
}

func (b maintainerToolBase) repository(ctx context.Context) (*triggersv1alpha1.GitHubRepository, error) {
	repository := &triggersv1alpha1.GitHubRepository{}
	if err := b.k8sClient.Get(ctx, client.ObjectKey{Name: b.repositoryName, Namespace: b.repositoryNamespace}, repository); err != nil {
		return nil, fmt.Errorf("failed to get maintained GitHubRepository: %w", err)
	}
	return repository, nil
}

func (b maintainerToolBase) isFleetRun(run *platformv1alpha1.AgentRun) bool {
	if run == nil || run.Namespace != b.currentRunNamespace || run.Labels[orchestration.StandingRunRoleLabel] != "" || run.Spec.Trigger.Kind != "GitHubRepository" {
		return false
	}
	triggerName := strings.TrimSpace(run.Annotations["triggers.gratefulagents.dev/runtime-trigger-name"])
	if triggerName == "" {
		triggerName = strings.TrimSpace(run.Spec.Trigger.Name)
	}
	return triggerName == b.repositoryName
}

func (b maintainerToolBase) fleetRuns(ctx context.Context) ([]platformv1alpha1.AgentRun, error) {
	var runs platformv1alpha1.AgentRunList
	if err := b.k8sClient.List(ctx, &runs, client.InNamespace(b.currentRunNamespace)); err != nil {
		return nil, fmt.Errorf("failed to list fleet AgentRuns: %w", err)
	}
	fleet := make([]platformv1alpha1.AgentRun, 0, len(runs.Items))
	for i := range runs.Items {
		if b.isFleetRun(&runs.Items[i]) {
			fleet = append(fleet, runs.Items[i])
		}
	}
	return fleet, nil
}

func (b maintainerToolBase) fleetRun(ctx context.Context, name string) (*platformv1alpha1.AgentRun, error) {
	run := &platformv1alpha1.AgentRun{}
	if err := b.k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: b.currentRunNamespace}, run); err != nil {
		return nil, err
	}
	if !b.isFleetRun(run) {
		return nil, fmt.Errorf("AgentRun %q is not a fleet run for the maintained repository", name)
	}
	return run, nil
}

func maintainerIsReviewer(run *platformv1alpha1.AgentRun) bool {
	return run != nil && run.Labels[triggersv1alpha1.PRLoopRoleLabelKey] == triggersv1alpha1.PRLoopRoleReviewerValue
}

func maintainerTerminal(phase platformv1alpha1.AgentRunPhase) bool {
	switch phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled:
		return true
	default:
		return false
	}
}

func maintainerBlockedReason(run *platformv1alpha1.AgentRun) string {
	if run != nil && run.Status.Queue != nil {
		return run.Status.Queue.BlockedReason
	}
	return ""
}

func maintainerDispatchCaps(repository *triggersv1alpha1.GitHubRepository) (int32, int32) {
	concurrent, perDay := defaultMaintainerConcurrentDispatches, defaultMaintainerDispatchesPerDay
	if repository != nil && repository.Spec.Maintainer != nil {
		if repository.Spec.Maintainer.MaxConcurrentDispatches > 0 {
			concurrent = repository.Spec.Maintainer.MaxConcurrentDispatches
		}
		if repository.Spec.Maintainer.MaxDispatchesPerDay > 0 {
			perDay = repository.Spec.Maintainer.MaxDispatchesPerDay
		}
	}
	return concurrent, perDay
}

func parseMaintainerLedger(run *platformv1alpha1.AgentRun, now time.Time) maintainerDispatchLedger {
	day := now.UTC().Format("2006-01-02")
	ledger := maintainerDispatchLedger{Day: day, Issues: []int{}, Pending: []maintainerDispatchReservation{}}
	if run == nil || run.Annotations == nil {
		return ledger
	}
	if err := json.Unmarshal([]byte(run.Annotations[triggersv1alpha1.MaintainerDispatchLedgerAnnotation]), &ledger); err != nil || ledger.Day != day || ledger.Count < 0 {
		return maintainerDispatchLedger{Day: day, Issues: []int{}, Pending: []maintainerDispatchReservation{}}
	}
	if ledger.Issues == nil {
		ledger.Issues = []int{}
	}
	if ledger.Pending == nil {
		ledger.Pending = []maintainerDispatchReservation{}
	}
	return ledger
}

func maintainerLedgerHasIssue(ledger maintainerDispatchLedger, issueNumber int) bool {
	return slices.Contains(ledger.Issues, issueNumber)
}
