package triggers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const maintainerProjectionFreshness = 5 * time.Minute

// reconcileMaintainerExecutionProjection derives execution facts exclusively
// from Kubernetes records. External references are used once for migration;
// durable correlation thereafter is by work-item name and immutable UID labels.
func (r *GitHubRepositoryReconciler) reconcileMaintainerExecutionProjection(ctx context.Context, repository *triggersv1alpha1.GitHubRepository) error {
	items := &triggersv1alpha1.MaintainerWorkItemList{}
	if err := r.List(ctx, items, client.InNamespace(repository.Namespace), client.MatchingLabels{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name}); err != nil {
		return err
	}
	runs := &platformv1alpha1.AgentRunList{}
	if err := r.List(ctx, runs, client.InNamespace(repository.Namespace)); err != nil {
		return err
	}
	byName := make(map[string]*triggersv1alpha1.MaintainerWorkItem, len(items.Items))
	byIssue := map[int32]*triggersv1alpha1.MaintainerWorkItem{}
	for i := range items.Items {
		byName[items.Items[i].Name] = &items.Items[i]
		byIssue[items.Items[i].Spec.IssueNumber] = &items.Items[i]
	}
	if err := r.migrateMaintainerRunLabels(ctx, repository, runs, byIssue); err != nil {
		return err
	}
	// Reload labels written by migration before projecting.
	if err := r.List(ctx, runs, client.InNamespace(repository.Namespace)); err != nil {
		return err
	}
	monitors := &triggersv1alpha1.PullRequestMonitorList{}
	if err := r.List(ctx, monitors, client.InNamespace(repository.Namespace)); err != nil {
		return err
	}
	now := time.Now()
	for i := range items.Items {
		item := &items.Items[i]
		if err := retryMaintainerWorkItemStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) bool {
			before := fresh.Status.DeepCopy()
			projectMaintainerDependencies(fresh, byName, now)
			projectMaintainerRunsAndPRs(fresh, runs.Items, monitors.Items, now)
			evaluateMaintainerReadiness(fresh, now)
			if maintainerWorkItemStatusSemanticallyEqual(before, &fresh.Status) {
				return false
			}
			fresh.Status.ProjectionSequence++
			return true
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *GitHubRepositoryReconciler) migrateMaintainerRunLabels(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, runs *platformv1alpha1.AgentRunList, byIssue map[int32]*triggersv1alpha1.MaintainerWorkItem) error {
	for i := range runs.Items {
		run := &runs.Items[i]
		if run.Labels[triggersv1alpha1.MaintainerWorkItemNameLabelKey] != "" {
			continue
		}
		var item *triggersv1alpha1.MaintainerWorkItem
		if run.Labels[triggersv1alpha1.PRLoopRoleLabelKey] == triggersv1alpha1.PRLoopRoleReviewerValue {
			implementerName := run.Annotations[PRLoopImplementerAnnotation]
			for j := range runs.Items {
				if runs.Items[j].Name == implementerName {
					name := runs.Items[j].Labels[triggersv1alpha1.MaintainerWorkItemNameLabelKey]
					item = findWorkItemByName(byIssue, name)
					break
				}
			}
		} else if TriggerRunMatches(run, gitHubRepositoryTriggerKind, repository.Name) && runOwnedByGitHubRepository(run, repository) && run.Spec.Trigger.ExternalRef != nil {
			raw := strings.TrimPrefix(strings.TrimSpace(run.Spec.Trigger.ExternalRef.Identifier), "#")
			if number, err := strconv.ParseInt(raw, 10, 32); err == nil {
				item = byIssue[int32(number)]
			}
			if item == nil {
				if number, err := strconv.ParseInt(strings.TrimSpace(run.Spec.Trigger.ExternalRef.ID), 10, 32); err == nil {
					item = byIssue[int32(number)]
				}
			}
		}
		if item == nil {
			continue
		}
		patch := client.MergeFrom(run.DeepCopy())
		if run.Labels == nil {
			run.Labels = map[string]string{}
		}
		run.Labels[triggersv1alpha1.MaintainerWorkItemNameLabelKey] = item.Name
		run.Labels[triggersv1alpha1.MaintainerWorkItemUIDLabelKey] = string(item.UID)
		if err := r.Patch(ctx, run, patch); err != nil {
			return fmt.Errorf("labeling AgentRun %s with work-item identity: %w", run.Name, err)
		}
	}
	return nil
}

func runOwnedByGitHubRepository(run *platformv1alpha1.AgentRun, repository *triggersv1alpha1.GitHubRepository) bool {
	for _, owner := range run.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == gitHubRepositoryTriggerKind && owner.Name == repository.Name && owner.UID == repository.UID {
			return true
		}
	}
	return false
}

func findWorkItemByName(items map[int32]*triggersv1alpha1.MaintainerWorkItem, name string) *triggersv1alpha1.MaintainerWorkItem {
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	return nil
}

func projectMaintainerDependencies(item *triggersv1alpha1.MaintainerWorkItem, byName map[string]*triggersv1alpha1.MaintainerWorkItem, now time.Time) {
	observed := metav1.NewTime(now)
	item.Status.Children = nil
	item.Status.Dependencies = nil
	for _, ref := range item.Spec.Children {
		target := byName[ref.Name]
		projection := triggersv1alpha1.MaintainerWorkItemChildProjection{Name: ref.Name, ObservedAt: &observed}
		if target != nil && (ref.UID == "" || ref.UID == target.UID) {
			projection.UID, projection.Phase, projection.Delivered = target.UID, target.Status.Phase, target.Status.Phase == triggersv1alpha1.MaintainerWorkItemPhaseDelivered
		}
		item.Status.Children = append(item.Status.Children, projection)
	}
	for _, ref := range item.Spec.Dependencies {
		target := byName[ref.Name]
		projection := triggersv1alpha1.MaintainerWorkItemDependencyProjection{Name: ref.Name, ObservedAt: &observed}
		if target != nil && (ref.UID == "" || ref.UID == target.UID) {
			projection.UID, projection.Phase, projection.Delivered = target.UID, target.Status.Phase, target.Status.Phase == triggersv1alpha1.MaintainerWorkItemPhaseDelivered
		}
		item.Status.Dependencies = append(item.Status.Dependencies, projection)
	}
}

func projectMaintainerRunsAndPRs(item *triggersv1alpha1.MaintainerWorkItem, runs []platformv1alpha1.AgentRun, monitors []triggersv1alpha1.PullRequestMonitor, now time.Time) {
	observed := metav1.NewTime(now)
	item.Status.AgentRuns = nil
	item.Status.PullRequests = nil
	runNames := map[string]bool{}
	for i := range runs {
		run := &runs[i]
		if run.Labels[triggersv1alpha1.MaintainerWorkItemNameLabelKey] != item.Name || run.Labels[triggersv1alpha1.MaintainerWorkItemUIDLabelKey] != string(item.UID) {
			continue
		}
		role := triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer
		if run.Labels[triggersv1alpha1.PRLoopRoleLabelKey] == triggersv1alpha1.PRLoopRoleReviewerValue {
			role = triggersv1alpha1.MaintainerWorkItemAgentRunRoleReviewer
		}
		item.Status.AgentRuns = append(item.Status.AgentRuns, triggersv1alpha1.MaintainerWorkItemAgentRunProjection{Name: run.Name, UID: run.UID, Role: role, Phase: string(run.Status.Phase), PRLoopState: run.Labels[PRLoopStateLabel], ObservedAt: &observed})
		if role == triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer {
			runNames[run.Name] = true
			if item.Status.DispatchReservation != nil && item.Status.DispatchReservation.AgentRunRef == nil {
				item.Status.DispatchReservation.AgentRunRef = &corev1.LocalObjectReference{Name: run.Name}
			}
		}
	}
	required := map[string]string{}
	for _, intent := range item.Spec.RequiredPullRequests {
		required[intent.Name] = intent.Name
	}
	for i := range monitors {
		monitor := &monitors[i]
		if !runNames[monitor.Spec.ImplementerRef.Name] {
			continue
		}
		if len(required) > 0 {
			if _, ok := required[monitor.Name]; !ok {
				continue
			}
			delete(required, monitor.Name)
		}
		projection := maintainerPRProjection(monitor)
		item.Status.PullRequests = append(item.Status.PullRequests, projection)
	}
	for name := range required {
		item.Status.PullRequests = append(item.Status.PullRequests, triggersv1alpha1.MaintainerWorkItemPullRequestProjection{IntentName: name})
	}
	sort.Slice(item.Status.AgentRuns, func(i, j int) bool { return item.Status.AgentRuns[i].Name < item.Status.AgentRuns[j].Name })
	sort.Slice(item.Status.PullRequests, func(i, j int) bool {
		if item.Status.PullRequests[i].Repository == item.Status.PullRequests[j].Repository {
			return item.Status.PullRequests[i].Number < item.Status.PullRequests[j].Number
		}
		return item.Status.PullRequests[i].Repository < item.Status.PullRequests[j].Repository
	})
}

func maintainerPRProjection(monitor *triggersv1alpha1.PullRequestMonitor) triggersv1alpha1.MaintainerWorkItemPullRequestProjection {
	p := triggersv1alpha1.MaintainerWorkItemPullRequestProjection{Repository: monitor.Spec.Repository, Number: monitor.Spec.Number, IntentName: monitor.Name, MonitorRef: &corev1.LocalObjectReference{Name: monitor.Name}, URL: monitor.Spec.URL, HeadSHA: monitor.Status.HeadSHA, Draft: monitor.Status.Lifecycle == triggersv1alpha1.PullRequestLifecycleDraft, ReviewDecision: string(monitor.Status.ReviewDecision), HeadObservedAt: timePtr(monitor.Status.PullObservedAt), ReviewObservedAt: timePtr(monitor.Status.ReviewsObservedAt), ChecksObservedAt: timePtr(monitor.Status.Checks.ObservedAt), StatusesObservedAt: timePtr(monitor.Status.Statuses.ObservedAt), ObservationError: monitor.Status.LastError}
	readyCondition := meta.FindStatusCondition(monitor.Status.Conditions, triggersv1alpha1.ConditionPullRequestMonitorReady)
	p.Fresh = readyCondition != nil && readyCondition.Status == metav1.ConditionTrue && monitor.Status.LastError == "" && monitor.Status.HeadSHA != "" && monitor.Status.Checks.HeadSHA == monitor.Status.HeadSHA && monitor.Status.Statuses.HeadSHA == monitor.Status.HeadSHA
	switch monitor.Status.Lifecycle {
	case triggersv1alpha1.PullRequestLifecycleMerged:
		p.State = triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged
		p.MergedAt = timePtr(monitor.Status.MergedAt)
	case triggersv1alpha1.PullRequestLifecycleClosed:
		p.State = triggersv1alpha1.MaintainerWorkItemPullRequestStateClosed
	default:
		p.State = triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen
	}
	switch monitor.Status.Mergeability {
	case triggersv1alpha1.PullRequestMergeabilityMergeable:
		value := true
		p.Mergeable = &value
	case triggersv1alpha1.PullRequestMergeabilityConflicting:
		value := false
		p.Mergeable = &value
	}
	if monitor.Status.Checks.HeadSHA != monitor.Status.HeadSHA || monitor.Status.Statuses.HeadSHA != monitor.Status.HeadSHA {
		p.CheckState = triggersv1alpha1.MaintainerWorkItemCheckStateUnknown
	} else if monitor.Status.Checks.Error != "" || monitor.Status.Statuses.Error != "" || monitor.Status.Checks.State == gitHubRollupFailure || monitor.Status.Statuses.State == gitHubRollupFailure {
		p.CheckState = triggersv1alpha1.MaintainerWorkItemCheckStateFailing
	} else if monitor.Status.Checks.State == gitHubRollupPending || monitor.Status.Statuses.State == gitHubRollupPending {
		p.CheckState = triggersv1alpha1.MaintainerWorkItemCheckStatePending
	} else if (monitor.Status.Checks.State == gitHubRollupSuccess || monitor.Status.Checks.State == gitHubRollupNone) && (monitor.Status.Statuses.State == gitHubRollupSuccess || monitor.Status.Statuses.State == gitHubRollupNone) && monitor.Status.Checks.Count+monitor.Status.Statuses.Count > 0 {
		p.CheckState = triggersv1alpha1.MaintainerWorkItemCheckStatePassing
	} else {
		p.CheckState = triggersv1alpha1.MaintainerWorkItemCheckStateUnknown
	}
	return p
}
func timePtr(value metav1.Time) *metav1.Time {
	if value.IsZero() {
		return nil
	}
	result := value
	return &result
}

//nolint:gocyclo // Readiness is a fail-closed conjunction over independently projected facts.
func evaluateMaintainerReadiness(item *triggersv1alpha1.MaintainerWorkItem, now time.Time) {
	unmet := []string{}
	dependenciesReady := true
	for _, dep := range item.Status.Dependencies {
		if !dep.Delivered {
			dependenciesReady = false
			unmet = append(unmet, "dependency "+dep.Name+" is not delivered")
		}
	}
	if item.Status.PendingDecision != nil {
		unmet = append(unmet, "pending decision "+item.Status.PendingDecision.ID)
	}
	readyToDispatch := dependenciesReady && item.Status.PendingDecision == nil && item.Status.DispatchReservation == nil && item.Spec.Disposition != "" && item.Spec.Disposition != triggersv1alpha1.MaintainerWorkItemDispositionNotActionable
	readyToMerge := len(item.Status.PullRequests) > 0
	for _, pr := range item.Status.PullRequests {
		identity := fmt.Sprintf("%s#%d", pr.Repository, pr.Number)
		if pr.MonitorRef == nil || pr.Repository == "" || pr.Number < 1 {
			readyToMerge = false
			unmet = append(unmet, "required pull request monitor is missing")
			continue
		}
		if pr.State == triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged {
			continue
		}
		if !pr.Fresh || pr.ObservationError != "" || pr.State != triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen || pr.Draft || pr.Mergeable == nil || !*pr.Mergeable || !strings.EqualFold(pr.ReviewDecision, string(triggersv1alpha1.PullRequestReviewDecisionApproved)) || pr.CheckState != triggersv1alpha1.MaintainerWorkItemCheckStatePassing || pr.HeadObservedAt == nil || pr.ReviewObservedAt == nil || pr.ChecksObservedAt == nil || pr.StatusesObservedAt == nil || now.Sub(pr.HeadObservedAt.Time) > maintainerProjectionFreshness || now.Sub(pr.ReviewObservedAt.Time) > maintainerProjectionFreshness || now.Sub(pr.ChecksObservedAt.Time) > maintainerProjectionFreshness || now.Sub(pr.StatusesObservedAt.Time) > maintainerProjectionFreshness {
			readyToMerge = false
			unmet = append(unmet, identity+" is incomplete, stale, or not merge-ready")
		}
	}
	allMerged := allProjectedPRsMerged(item.Status.PullRequests)
	if allMerged {
		readyToMerge = false
	}
	observed := metav1.NewTime(now)
	item.Status.Readiness = &triggersv1alpha1.MaintainerWorkItemReadiness{ReadyToDispatch: readyToDispatch, ReadyToMerge: readyToMerge, UnmetRequirements: unmet, ObservedAt: &observed}
	meta.SetStatusCondition(&item.Status.Conditions, metav1.Condition{Type: triggersv1alpha1.ConditionMaintainerWorkItemDependenciesReady, Status: boolCondition(dependenciesReady), Reason: conditionReason(dependenciesReady, "Delivered", "Blocked"), ObservedGeneration: item.Generation, LastTransitionTime: observed})
	meta.SetStatusCondition(&item.Status.Conditions, metav1.Condition{Type: triggersv1alpha1.ConditionMaintainerWorkItemReadyToMerge, Status: boolCondition(readyToMerge), Reason: conditionReason(readyToMerge, "Ready", "RequirementsNotMet"), ObservedGeneration: item.Generation, LastTransitionTime: observed})
	if item.Status.PendingDecision != nil {
		item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseAwaitingDecision
	} else if allMerged {
		item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseDelivered
	} else if readyToMerge {
		item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseReadyToMerge
	} else if hasActiveImplementer(item.Status.AgentRuns) {
		item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseImplementing
	} else if item.Status.DispatchReservation != nil {
		item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseDispatched
	} else if readyToDispatch {
		item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseReadyToDispatch
	} else if item.Spec.Disposition != "" {
		item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseTriaged
	} else {
		item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhasePendingTriage
	}
}
func boolCondition(value bool) metav1.ConditionStatus {
	if value {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}
func conditionReason(value bool, yes, no string) string {
	if value {
		return yes
	}
	return no
}
func allProjectedPRsMerged(prs []triggersv1alpha1.MaintainerWorkItemPullRequestProjection) bool {
	if len(prs) == 0 {
		return false
	}
	for _, pr := range prs {
		if pr.State != triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged {
			return false
		}
	}
	return true
}
func hasActiveImplementer(runs []triggersv1alpha1.MaintainerWorkItemAgentRunProjection) bool {
	for _, run := range runs {
		if run.Role == triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer && run.Phase != string(platformv1alpha1.AgentRunPhaseSucceeded) && run.Phase != string(platformv1alpha1.AgentRunPhaseFailed) && run.Phase != string(platformv1alpha1.AgentRunPhaseCancelled) {
			return true
		}
	}
	return false
}
