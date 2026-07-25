package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *GitHubRepositoryReconciler) processMaintainerExecutionCommand(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, githubClient GitHubTriageClient, deliveryClient maintainerGitHubDeliveryClient, pending bool) error {
	switch command.Spec.Type {
	case triggersv1alpha1.MaintainerWorkItemCommandTypeRequestMerge:
		return r.processMaintainerRequestMerge(ctx, repository, command, item, deliveryClient, pending)
	case triggersv1alpha1.MaintainerWorkItemCommandTypeFinalizeWorkItem:
		return r.processMaintainerFinalizeWorkItem(ctx, repository, command, item, githubClient, deliveryClient, pending)
	}
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
	active, err := r.maintainerDispatchReservationActive(ctx, repository, command, item)
	if err != nil {
		return err
	}
	if !active {
		return r.failAndReleaseMaintainerDispatch(ctx, repository, command, item, "dispatch reservation is no longer active")
	}
	if githubClient == nil {
		return fmt.Errorf("GitHub client unavailable after dispatch capacity reservation")
	}
	issue, _, err := githubClient.GetIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber))
	if err != nil {
		if isDefiniteGitHubDispatchError(err) {
			return r.failAndReleaseMaintainerDispatch(ctx, repository, command, item, "getting issue after dispatch reservation: "+err.Error())
		}
		return fmt.Errorf("getting issue after dispatch reservation: %w", err)
	}
	if !strings.EqualFold(issue.GetState(), "open") {
		return r.failAndReleaseMaintainerDispatch(ctx, repository, command, item, fmt.Sprintf("issue #%d is no longer open after dispatch reservation", item.Spec.IssueNumber))
	}
	if _, _, err := githubClient.AddLabelsToIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber), []string{command.Spec.Dispatch.Mode}); err != nil {
		if isDefiniteGitHubDispatchError(err) {
			return r.failAndReleaseMaintainerDispatch(ctx, repository, command, item, "applying trigger label after dispatch reservation: "+err.Error())
		}
		return fmt.Errorf("applying trigger label after dispatch reservation: %w", err)
	}
	if err := r.recreateMaintainerDispatchRun(ctx, repository, command, item, issue); err != nil {
		var rejected maintainerCommandRejectedError
		if asMaintainerCommandRejected(err, &rejected) {
			return r.failAndReleaseMaintainerDispatch(ctx, repository, command, item, rejected.message)
		}
		return err
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
			if equality.Semantic.DeepEqual(fresh.Spec.Children, command.Spec.Breakdown.Children) && equality.Semantic.DeepEqual(fresh.Spec.Dependencies, command.Spec.Breakdown.Dependencies) && fresh.Spec.GraphConfiguredByCommand != nil && fresh.Spec.GraphConfiguredByCommand.Name == command.Name {
				return nil
			}
			if fresh.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence {
				return rejectMaintainerCommand(currentProjectionMessage(fresh))
			}
			if fresh.Spec.TriagedByCommand == nil {
				return rejectMaintainerCommand("work item must be triaged before its graph is configured")
			}
			if err := r.validateBreakdown(ctx, repository, item.Name, command.Spec.Breakdown.Children, command.Spec.Breakdown.Dependencies); err != nil {
				return err
			}
			fresh.Spec.Children = append([]triggersv1alpha1.MaintainerWorkItemReference(nil), command.Spec.Breakdown.Children...)
			fresh.Spec.Dependencies = append([]triggersv1alpha1.MaintainerWorkItemReference(nil), command.Spec.Breakdown.Dependencies...)
			fresh.Spec.GraphConfiguredByCommand = &corev1.LocalObjectReference{Name: command.Name}
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
			if fresh.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence {
				return false, rejectMaintainerCommand(currentProjectionMessage(fresh))
			}
			now := metav1.Now()
			fresh.Status.PendingDecision = &triggersv1alpha1.MaintainerPendingDecision{ID: command.Spec.RequestDecision.DecisionID, Question: command.Spec.RequestDecision.Question, Options: append([]string(nil), command.Spec.RequestDecision.Options...), RequestedAt: now, RequestedByCommand: &corev1.LocalObjectReference{Name: command.Name}}
			fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseAwaitingDecision
			fresh.Status.ProjectionSequence++
			return true, nil
		})
	case triggersv1alpha1.MaintainerWorkItemCommandTypeResolveDecision:
		return rejectMaintainerCommand("resolveDecision commands from AgentRuns are not authorized")
	case triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem:
		configuredMode, err := configuredMaintainerDispatchMode(repository)
		if err != nil {
			return rejectMaintainerCommand(err.Error())
		}
		requestedMode := strings.ToLower(strings.TrimSpace(command.Spec.Dispatch.Mode))
		if requestedMode != configuredMode {
			return rejectMaintainerCommand(fmt.Sprintf("dispatch mode %q does not match controller-owned mode %q", requestedMode, configuredMode))
		}
		if !ModeExistsFromK8s(ctx, r.Client)(configuredMode) {
			return rejectMaintainerCommand(fmt.Sprintf("configured dispatch ModeTemplate %q does not exist", configuredMode))
		}
		fresh := &triggersv1alpha1.MaintainerWorkItem{}
		if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(item), fresh); err != nil {
			return err
		}
		replay := fresh.Status.DispatchReservation != nil && fresh.Status.DispatchReservation.CommandRef.Name == command.Name
		if replay {
			// The reservation is the durable application boundary. Replays must
			// proceed to verify or finish side effects even though readiness is
			// intentionally false while that reservation exists.
			return nil
		}
		if fresh.Status.DispatchReservation != nil {
			return r.replaceOrphanedMaintainerDispatch(ctx, repository, command, fresh)
		}
		if fresh.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseReadyToDispatch {
			return rejectMaintainerCommand("work item is not in the pre-dispatch phase")
		}
		if fresh.Spec.GraphConfiguredByCommand == nil {
			return rejectMaintainerCommand("work item graph has not been explicitly configured")
		}
		if fresh.Generation > 0 {
			projectionCurrent := false
			for _, condition := range fresh.Status.Conditions {
				if condition.Type == triggersv1alpha1.ConditionMaintainerWorkItemDependenciesReady && condition.ObservedGeneration == fresh.Generation {
					projectionCurrent = true
					break
				}
			}
			if !projectionCurrent {
				return rejectMaintainerCommand("readiness projection has not observed the current work-item graph generation")
			}
		}
		if fresh.Status.PendingDecision != nil {
			return rejectMaintainerCommand("work item has a pending decision")
		}
		if fresh.Status.Readiness == nil || !fresh.Status.Readiness.ReadyToDispatch {
			return rejectMaintainerCommand("dependencies are not delivered")
		}
		if !replay && fresh.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence {
			return rejectMaintainerCommand(currentProjectionMessage(fresh))
		}
		item = fresh
		// Required pull requests are intentionally not persisted: a monitor name
		// is derived from the implementer run UID and the pull request URL, so no
		// caller can name one before dispatch. Delivery evidence comes from the
		// monitors actually bound to this work item's implementer runs.
		return r.reserveMaintainerDispatch(ctx, repository, command, item)
	default:
		return rejectMaintainerCommand("unsupported execution command")
	}
}

// replaceOrphanedMaintainerDispatch permits a fresh authenticated dispatch to
// replace a materialized reservation only after its exact controller-derived
// implementer has disappeared. The existing ledger entry must still own a
// capacity slot, so recovery cannot bypass concurrency accounting.
func (r *GitHubRepositoryReconciler) replaceOrphanedMaintainerDispatch(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem) error {
	reservation := item.Status.DispatchReservation
	if reservation == nil || reservation.AgentRunRef == nil || reservation.AgentRunRef.Name == "" {
		return rejectMaintainerCommand("work item was already dispatched")
	}
	if item.Name != MaintainerWorkItemName(repository.Name, item.Spec.IssueNumber) || item.Spec.RepositoryRef.Name != repository.Name || !item.DeletionTimestamp.IsZero() || !runOwnerMatchesRepository(item.OwnerReferences, repository) {
		return rejectMaintainerCommand("existing dispatch does not belong to the current repository work item")
	}
	expectedRunName := ghIssueName(repository.Spec.Owner, repository.Spec.Repo, strconv.Itoa(int(item.Spec.IssueNumber)))
	if reservation.AgentRunRef.Name != expectedRunName {
		return rejectMaintainerCommand("existing dispatch run does not match the controller-derived implementer identity")
	}
	run := &platformv1alpha1.AgentRun{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKey{Namespace: repository.Namespace, Name: expectedRunName}, run); err == nil {
		return rejectMaintainerCommand("work item was already dispatched and its implementer still exists")
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking existing dispatch run: %w", err)
	}
	oldCommand := &triggersv1alpha1.MaintainerWorkItemCommand{ObjectMeta: metav1.ObjectMeta{Name: reservation.CommandRef.Name}}
	if item.Spec.GraphConfiguredByCommand == nil {
		return rejectMaintainerCommand("work item graph has not been explicitly configured")
	}
	if item.Generation > 0 {
		projectionCurrent := false
		for _, condition := range item.Status.Conditions {
			if condition.Type == triggersv1alpha1.ConditionMaintainerWorkItemDependenciesReady && condition.ObservedGeneration == item.Generation {
				projectionCurrent = true
				break
			}
		}
		if !projectionCurrent {
			return rejectMaintainerCommand("readiness projection has not observed the current work-item graph generation")
		}
	}
	candidate := item.DeepCopy()
	candidate.Status.DispatchReservation = nil
	evaluateMaintainerReadiness(candidate, time.Now(), repository.Spec.Maintainer != nil && repository.Spec.Maintainer.FullControl)
	if candidate.Status.Readiness == nil || !candidate.Status.Readiness.ReadyToDispatch {
		return rejectMaintainerCommand("work item is not ready for recovery dispatch")
	}
	if err := retryMaintainerWorkItemCommandStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(command), func(fresh *triggersv1alpha1.MaintainerWorkItemCommand) {
		if fresh.Status.Result == nil {
			fresh.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{WorkItemRef: corev1.LocalObjectReference{Name: item.Name}}
		}
		fresh.Status.Result.AgentRunRef = &corev1.LocalObjectReference{Name: expectedRunName}
		fresh.Status.Result.Message = "authenticated recovery dispatch validated"
	}); err != nil {
		return fmt.Errorf("recording validated dispatch recovery: %w", err)
	}
	command.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{WorkItemRef: corev1.LocalObjectReference{Name: item.Name}, AgentRunRef: &corev1.LocalObjectReference{Name: expectedRunName}}
	return r.transferMaintainerDispatchReservation(ctx, repository, oldCommand.Name, command, item)
}

func (r *GitHubRepositoryReconciler) transferMaintainerDispatchReservation(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, oldCommandName string, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem) error {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.GitHubRepository{}
		if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(repository), fresh); err != nil {
			return err
		}
		ledger := maintainerRepositoryDispatchLedger{}
		raw := fresh.Annotations[triggersv1alpha1.MaintainerDispatchReservationsAnnotation]
		if raw == "" {
			return rejectMaintainerCommand("existing dispatch no longer owns a recoverable capacity reservation")
		}
		if err := json.Unmarshal([]byte(raw), &ledger); err != nil {
			return fmt.Errorf("invalid repository dispatch ledger: %w", err)
		}
		entry, ok := ledger.Reservations[item.Name]
		if !ok {
			return rejectMaintainerCommand("existing dispatch no longer owns a recoverable capacity reservation")
		}
		switch entry.CommandName {
		case command.Name:
			return nil
		case oldCommandName:
			entry.CommandName = command.Name
			ledger.Reservations[item.Name] = entry
		default:
			return rejectMaintainerCommand("dispatch capacity reservation changed owners")
		}
		encoded, err := json.Marshal(ledger)
		if err != nil {
			return err
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		fresh.Annotations[triggersv1alpha1.MaintainerDispatchReservationsAnnotation] = string(encoded)
		return r.Patch(ctx, fresh, patch)
	}); err != nil {
		return err
	}
	return r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
		reservation := fresh.Status.DispatchReservation
		if reservation == nil {
			return false, rejectMaintainerCommand("dispatch reservation disappeared during recovery")
		}
		switch reservation.CommandRef.Name {
		case command.Name:
			return false, nil
		case oldCommandName:
			reservation.ID = command.Spec.IdempotencyKey
			reservation.CommandRef.Name = command.Name
			reservation.AgentRunRef = nil
			fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseDispatched
			fresh.Status.ProjectionSequence++
			return true, nil
		default:
			return false, rejectMaintainerCommand("dispatch reservation changed owners during recovery")
		}
	})
}

func runOwnerMatchesRepository(owners []metav1.OwnerReference, repository *triggersv1alpha1.GitHubRepository) bool {
	for _, owner := range owners {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == gitHubRepositoryTriggerKind && owner.Name == repository.Name && owner.UID == repository.UID {
			return true
		}
	}
	return false
}

// recreateMaintainerDispatchRun performs the wake-up side effect for a
// recovery command. Ordinary dispatches continue to use the label/webhook path.
func (r *GitHubRepositoryReconciler) recreateMaintainerDispatchRun(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, issue *github.Issue) error {
	if command.Status.Result == nil || command.Status.Result.AgentRunRef == nil {
		return nil
	}
	recoveryRunName := command.Status.Result.AgentRunRef.Name
	expectedRunName := ghIssueName(repository.Spec.Owner, repository.Spec.Repo, strconv.Itoa(int(item.Spec.IssueNumber)))
	if recoveryRunName != expectedRunName {
		return fmt.Errorf("validated dispatch recovery names unexpected AgentRun %q", recoveryRunName)
	}
	fresh := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(item), fresh); err != nil {
		return err
	}
	if fresh.Status.DispatchReservation == nil || fresh.Status.DispatchReservation.CommandRef.Name != command.Name {
		return fmt.Errorf("dispatch recovery reservation is no longer owned by command %s", command.Name)
	}
	if fresh.Spec.GraphConfiguredByCommand == nil || fresh.Status.PendingDecision != nil {
		return rejectMaintainerCommand("work item is no longer ready for recovery dispatch")
	}
	if fresh.Generation > 0 {
		projectionCurrent := false
		for _, condition := range fresh.Status.Conditions {
			if condition.Type == triggersv1alpha1.ConditionMaintainerWorkItemDependenciesReady && condition.ObservedGeneration == fresh.Generation {
				projectionCurrent = true
				break
			}
		}
		if !projectionCurrent {
			return rejectMaintainerCommand("readiness projection has not observed the current work-item graph generation")
		}
	}
	candidate := fresh.DeepCopy()
	candidate.Status.DispatchReservation = nil
	evaluateMaintainerReadiness(candidate, time.Now(), repository.Spec.Maintainer != nil && repository.Spec.Maintainer.FullControl)
	if candidate.Status.Readiness == nil || !candidate.Status.Readiness.ReadyToDispatch {
		return rejectMaintainerCommand("work item is no longer ready for recovery dispatch")
	}
	runs := &platformv1alpha1.AgentRunList{}
	if err := r.maintainerReader().List(ctx, runs, client.InNamespace(repository.Namespace)); err != nil {
		return err
	}
	if hasOtherActiveMaintainerImplementer(repository, fresh, expectedRunName, runs.Items) {
		return rejectMaintainerCommand("another implementer is already active for this work item")
	}
	active, err := r.maintainerDispatchReservationActive(ctx, repository, command, fresh)
	if err != nil {
		return err
	}
	if !active {
		return rejectMaintainerCommand("recovery dispatch no longer owns its capacity reservation")
	}
	issueID := strconv.Itoa(issue.GetNumber())
	request := fmt.Sprintf("# %s\n\n%s", issue.GetTitle(), issue.GetBody())
	if _, err := r.createAgentRun(ctx, repository, issueID, issue.GetNumber(), issue.GetHTMLURL(), request, issue.GetUser().GetLogin(), &platformv1alpha1.ModeRef{Name: command.Spec.Dispatch.Mode}); err != nil {
		return fmt.Errorf("recreating deleted implementer %s: %w", expectedRunName, err)
	}
	return nil
}

func hasOtherActiveMaintainerImplementer(repository *triggersv1alpha1.GitHubRepository, item *triggersv1alpha1.MaintainerWorkItem, expectedRunName string, runs []platformv1alpha1.AgentRun) bool {
	for i := range runs {
		run := &runs[i]
		if run.Name == expectedRunName || run.Labels[triggersv1alpha1.PRLoopRoleLabelKey] == triggersv1alpha1.PRLoopRoleReviewerValue || isTerminalAgentRunPhase(run.Status.Phase) {
			continue
		}
		correlated := run.Labels[triggersv1alpha1.MaintainerWorkItemNameLabelKey] == item.Name && run.Labels[triggersv1alpha1.MaintainerWorkItemUIDLabelKey] == string(item.UID)
		legacyCorrelated := TriggerRunMatches(run, gitHubRepositoryTriggerKind, repository.Name) && runOwnedByGitHubRepository(run, repository) && maintainerRunWorkItemName(repository, run) == item.Name
		if correlated || legacyCorrelated {
			return true
		}
	}
	return false
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

func isDefiniteGitHubDispatchError(err error) bool {
	var responseError *github.ErrorResponse
	if !errors.As(err, &responseError) || responseError.Response == nil {
		return false
	}
	status := responseError.Response.StatusCode
	return status >= 400 && status < 500 && status != 408 && status != 409 && status != 429
}

func (r *GitHubRepositoryReconciler) maintainerDispatchReservationActive(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
	freshItem := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(item), freshItem); err != nil {
		return false, err
	}
	if freshItem.Status.DispatchReservation == nil || freshItem.Status.DispatchReservation.CommandRef.Name != command.Name {
		return false, nil
	}
	freshRepository := &triggersv1alpha1.GitHubRepository{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(repository), freshRepository); err != nil {
		return false, err
	}
	raw := freshRepository.Annotations[triggersv1alpha1.MaintainerDispatchReservationsAnnotation]
	if raw == "" {
		return false, nil
	}
	ledger := maintainerRepositoryDispatchLedger{}
	if err := json.Unmarshal([]byte(raw), &ledger); err != nil {
		return false, err
	}
	reservation, ok := ledger.Reservations[item.Name]
	return ok && reservation.CommandName == command.Name, nil
}

func (r *GitHubRepositoryReconciler) failAndReleaseMaintainerDispatch(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, message string) error {
	if err := r.releaseMaintainerDispatch(ctx, repository, command, item); err != nil {
		return err
	}
	return r.failMaintainerWorkItemCommand(ctx, command, item, message)
}

func (r *GitHubRepositoryReconciler) releaseMaintainerDispatch(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem) error {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.GitHubRepository{}
		if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(repository), fresh); err != nil {
			return err
		}
		raw := fresh.Annotations[triggersv1alpha1.MaintainerDispatchReservationsAnnotation]
		if raw == "" {
			return nil
		}
		ledger := maintainerRepositoryDispatchLedger{}
		if err := json.Unmarshal([]byte(raw), &ledger); err != nil {
			return err
		}
		reservation, ok := ledger.Reservations[item.Name]
		if !ok || reservation.CommandName != command.Name {
			return nil
		}
		delete(ledger.Reservations, item.Name)
		// Only give back today's budget for reservations made today; a release
		// after midnight must not discount dispatches from a previous day.
		if ledger.Count > 0 && reservation.ReservedAt.Time.UTC().Format("2006-01-02") == ledger.Day {
			ledger.Count--
		}
		encoded, err := json.Marshal(ledger)
		if err != nil {
			return err
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		fresh.Annotations[triggersv1alpha1.MaintainerDispatchReservationsAnnotation] = string(encoded)
		return r.Patch(ctx, fresh, patch)
	}); err != nil {
		return err
	}
	return r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
		if fresh.Status.DispatchReservation == nil || fresh.Status.DispatchReservation.CommandRef.Name != command.Name {
			return false, nil
		}
		fresh.Status.DispatchReservation = nil
		fresh.Status.ProjectionSequence++
		return true, nil
	})
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

// maintainerDispatchReservationTTL bounds how long an unmaterialized
// reservation with no correlated active run may hold a capacity slot.
const maintainerDispatchReservationTTL = 24 * time.Hour

// pruneMaintainerDispatchReservations removes ledger entries that can no
// longer legitimately hold capacity: entries whose work item was deleted,
// entries whose bound run already finished, and unmaterialized entries older
// than the TTL with no correlated active run. Without this, orphaned entries
// count against the concurrency cap forever. The entry for protect (the work
// item currently being reserved) is never pruned so an in-flight dispatch
// cannot lose its own reservation.
func pruneMaintainerDispatchReservations(ledger *maintainerRepositoryDispatchLedger, protect string, workItemUIDs map[string]string, materialized, activeItems map[string]bool, now time.Time) bool {
	pruned := false
	for name, reservation := range ledger.Reservations {
		if name == protect {
			continue
		}
		switch {
		case workItemUIDs[name] == "":
			// The work item no longer exists; its reservation is unreleasable.
		case materialized[name] && !activeItems[name]:
			// The reservation was bound to a run that is now terminal or gone.
		case !materialized[name] && !activeItems[name] && now.Sub(reservation.ReservedAt.Time) > maintainerDispatchReservationTTL:
			// The reservation never materialized and has no active run.
		default:
			continue
		}
		delete(ledger.Reservations, name)
		pruned = true
	}
	return pruned
}

// maintainerRunWorkItemName derives the deterministic work-item name for a
// repository-owned run that predates work-item labels, so legacy runs dedupe
// against ledger reservations by identity instead of double-counting.
func maintainerRunWorkItemName(repository *triggersv1alpha1.GitHubRepository, run *platformv1alpha1.AgentRun) string {
	if run.Spec.Trigger.ExternalRef == nil {
		return ""
	}
	raw := strings.TrimPrefix(strings.TrimSpace(run.Spec.Trigger.ExternalRef.Identifier), "#")
	if number, err := strconv.ParseInt(raw, 10, 32); err == nil && number > 0 {
		return MaintainerWorkItemName(repository.Name, int32(number))
	}
	if number, err := strconv.ParseInt(strings.TrimSpace(run.Spec.Trigger.ExternalRef.ID), 10, 32); err == nil && number > 0 {
		return MaintainerWorkItemName(repository.Name, int32(number))
	}
	return ""
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
	// Deduplicate capacity by work-item identity: a work item must never
	// consume one slot through its active run and a second slot through its
	// not-yet-materialized ledger reservation.
	activeItems := map[string]bool{}
	legacyActive := 0
	for i := range runs.Items {
		run := &runs.Items[i]
		if run.Labels[triggersv1alpha1.PRLoopRoleLabelKey] == triggersv1alpha1.PRLoopRoleReviewerValue || isTerminalAgentRunPhase(run.Status.Phase) {
			continue
		}
		// Standing runs (the maintainer itself) are supervisors, not dispatched
		// work; counting them would permanently consume a capacity slot.
		if run.Labels[orchestration.StandingRunRoleLabel] != "" {
			continue
		}
		workItemName := run.Labels[triggersv1alpha1.MaintainerWorkItemNameLabelKey]
		correlated := workItemUIDs[workItemName] != "" && workItemUIDs[workItemName] == run.Labels[triggersv1alpha1.MaintainerWorkItemUIDLabelKey]
		legacyOwned := TriggerRunMatches(run, gitHubRepositoryTriggerKind, repository.Name) && runOwnedByGitHubRepository(run, repository)
		switch {
		case correlated:
			activeItems[workItemName] = true
		case legacyOwned:
			if name := maintainerRunWorkItemName(repository, run); name != "" {
				activeItems[name] = true
			} else {
				legacyActive++
			}
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
		pruned := pruneMaintainerDispatchReservations(&ledger, item.Name, workItemUIDs, materialized, activeItems, now)
		persist := func() error {
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
		}
		if existing, ok := ledger.Reservations[item.Name]; ok {
			if existing.CommandName != command.Name {
				if pruned {
					if err := persist(); err != nil {
						return err
					}
				}
				return rejectMaintainerCommand("work item capacity is reserved by another command")
			}
			if pruned {
				return persist()
			}
			return nil
		}
		pending := 0
		for workItemName := range ledger.Reservations {
			if !materialized[workItemName] && !activeItems[workItemName] {
				pending++
			}
		}
		usage := len(activeItems) + legacyActive + pending
		if int32(usage) >= maxConcurrent {
			if pruned {
				if err := persist(); err != nil {
					return err
				}
			}
			return rejectMaintainerCommand(fmt.Sprintf("dispatch concurrency cap reached (%d/%d)", usage, maxConcurrent))
		}
		if int32(ledger.Count) >= maxDaily {
			if pruned {
				if err := persist(); err != nil {
					return err
				}
			}
			return rejectMaintainerCommand(fmt.Sprintf("daily dispatch cap reached (%d/%d)", ledger.Count, maxDaily))
		}
		ledger.Count++
		ledger.Reservations[item.Name] = maintainerRepositoryReservation{CommandName: command.Name, ReservedAt: metav1.NewTime(now)}
		return persist()
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
