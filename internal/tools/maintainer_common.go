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
	"k8s.io/apimachinery/pkg/types"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultMaintainerConcurrentDispatches int32 = 2
	defaultMaintainerDispatchesPerDay     int32 = 10
	defaultMaintainerDispatchMode               = "autopilot"
	maintainerPRLoopStateLabel                  = "triggers.gratefulagents.dev/pr-loop"
	maintainerPRLoopRoundAnnotation             = "triggers.gratefulagents.dev/review-round"
)

type maintainerToolBase struct {
	stateStore                          store.StateStore
	k8sClient                           client.Client
	currentRunName, currentRunNamespace string
	currentRunUID                       types.UID
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
	switch mode {
	case triggersv1alpha1.MaintainerWorkItemCutoverLegacy, triggersv1alpha1.MaintainerWorkItemCutoverDualRead:
		return nil
	case triggersv1alpha1.MaintainerWorkItemCutoverController:
		return fmt.Errorf("generic maintainer mutation is denied in Controller cutover")
	default:
		return fmt.Errorf("generic maintainer mutation is denied for unknown cutover mode %q", mode)
	}
}

func (b maintainerToolBase) currentRun(ctx context.Context) (*platformv1alpha1.AgentRun, error) {
	repository, err := b.repository(ctx)
	if err != nil {
		return nil, err
	}
	return b.currentRunForRepository(ctx, repository)
}

func (b maintainerToolBase) currentRunForRepository(ctx context.Context, repository *triggersv1alpha1.GitHubRepository) (*platformv1alpha1.AgentRun, error) {
	current := &platformv1alpha1.AgentRun{}
	if err := b.k8sClient.Get(ctx, client.ObjectKey{Name: b.currentRunName, Namespace: b.currentRunNamespace}, current); err != nil {
		return nil, fmt.Errorf("failed to verify maintainer AgentRun: %w", err)
	}
	if b.currentRunUID == "" || current.UID != b.currentRunUID {
		return nil, fmt.Errorf("current AgentRun UID does not match the tool session")
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
	if maintainerFleetRunOwnedByRepository(current, repository) {
		return current, nil
	}
	return nil, fmt.Errorf("current AgentRun is not controller-owned by the maintained GitHubRepository UID")
}

func (b maintainerToolBase) repository(ctx context.Context) (*triggersv1alpha1.GitHubRepository, error) {
	repository := &triggersv1alpha1.GitHubRepository{}
	if err := b.k8sClient.Get(ctx, client.ObjectKey{Name: b.repositoryName, Namespace: b.repositoryNamespace}, repository); err != nil {
		return nil, fmt.Errorf("failed to get maintained GitHubRepository: %w", err)
	}
	if repository.UID == "" || !repository.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("maintained GitHubRepository has no stable live UID")
	}
	return repository, nil
}

func maintainerDispatchMode(repository *triggersv1alpha1.GitHubRepository) (string, error) {
	if repository == nil || repository.Spec.Maintainer == nil || repository.Spec.Maintainer.DispatchModeRef == "" {
		return defaultMaintainerDispatchMode, nil
	}
	raw := repository.Spec.Maintainer.DispatchModeRef
	mode := strings.TrimSpace(raw)
	if mode == "" || mode != raw {
		return "", fmt.Errorf("invalid configured dispatch ModeTemplate name %q", raw)
	}
	if problems := utilvalidation.IsDNS1123Subdomain(mode); len(problems) > 0 {
		return "", fmt.Errorf("invalid configured dispatch ModeTemplate name %q: %s", raw, strings.Join(problems, "; "))
	}
	return mode, nil
}

func (b maintainerToolBase) isFleetRunCandidate(run *platformv1alpha1.AgentRun) bool {
	if run == nil || run.Namespace != b.currentRunNamespace || run.Labels[orchestration.StandingRunRoleLabel] != "" || run.Spec.Trigger.Kind != "GitHubRepository" {
		return false
	}
	triggerName := strings.TrimSpace(run.Annotations["triggers.gratefulagents.dev/runtime-trigger-name"])
	if triggerName == "" {
		triggerName = strings.TrimSpace(run.Spec.Trigger.Name)
	}
	return triggerName == b.repositoryName
}

func (b maintainerToolBase) isFleetRunForRepository(ctx context.Context, run *platformv1alpha1.AgentRun, repository *triggersv1alpha1.GitHubRepository) bool {
	if !b.isFleetRunCandidate(run) {
		return false
	}
	if maintainerFleetRunOwnedByRepository(run, repository) {
		return true
	}
	return b.maintainerWorkItemBindsRun(ctx, run, repository)
}

func (b maintainerToolBase) isFleetRunForCurrentRepository(ctx context.Context, run *platformv1alpha1.AgentRun) bool {
	repository, err := b.repository(ctx)
	return err == nil && b.isFleetRunForRepository(ctx, run, repository)
}

func (b maintainerToolBase) maintainerWorkItemBindsRun(ctx context.Context, run *platformv1alpha1.AgentRun, repository *triggersv1alpha1.GitHubRepository) bool {
	if run == nil || repository == nil || run.Spec.Context == nil || run.Spec.Context.ProjectRef == nil || run.Spec.Context.ProjectRef.Kind != "Project" {
		return false
	}
	projectName := strings.TrimSpace(repository.Annotations["triggers.gratefulagents.dev/project-name"])
	if projectName == "" {
		projectName = strings.TrimSpace(repository.Labels["triggers.gratefulagents.dev/project-name"])
	}
	projectUID := strings.TrimSpace(repository.Annotations["triggers.gratefulagents.dev/project-uid"])
	if projectUID == "" {
		projectUID = strings.TrimSpace(repository.Labels["triggers.gratefulagents.dev/project-uid"])
	}
	generated := strings.TrimSpace(repository.Annotations["triggers.gratefulagents.dev/generated-runtime"])
	if generated == "" {
		generated = strings.TrimSpace(repository.Labels["triggers.gratefulagents.dev/generated-runtime"])
	}
	triggerName := strings.TrimSpace(repository.Annotations["triggers.gratefulagents.dev/project-trigger-name"])
	if triggerName == "" {
		triggerName = strings.TrimSpace(repository.Labels["triggers.gratefulagents.dev/project-trigger-name"])
	}
	triggerType := strings.TrimSpace(repository.Annotations["triggers.gratefulagents.dev/project-trigger-type"])
	if triggerType == "" {
		triggerType = strings.TrimSpace(repository.Labels["triggers.gratefulagents.dev/project-trigger-type"])
	}
	runTriggerType := strings.TrimSpace(run.Spec.Trigger.Type)
	// Trigger.Type was added after the original AgentRun CRD. During a rolling
	// CRD upgrade an older schema can prune only this redundant discriminator.
	// Permit that empty value because the checks below still require the live
	// Project/repository/work-item ownership chain and an exact bound run UID;
	// never permit a non-empty conflicting type.
	triggerTypeMatches := runTriggerType == "" || strings.EqualFold(runTriggerType, triggerType)
	if generated != "true" || projectUID == "" || projectName == "" || triggerName == "" || triggerType == "" || run.Spec.Context.ProjectRef.Name != projectName || run.Spec.Trigger.Name != triggerName || !triggerTypeMatches || strings.TrimSpace(run.Annotations["triggers.gratefulagents.dev/runtime-trigger-name"]) != repository.Name || len(run.OwnerReferences) != 0 {
		return false
	}
	project := &triggersv1alpha1.Project{}
	if err := b.k8sClient.Get(ctx, client.ObjectKey{Name: projectName, Namespace: repository.Namespace}, project); err != nil || string(project.UID) != projectUID || !project.DeletionTimestamp.IsZero() {
		return false
	}
	ownedByProject := false
	for _, owner := range repository.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == "Project" && owner.Name == project.Name && owner.UID == project.UID {
			ownedByProject = true
			break
		}
	}
	if !ownedByProject {
		return false
	}
	workItemName := strings.TrimSpace(run.Labels[triggersv1alpha1.MaintainerWorkItemNameLabelKey])
	workItemUID := types.UID(strings.TrimSpace(run.Labels[triggersv1alpha1.MaintainerWorkItemUIDLabelKey]))
	if workItemName == "" || workItemUID == "" || run.UID == "" {
		return false
	}
	workItem := &triggersv1alpha1.MaintainerWorkItem{}
	if err := b.k8sClient.Get(ctx, client.ObjectKey{Name: workItemName, Namespace: run.Namespace}, workItem); err != nil || workItem.UID != workItemUID || !workItem.DeletionTimestamp.IsZero() || workItem.Spec.RepositoryRef.Name != repository.Name {
		return false
	}
	ownedByRepository := false
	for _, owner := range workItem.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == "GitHubRepository" && owner.Name == repository.Name && owner.UID == repository.UID {
			ownedByRepository = true
			break
		}
	}
	if !ownedByRepository {
		return false
	}
	for _, binding := range workItem.Status.AuthorizedAgentRuns {
		if binding.Name == run.Name && binding.UID == run.UID {
			return true
		}
	}
	return false
}

func maintainerFleetRunOwnedByRepository(run *platformv1alpha1.AgentRun, repository *triggersv1alpha1.GitHubRepository) bool {
	if run == nil || repository == nil || run.Namespace != repository.Namespace {
		return false
	}
	for _, owner := range run.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == "GitHubRepository" && owner.Name == repository.Name && owner.UID == repository.UID {
			return true
		}
	}
	return false
}

func (b maintainerToolBase) fleetRuns(ctx context.Context) ([]platformv1alpha1.AgentRun, error) {
	repository, err := b.repository(ctx)
	if err != nil {
		return nil, err
	}
	var runs platformv1alpha1.AgentRunList
	if err := b.k8sClient.List(ctx, &runs, client.InNamespace(b.currentRunNamespace)); err != nil {
		return nil, fmt.Errorf("failed to list fleet AgentRuns: %w", err)
	}
	fleet := make([]platformv1alpha1.AgentRun, 0, len(runs.Items))
	for i := range runs.Items {
		if b.isFleetRunForRepository(ctx, &runs.Items[i], repository) {
			fleet = append(fleet, runs.Items[i])
		}
	}
	return fleet, nil
}

func (b maintainerToolBase) fleetRun(ctx context.Context, name string) (*platformv1alpha1.AgentRun, error) {
	repository, err := b.repository(ctx)
	if err != nil {
		return nil, err
	}
	run := &platformv1alpha1.AgentRun{}
	if err := b.k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: b.currentRunNamespace}, run); err != nil {
		return nil, err
	}
	if !b.isFleetRunForRepository(ctx, run, repository) {
		return nil, fmt.Errorf("AgentRun %q is not an authorized fleet run for the maintained repository UID", name)
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
