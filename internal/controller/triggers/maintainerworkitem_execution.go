package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *GitHubRepositoryReconciler) processMaintainerExecutionCommand(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, githubClient GitHubTriageClient, pending bool) error {
	if pending {
		if command.Spec.Type == triggersv1alpha1.MaintainerWorkItemCommandTypeBreakdownIssue {
			if err := r.acquireMaintainerCommandLock(ctx, repository, command.Name); err != nil {
				return err
			}
			defer func() { _ = r.releaseMaintainerCommandLock(context.Background(), repository, command.Name) }()
		}
		if err := r.applyMaintainerExecutionIntent(ctx, repository, command, item); err != nil {
			var rejected maintainerCommandRejectedError
			if asMaintainerCommandRejected(err, &rejected) {
				return r.rejectMaintainerWorkItemCommand(ctx, repository, command, rejected.message)
			}
			return err
		}
		if err := r.setMaintainerWorkItemCommandAccepted(ctx, command, item); err != nil {
			return err
		}
	}
	if command.Spec.Type != triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem {
		return r.completeMaintainerWorkItemCommand(ctx, command, item, "work-item command applied", "", observedIssueState(item))
	}
	if githubClient == nil {
		return fmt.Errorf("GitHub client unavailable after dispatch capacity reservation")
	}
	issue, _, err := githubClient.GetIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber))
	if err != nil {
		return fmt.Errorf("getting issue after dispatch reservation: %w", err)
	}
	if !strings.EqualFold(issue.GetState(), "open") {
		return fmt.Errorf("issue #%d is no longer open after dispatch reservation", item.Spec.IssueNumber)
	}
	if _, _, err := githubClient.AddLabelsToIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber), []string{command.Spec.Dispatch.Mode}); err != nil {
		return fmt.Errorf("applying trigger label after dispatch reservation: %w", err)
	}
	return r.completeMaintainerWorkItemCommand(ctx, command, item, "dispatch capacity reserved and trigger label applied", "", observedIssueState(item))
}

//nolint:gocyclo // Typed command variants intentionally share one authorization and idempotency entrypoint.
func (r *GitHubRepositoryReconciler) applyMaintainerExecutionIntent(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem) error {
	switch command.Spec.Type {
	case triggersv1alpha1.MaintainerWorkItemCommandTypeBreakdownIssue:
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			fresh := &triggersv1alpha1.MaintainerWorkItem{}
			if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(item), fresh); err != nil {
				return err
			}
			if equality.Semantic.DeepEqual(fresh.Spec.Children, command.Spec.Breakdown.Children) && equality.Semantic.DeepEqual(fresh.Spec.Dependencies, command.Spec.Breakdown.Dependencies) {
				return nil
			}
			if fresh.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence || fresh.ResourceVersion != command.Spec.Preconditions.ResourceVersion {
				return rejectMaintainerCommand(currentProjectionMessage(fresh))
			}
			if err := r.validateBreakdown(ctx, repository, item.Name, command.Spec.Breakdown.Children, command.Spec.Breakdown.Dependencies); err != nil {
				return err
			}
			fresh.Spec.Children = append([]triggersv1alpha1.MaintainerWorkItemReference(nil), command.Spec.Breakdown.Children...)
			fresh.Spec.Dependencies = append([]triggersv1alpha1.MaintainerWorkItemReference(nil), command.Spec.Breakdown.Dependencies...)
			return r.Update(ctx, fresh)
		})
	case triggersv1alpha1.MaintainerWorkItemCommandTypeRequestDecision:
		if item.Status.PendingDecision != nil && (item.Status.PendingDecision.RequestedByCommand == nil || item.Status.PendingDecision.RequestedByCommand.Name != command.Name) {
			return rejectMaintainerCommand("work item already has a pending decision")
		}
		return r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
			if fresh.Status.PendingDecision != nil {
				if fresh.Status.PendingDecision.RequestedByCommand != nil && fresh.Status.PendingDecision.RequestedByCommand.Name == command.Name {
					return false, nil
				}
				return false, rejectMaintainerCommand("work item already has a pending decision")
			}
			if fresh.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence || fresh.ResourceVersion != command.Spec.Preconditions.ResourceVersion {
				return false, rejectMaintainerCommand(currentProjectionMessage(fresh))
			}
			now := metav1.Now()
			fresh.Status.PendingDecision = &triggersv1alpha1.MaintainerPendingDecision{ID: command.Spec.RequestDecision.DecisionID, Question: command.Spec.RequestDecision.Question, Options: append([]string(nil), command.Spec.RequestDecision.Options...), RequestedAt: now, RequestedByCommand: &corev1.LocalObjectReference{Name: command.Name}}
			fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseAwaitingDecision
			fresh.Status.ProjectionSequence++
			return true, nil
		})
	case triggersv1alpha1.MaintainerWorkItemCommandTypeResolveDecision:
		if item.Status.ResolvedDecision != nil && item.Status.ResolvedDecision.ResolvedByCommand.Name == command.Name {
			return nil
		}
		if item.Status.PendingDecision == nil || item.Status.PendingDecision.ID != command.Spec.ResolveDecision.DecisionID {
			return rejectMaintainerCommand("resolveDecision does not match pendingDecision")
		}
		return r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
			if fresh.Status.ResolvedDecision != nil && fresh.Status.ResolvedDecision.ResolvedByCommand.Name == command.Name {
				return false, nil
			}
			if fresh.Status.PendingDecision == nil || fresh.Status.PendingDecision.ID != command.Spec.ResolveDecision.DecisionID {
				return false, rejectMaintainerCommand("resolveDecision does not match pendingDecision")
			}
			if fresh.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence || fresh.ResourceVersion != command.Spec.Preconditions.ResourceVersion {
				return false, rejectMaintainerCommand(currentProjectionMessage(fresh))
			}
			now := metav1.Now()
			fresh.Status.PendingDecision = nil
			fresh.Status.ResolvedDecision = &triggersv1alpha1.MaintainerResolvedDecision{ID: command.Spec.ResolveDecision.DecisionID, HumanSubject: command.Spec.ResolveDecision.HumanAnswer.Subject, Answer: command.Spec.ResolveDecision.HumanAnswer.Answer, ResolvedAt: now, ResolvedByCommand: corev1.LocalObjectReference{Name: command.Name}}
			fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseTriaged
			fresh.Status.ProjectionSequence++
			return true, nil
		})
	case triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem:
		if !ModeExistsFromK8s(ctx, r.Client)(strings.ToLower(strings.TrimSpace(command.Spec.Dispatch.Mode))) {
			return rejectMaintainerCommand("dispatch ModeTemplate does not exist")
		}
		fresh := &triggersv1alpha1.MaintainerWorkItem{}
		if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(item), fresh); err != nil {
			return err
		}
		replay := fresh.Status.DispatchReservation != nil && fresh.Status.DispatchReservation.CommandRef.Name == command.Name
		if fresh.Status.DispatchReservation != nil && !replay {
			return rejectMaintainerCommand("work item was already dispatched")
		}
		if !replay && fresh.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseReadyToDispatch {
			return rejectMaintainerCommand("work item is not in the pre-dispatch phase")
		}
		if fresh.Status.PendingDecision != nil {
			return rejectMaintainerCommand("work item has a pending decision")
		}
		if fresh.Status.Readiness == nil || !fresh.Status.Readiness.ReadyToDispatch {
			return rejectMaintainerCommand("dependencies are not delivered")
		}
		if !replay && (fresh.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence || fresh.ResourceVersion != command.Spec.Preconditions.ResourceVersion) {
			return rejectMaintainerCommand(currentProjectionMessage(fresh))
		}
		item = fresh
		if err := r.reserveMaintainerDispatch(ctx, repository, command, item); err != nil {
			return err
		}
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			fresh := &triggersv1alpha1.MaintainerWorkItem{}
			if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(item), fresh); err != nil {
				return err
			}
			fresh.Spec.RequiredPullRequests = append([]triggersv1alpha1.MaintainerRequiredPullRequestIntent(nil), command.Spec.Dispatch.RequiredPullRequests...)
			return r.Update(ctx, fresh)
		})
	default:
		return rejectMaintainerCommand("unsupported execution command")
	}
}

func (r *GitHubRepositoryReconciler) validateBreakdown(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, itemName string, children, dependencies []triggersv1alpha1.MaintainerWorkItemReference) error {
	items := &triggersv1alpha1.MaintainerWorkItemList{}
	if err := r.maintainerReader().List(ctx, items, client.InNamespace(repository.Namespace), client.MatchingLabels{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name}); err != nil {
		return err
	}
	byName := make(map[string]*triggersv1alpha1.MaintainerWorkItem, len(items.Items))
	for i := range items.Items {
		byName[items.Items[i].Name] = &items.Items[i]
	}
	for _, ref := range append(append([]triggersv1alpha1.MaintainerWorkItemReference(nil), children...), dependencies...) {
		target := byName[ref.Name]
		if target == nil {
			return rejectMaintainerCommand("referenced work item does not exist: " + ref.Name)
		}
		if ref.UID == "" {
			return rejectMaintainerCommand("referenced work item UID is required: " + ref.Name)
		}
		if ref.UID != target.UID {
			return rejectMaintainerCommand("referenced work item UID mismatch: " + ref.Name)
		}
		if ref.Name == itemName {
			return rejectMaintainerCommand("work item cannot depend on itself")
		}
	}
	graph := map[string][]string{}
	for i := range items.Items {
		for _, ref := range items.Items[i].Spec.Dependencies {
			graph[items.Items[i].Name] = append(graph[items.Items[i].Name], ref.Name)
		}
	}
	graph[itemName] = nil
	for _, ref := range dependencies {
		graph[itemName] = append(graph[itemName], ref.Name)
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(name string) bool {
		if visiting[name] {
			return true
		}
		if visited[name] {
			return false
		}
		visiting[name] = true
		if slices.ContainsFunc(graph[name], visit) {
			return true
		}
		visiting[name] = false
		visited[name] = true
		return false
	}
	if visit(itemName) {
		return rejectMaintainerCommand("dependency cycle rejected")
	}
	return nil
}

type maintainerRepositoryCommandLock struct {
	CommandName string      `json:"commandName"`
	AcquiredAt  metav1.Time `json:"acquiredAt"`
}

func (r *GitHubRepositoryReconciler) acquireMaintainerCommandLock(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, commandName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.GitHubRepository{}
		if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(repository), fresh); err != nil {
			return err
		}
		var lock maintainerRepositoryCommandLock
		if raw := fresh.Annotations[triggersv1alpha1.MaintainerCommandLockAnnotation]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &lock); err != nil {
				return fmt.Errorf("invalid maintainer command lock: %w", err)
			}
			if lock.CommandName != commandName && time.Since(lock.AcquiredAt.Time) < 5*time.Minute {
				return fmt.Errorf("maintainer graph mutation is serialized behind command %s", lock.CommandName)
			}
		}
		encoded, _ := json.Marshal(maintainerRepositoryCommandLock{CommandName: commandName, AcquiredAt: metav1.Now()})
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[triggersv1alpha1.MaintainerCommandLockAnnotation] = string(encoded)
		return r.Patch(ctx, fresh, patch)
	})
}

func (r *GitHubRepositoryReconciler) releaseMaintainerCommandLock(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, commandName string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.GitHubRepository{}
		if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(repository), fresh); err != nil {
			return err
		}
		var lock maintainerRepositoryCommandLock
		raw := fresh.Annotations[triggersv1alpha1.MaintainerCommandLockAnnotation]
		if raw == "" {
			return nil
		}
		if err := json.Unmarshal([]byte(raw), &lock); err != nil {
			return err
		}
		if lock.CommandName != commandName {
			return nil
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		delete(fresh.Annotations, triggersv1alpha1.MaintainerCommandLockAnnotation)
		return r.Patch(ctx, fresh, patch)
	})
}

type maintainerRepositoryDispatchLedger struct {
	Day          string                                     `json:"day"`
	Count        int                                        `json:"count"`
	Reservations map[string]maintainerRepositoryReservation `json:"reservations"`
}

type maintainerRepositoryReservation struct {
	CommandName string      `json:"commandName"`
	ReservedAt  metav1.Time `json:"reservedAt"`
}

//nolint:gocyclo // Reservation validates all cap, replay, migration, and ownership invariants atomically.
func (r *GitHubRepositoryReconciler) reserveMaintainerDispatch(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem) error {
	items := &triggersv1alpha1.MaintainerWorkItemList{}
	if err := r.maintainerReader().List(ctx, items, client.InNamespace(repository.Namespace), client.MatchingLabels{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name}); err != nil {
		return err
	}
	materialized := map[string]bool{}
	workItemUIDs := map[string]string{}
	for i := range items.Items {
		materialized[items.Items[i].Name] = items.Items[i].Status.DispatchReservation != nil && items.Items[i].Status.DispatchReservation.AgentRunRef != nil
		workItemUIDs[items.Items[i].Name] = string(items.Items[i].UID)
	}
	runs := &platformv1alpha1.AgentRunList{}
	if err := r.maintainerReader().List(ctx, runs, client.InNamespace(repository.Namespace)); err != nil {
		return err
	}
	active := 0
	for i := range runs.Items {
		run := &runs.Items[i]
		workItemName := run.Labels[triggersv1alpha1.MaintainerWorkItemNameLabelKey]
		correlated := workItemUIDs[workItemName] != "" && workItemUIDs[workItemName] == run.Labels[triggersv1alpha1.MaintainerWorkItemUIDLabelKey]
		legacyOwned := TriggerRunMatches(run, gitHubRepositoryTriggerKind, repository.Name) && runOwnedByGitHubRepository(run, repository)
		if (correlated || legacyOwned) && run.Labels[triggersv1alpha1.PRLoopRoleLabelKey] != triggersv1alpha1.PRLoopRoleReviewerValue && !isTerminalAgentRunPhase(run.Status.Phase) {
			active++
		}
	}
	maxConcurrent, maxDaily := int32(2), int32(10)
	if repository.Spec.Maintainer != nil {
		if repository.Spec.Maintainer.MaxConcurrentDispatches > 0 {
			maxConcurrent = repository.Spec.Maintainer.MaxConcurrentDispatches
		}
		if repository.Spec.Maintainer.MaxDispatchesPerDay > 0 {
			maxDaily = repository.Spec.Maintainer.MaxDispatchesPerDay
		}
	}
	now := time.Now().UTC()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.GitHubRepository{}
		if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(repository), fresh); err != nil {
			return err
		}
		ledger := maintainerRepositoryDispatchLedger{Day: now.Format("2006-01-02"), Reservations: map[string]maintainerRepositoryReservation{}}
		if fresh.Annotations != nil {
			if raw := fresh.Annotations[triggersv1alpha1.MaintainerDispatchReservationsAnnotation]; raw != "" {
				if err := json.Unmarshal([]byte(raw), &ledger); err != nil {
					return fmt.Errorf("invalid repository dispatch ledger: %w", err)
				}
			}
		}
		if ledger.Count < 0 {
			return fmt.Errorf("invalid negative repository dispatch count")
		}
		if ledger.Day != now.Format("2006-01-02") {
			ledger.Day, ledger.Count = now.Format("2006-01-02"), 0
		}
		if ledger.Reservations == nil {
			ledger.Reservations = map[string]maintainerRepositoryReservation{}
		}
		if existing, ok := ledger.Reservations[item.Name]; ok {
			if existing.CommandName != command.Name {
				return rejectMaintainerCommand("work item capacity is reserved by another command")
			}
			return nil
		}
		pending := 0
		for workItemName := range ledger.Reservations {
			if !materialized[workItemName] {
				pending++
			}
		}
		if int32(active+pending) >= maxConcurrent {
			return rejectMaintainerCommand(fmt.Sprintf("dispatch concurrency cap reached (%d/%d)", active+pending, maxConcurrent))
		}
		if int32(ledger.Count) >= maxDaily {
			return rejectMaintainerCommand(fmt.Sprintf("daily dispatch cap reached (%d/%d)", ledger.Count, maxDaily))
		}
		ledger.Count++
		ledger.Reservations[item.Name] = maintainerRepositoryReservation{CommandName: command.Name, ReservedAt: metav1.NewTime(now)}
		encoded, err := json.Marshal(ledger)
		if err != nil {
			return err
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[triggersv1alpha1.MaintainerDispatchReservationsAnnotation] = string(encoded)
		return r.Patch(ctx, fresh, patch)
	}); err != nil {
		return err
	}
	return r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
		if fresh.Status.DispatchReservation != nil {
			if fresh.Status.DispatchReservation.CommandRef.Name == command.Name {
				return false, nil
			}
			return false, rejectMaintainerCommand("dispatch capacity reservation is owned by another command")
		}
		fresh.Status.DispatchReservation = &triggersv1alpha1.MaintainerDispatchReservation{ID: command.Spec.IdempotencyKey, CommandRef: corev1.LocalObjectReference{Name: command.Name}, ReservedAt: metav1.NewTime(now)}
		fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseDispatched
		fresh.Status.ProjectionSequence++
		return true, nil
	})
}
