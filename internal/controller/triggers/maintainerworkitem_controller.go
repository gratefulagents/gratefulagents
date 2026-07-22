package triggers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const maintainerDecisionMarker = "gratefulagents-maintainer-work-item"

// GitHubTriageClient is the narrow GitHub API surface used for maintainer triage.
type GitHubTriageClient interface {
	ListIssueComments(context.Context, string, string, int, *github.IssueListCommentsOptions) ([]*github.IssueComment, *github.Response, error)
	CreateIssueComment(context.Context, string, string, int, *github.IssueComment) (*github.IssueComment, *github.Response, error)
	EditIssueComment(context.Context, string, string, int64, *github.IssueComment) (*github.IssueComment, *github.Response, error)
	GetIssue(context.Context, string, string, int) (*github.Issue, *github.Response, error)
	AddLabelsToIssue(context.Context, string, string, int, []string) ([]*github.Label, *github.Response, error)
	EditIssue(context.Context, string, string, int, *github.IssueRequest) (*github.Issue, *github.Response, error)
}

type githubTriageAdapter struct {
	issues *github.IssuesService
}

func (a githubTriageAdapter) ListIssueComments(ctx context.Context, owner, repo string, number int, opts *github.IssueListCommentsOptions) ([]*github.IssueComment, *github.Response, error) {
	return a.issues.ListComments(ctx, owner, repo, number, opts)
}

func (a githubTriageAdapter) CreateIssueComment(ctx context.Context, owner, repo string, number int, comment *github.IssueComment) (*github.IssueComment, *github.Response, error) {
	return a.issues.CreateComment(ctx, owner, repo, number, comment)
}

func (a githubTriageAdapter) EditIssueComment(ctx context.Context, owner, repo string, id int64, comment *github.IssueComment) (*github.IssueComment, *github.Response, error) {
	return a.issues.EditComment(ctx, owner, repo, id, comment)
}

func (a githubTriageAdapter) GetIssue(ctx context.Context, owner, repo string, number int) (*github.Issue, *github.Response, error) {
	return a.issues.Get(ctx, owner, repo, number)
}

func (a githubTriageAdapter) AddLabelsToIssue(ctx context.Context, owner, repo string, number int, labels []string) ([]*github.Label, *github.Response, error) {
	return a.issues.AddLabelsToIssue(ctx, owner, repo, number, labels)
}

func (a githubTriageAdapter) EditIssue(ctx context.Context, owner, repo string, number int, request *github.IssueRequest) (*github.Issue, *github.Response, error) {
	return a.issues.Edit(ctx, owner, repo, number, request)
}

// MaintainerWorkItemName returns the stable name for a repository issue work item.
func MaintainerWorkItemName(repositoryName string, issueNumber int32) string {
	return triggersv1alpha1.MaintainerWorkItemName(repositoryName, issueNumber)
}

// MaintainerWorkItemCommandName returns the deterministic DNS-safe name for an idempotency key.
func MaintainerWorkItemCommandName(repositoryName, idempotencyKey string) string {
	return triggersv1alpha1.MaintainerWorkItemCommandName(repositoryName, idempotencyKey)
}

// MaintainerWorkItemCommandPayloadHash delegates to the API contract's shared canonical encoder.
func MaintainerWorkItemCommandPayloadHash(commandType triggersv1alpha1.MaintainerWorkItemCommandType, triage *triggersv1alpha1.MaintainerTriageCommand, preconditions triggersv1alpha1.MaintainerWorkItemCommandPreconditions) string {
	return triggersv1alpha1.MaintainerWorkItemCommandPayloadHash(commandType, triage, preconditions)
}

func maintainerWorkItemsEnabled(r *GitHubRepositoryReconciler, repository *triggersv1alpha1.GitHubRepository) bool {
	return r != nil && r.MaintainerEnabled && repository != nil && repository.Spec.Maintainer != nil && !repository.Spec.Maintainer.Disabled
}

func parseMaintainerDecisionAnswer(body, keyword string) (string, string, bool) {
	lowerBody, lowerKeyword := strings.ToLower(body), strings.ToLower(keyword)
	index := strings.Index(lowerBody, lowerKeyword)
	if index < 0 {
		return "", "", false
	}
	remainder := strings.TrimSpace(body[index+len(keyword):])
	const prefix = "answer "
	if !strings.HasPrefix(strings.ToLower(remainder), prefix) {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimSpace(remainder[len(prefix):]), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	decisionID, answer := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	return decisionID, answer, decisionID != "" && answer != ""
}

func (r *GitHubRepositoryReconciler) resolveMaintainerDecisionFromGitHubComment(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, issueNumber int, decisionID, answer, subject string, commentID int64) error {
	if issueNumber < 1 || commentID < 1 {
		return nil
	}
	key := client.ObjectKey{Namespace: repository.Namespace, Name: MaintainerWorkItemName(repository.Name, int32(issueNumber))}
	err := r.retryMaintainerWorkItemStatusMutation(ctx, key, func(item *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
		if item.Status.PendingDecision == nil || item.Status.PendingDecision.ID != decisionID {
			return false, nil
		}
		now := metav1.Now()
		item.Status.PendingDecision = nil
		item.Status.ResolvedDecision = &triggersv1alpha1.MaintainerResolvedDecision{ID: decisionID, HumanSubject: subject, Answer: answer, ResolvedAt: now, ResolvedByCommand: corev1.LocalObjectReference{Name: fmt.Sprintf("github-comment-%d", commentID)}}
		item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseTriaged
		item.Status.ProjectionSequence++
		return true, nil
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (r *GitHubRepositoryReconciler) maintainerReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *GitHubRepositoryReconciler) reconcileMaintainerWorkItems(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, issues []*github.Issue, issueListComplete bool, githubClient GitHubTriageClient) error {
	items := &triggersv1alpha1.MaintainerWorkItemList{}
	if err := r.List(ctx, items, client.InNamespace(repository.Namespace), client.MatchingLabels{
		triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name,
	}); err != nil {
		return fmt.Errorf("listing maintainer work items: %w", err)
	}

	seen := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		if issue.IsPullRequest() {
			continue
		}
		issueNumber := int32(issue.GetNumber())
		if issueNumber < 1 {
			continue
		}
		name := MaintainerWorkItemName(repository.Name, issueNumber)
		seen[name] = struct{}{}
		if err := r.ensureMaintainerWorkItem(ctx, repository, name, issueNumber); err != nil {
			return err
		}
		if err := r.observeMaintainerWorkItem(ctx, client.ObjectKey{Namespace: repository.Namespace, Name: name}, issue, "Observed", "Issue observed in open issue list"); err != nil {
			return err
		}
	}

	if issueListComplete {
		for i := range items.Items {
			item := &items.Items[i]
			if _, ok := seen[item.Name]; ok {
				continue
			}
			// An issue absent from the open list is usually closed (for example
			// auto-closed by a merged pull request). Re-observe it directly so the
			// observation stays fresh and records the real state; otherwise
			// finalize/merge preconditions can never be satisfied again.
			if item.Status.IssueObservation != nil && item.Status.IssueObservation.State == triggersv1alpha1.MaintainerIssueStateClosed && maintainerWorkItemObservationIsFresh(item) {
				continue
			}
			if githubClient != nil {
				issue, _, err := githubClient.GetIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber))
				if err == nil && issue != nil {
					if err := r.observeMaintainerWorkItem(ctx, client.ObjectKeyFromObject(item), issue, "ObservedDirectly", "Issue observed directly after leaving the open issue list"); err != nil {
						return err
					}
					continue
				}
			}
			if err := r.markMaintainerWorkItemObservationStale(ctx, client.ObjectKeyFromObject(item)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *GitHubRepositoryReconciler) ensureMaintainerWorkItem(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, name string, issueNumber int32) error {
	item := &triggersv1alpha1.MaintainerWorkItem{}
	err := r.Get(ctx, types.NamespacedName{Namespace: repository.Namespace, Name: name}, item)
	if err == nil {
		if item.Spec.RepositoryRef.Name != repository.Name || item.Spec.IssueNumber != issueNumber {
			return fmt.Errorf("maintainer work item %s has a conflicting identity", name)
		}
		if metav1.IsControlledBy(item, repository) && item.Labels[triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey] == repository.Name && item.Labels[triggersv1alpha1.MaintainerWorkItemIssueNumberLabelKey] == strconv.FormatInt(int64(issueNumber), 10) {
			return nil
		}
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			fresh := &triggersv1alpha1.MaintainerWorkItem{}
			if err := r.Get(ctx, types.NamespacedName{Namespace: repository.Namespace, Name: name}, fresh); err != nil {
				return err
			}
			if fresh.Spec.RepositoryRef.Name != repository.Name || fresh.Spec.IssueNumber != issueNumber {
				return fmt.Errorf("maintainer work item %s has a conflicting identity", name)
			}
			if fresh.Labels == nil {
				fresh.Labels = map[string]string{}
			}
			fresh.Labels[triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey] = repository.Name
			fresh.Labels[triggersv1alpha1.MaintainerWorkItemIssueNumberLabelKey] = strconv.FormatInt(int64(issueNumber), 10)
			if err := ctrl.SetControllerReference(repository, fresh, r.Scheme); err != nil {
				return fmt.Errorf("setting owner reference on maintainer work item %s: %w", name, err)
			}
			return r.Update(ctx, fresh)
		})
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting maintainer work item %s: %w", name, err)
	}
	item = &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: repository.Namespace,
			Name:      name,
			Labels: map[string]string{
				triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey:  repository.Name,
				triggersv1alpha1.MaintainerWorkItemIssueNumberLabelKey: strconv.FormatInt(int64(issueNumber), 10),
			},
		},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{
			RepositoryRef: corev1.LocalObjectReference{Name: repository.Name},
			IssueNumber:   issueNumber,
		},
	}
	if err := ctrl.SetControllerReference(repository, item, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on maintainer work item %s: %w", name, err)
	}
	if err := r.Create(ctx, item); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating maintainer work item %s: %w", name, err)
	}
	return nil
}

func (r *GitHubRepositoryReconciler) observeMaintainerWorkItem(ctx context.Context, key client.ObjectKey, issue *github.Issue, reason, message string) error {
	now := metav1.Now()
	return retryMaintainerWorkItemStatusUpdate(ctx, r.Client, key, func(item *triggersv1alpha1.MaintainerWorkItem) bool {
		before := item.Status.DeepCopy()
		observation := maintainerIssueObservation(issue, now)
		if maintainerObservationEqual(item.Status.IssueObservation, &observation) {
			observation.ObservedAt = item.Status.IssueObservation.ObservedAt
			// Non-substantive GitHub activity (comments, cross-references) bumps
			// updatedAt without changing triage-relevant facts; keep the recorded
			// value so the projection sequence only advances on semantic change.
			observation.GitHubUpdatedAt = item.Status.IssueObservation.GitHubUpdatedAt
		}
		item.Status.IssueObservation = &observation
		item.Status.Phase = maintainerWorkItemPhase(item)
		setMaintainerWorkItemCondition(&item.Status.Conditions, metav1.Condition{
			Type:               triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: item.Generation,
			Reason:             reason,
			Message:            message,
		}, now)
		if maintainerWorkItemStatusSemanticallyEqual(before, &item.Status) {
			return false
		}
		item.Status.ProjectionSequence++
		return true
	})
}

func (r *GitHubRepositoryReconciler) markMaintainerWorkItemObservationStale(ctx context.Context, key client.ObjectKey) error {
	return r.markMaintainerWorkItemObservationNotFresh(ctx, key, "NotInOpenIssueList", "Issue was not present in the latest open issue list")
}

func (r *GitHubRepositoryReconciler) markMaintainerWorkItemObservationsUnavailable(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, reason, message string) error {
	items := &triggersv1alpha1.MaintainerWorkItemList{}
	if err := r.List(ctx, items, client.InNamespace(repository.Namespace), client.MatchingLabels{
		triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name,
	}); err != nil {
		return fmt.Errorf("listing maintainer work items to mark observations unavailable: %w", err)
	}
	for i := range items.Items {
		if items.Items[i].Spec.RepositoryRef.Name != repository.Name {
			continue
		}
		if err := r.markMaintainerWorkItemObservationNotFresh(ctx, client.ObjectKeyFromObject(&items.Items[i]), reason, message); err != nil {
			return err
		}
	}
	return nil
}

func (r *GitHubRepositoryReconciler) markMaintainerWorkItemObservationNotFresh(ctx context.Context, key client.ObjectKey, reason, message string) error {
	now := metav1.Now()
	return retryMaintainerWorkItemStatusUpdate(ctx, r.Client, key, func(item *triggersv1alpha1.MaintainerWorkItem) bool {
		before := item.Status.DeepCopy()
		setMaintainerWorkItemCondition(&item.Status.Conditions, metav1.Condition{
			Type:               triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: item.Generation,
			Reason:             reason,
			Message:            message,
		}, now)
		if maintainerWorkItemStatusSemanticallyEqual(before, &item.Status) {
			return false
		}
		item.Status.ProjectionSequence++
		return true
	})
}

func maintainerIssueObservation(issue *github.Issue, now metav1.Time) triggersv1alpha1.MaintainerIssueObservation {
	labels := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, label.GetName())
	}
	sort.Strings(labels)
	bodyHash := sha256.Sum256([]byte(issue.GetBody()))
	state := triggersv1alpha1.MaintainerIssueStateOpen
	if strings.EqualFold(issue.GetState(), string(triggersv1alpha1.MaintainerIssueStateClosed)) {
		state = triggersv1alpha1.MaintainerIssueStateClosed
	}
	return triggersv1alpha1.MaintainerIssueObservation{
		Number:          int32(issue.GetNumber()),
		URL:             issue.GetHTMLURL(),
		Title:           issue.GetTitle(),
		BodyHash:        hex.EncodeToString(bodyHash[:]),
		AuthorLogin:     issue.GetUser().GetLogin(),
		State:           state,
		Labels:          labels,
		GitHubUpdatedAt: metav1.NewTime(issue.GetUpdatedAt().UTC()),
		ObservedAt:      now,
	}
}

func maintainerObservationEqual(left *triggersv1alpha1.MaintainerIssueObservation, right *triggersv1alpha1.MaintainerIssueObservation) bool {
	if left == nil || right == nil {
		return left == right
	}
	leftCopy, rightCopy := *left, *right
	leftCopy.ObservedAt = metav1.Time{}
	rightCopy.ObservedAt = metav1.Time{}
	// updatedAt moves on every comment or timeline event; only substantive
	// fields (title, body hash, labels, state, author) drive change detection.
	leftCopy.GitHubUpdatedAt = metav1.Time{}
	rightCopy.GitHubUpdatedAt = metav1.Time{}
	return equality.Semantic.DeepEqual(leftCopy, rightCopy)
}

func maintainerWorkItemPhase(item *triggersv1alpha1.MaintainerWorkItem) triggersv1alpha1.MaintainerWorkItemPhase {
	if item.Status.Phase != "" && item.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhasePendingTriage && item.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseTriaged {
		return item.Status.Phase
	}
	if item.Spec.Disposition != "" {
		return triggersv1alpha1.MaintainerWorkItemPhaseTriaged
	}
	return triggersv1alpha1.MaintainerWorkItemPhasePendingTriage
}

func setMaintainerWorkItemCondition(conditions *[]metav1.Condition, desired metav1.Condition, now metav1.Time) {
	for i := range *conditions {
		current := &(*conditions)[i]
		if current.Type != desired.Type {
			continue
		}
		if current.Status == desired.Status && current.Reason == desired.Reason && current.Message == desired.Message {
			return
		}
		desired.LastTransitionTime = now
		(*conditions)[i] = desired
		return
	}
	desired.LastTransitionTime = now
	*conditions = append(*conditions, desired)
}

func maintainerWorkItemStatusSemanticallyEqual(left, right *triggersv1alpha1.MaintainerWorkItemStatus) bool {
	if left == nil || right == nil {
		return left == right
	}
	leftCopy, rightCopy := left.DeepCopy(), right.DeepCopy()
	normalize := func(status *triggersv1alpha1.MaintainerWorkItemStatus) {
		status.ProjectionSequence = 0
		if status.IssueObservation != nil {
			status.IssueObservation.ObservedAt = metav1.Time{}
			status.IssueObservation.GitHubUpdatedAt = metav1.Time{}
		}
		if status.Readiness != nil {
			status.Readiness.ObservedAt = nil
		}
		for i := range status.Children {
			status.Children[i].ObservedAt = nil
		}
		for i := range status.Dependencies {
			status.Dependencies[i].ObservedAt = nil
		}
		for i := range status.AgentRuns {
			status.AgentRuns[i].ObservedAt = nil
		}
		for i := range status.PullRequests {
			status.PullRequests[i].HeadObservedAt = nil
			status.PullRequests[i].ReviewObservedAt = nil
			status.PullRequests[i].ChecksObservedAt = nil
			status.PullRequests[i].StatusesObservedAt = nil
		}
		for i := range status.Conditions {
			status.Conditions[i].LastTransitionTime = metav1.Time{}
			// Free-form messages (for example error text with embedded rate-limit
			// reset times) must not advance the semantic projection when the
			// condition's type, status, and reason are unchanged.
			status.Conditions[i].Message = ""
		}
		sort.Slice(status.Conditions, func(i, j int) bool { return status.Conditions[i].Type < status.Conditions[j].Type })
	}
	normalize(leftCopy)
	normalize(rightCopy)
	return equality.Semantic.DeepEqual(leftCopy, rightCopy)
}

func (r *GitHubRepositoryReconciler) retryMaintainerWorkItemStatusMutation(ctx context.Context, key client.ObjectKey, mutate func(*triggersv1alpha1.MaintainerWorkItem) (bool, error)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		item := &triggersv1alpha1.MaintainerWorkItem{}
		if err := r.maintainerReader().Get(ctx, key, item); err != nil {
			return err
		}
		changed, err := mutate(item)
		if err != nil || !changed {
			return err
		}
		return r.Client.Status().Update(ctx, item)
	})
}

func retryMaintainerWorkItemStatusUpdate(ctx context.Context, c client.Client, key client.ObjectKey, mutate func(*triggersv1alpha1.MaintainerWorkItem) bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		item := &triggersv1alpha1.MaintainerWorkItem{}
		if err := c.Get(ctx, key, item); err != nil {
			return err
		}
		if !mutate(item) {
			return nil
		}
		return c.Status().Update(ctx, item)
	})
}

func retryMaintainerWorkItemCommandStatusUpdate(ctx context.Context, c client.Client, key client.ObjectKey, mutate func(*triggersv1alpha1.MaintainerWorkItemCommand)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		command := &triggersv1alpha1.MaintainerWorkItemCommand{}
		if err := c.Get(ctx, key, command); err != nil {
			return err
		}
		mutate(command)
		return c.Status().Update(ctx, command)
	})
}

func (r *GitHubRepositoryReconciler) reconcileMaintainerWorkItemCommands(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, githubClient GitHubTriageClient, deliveryClients ...maintainerGitHubDeliveryClient) error {
	var deliveryClient maintainerGitHubDeliveryClient
	if len(deliveryClients) > 0 {
		deliveryClient = deliveryClients[0]
	}
	commands := &triggersv1alpha1.MaintainerWorkItemCommandList{}
	if err := r.List(ctx, commands, client.InNamespace(repository.Namespace)); err != nil {
		return fmt.Errorf("listing maintainer work item commands: %w", err)
	}
	for i := range commands.Items {
		command := &commands.Items[i]
		if command.Spec.RepositoryRef.Name != repository.Name || maintainerCommandTerminal(command.Status.Phase) {
			continue
		}
		if command.Status.Phase == triggersv1alpha1.MaintainerWorkItemCommandPhaseFailed && maintainerCommandFailureCount(command) >= maintainerCommandFailureBudget {
			durable, err := r.maintainerCommandHasDurableSideEffects(ctx, repository, command)
			if err != nil {
				return err
			}
			if !durable {
				message := "retry budget exhausted after repeated failures"
				if command.Status.Result != nil && command.Status.Result.Message != "" {
					message += "; last failure: " + command.Status.Result.Message
				}
				if err := r.rejectMaintainerWorkItemCommand(ctx, repository, command, message); err != nil {
					return err
				}
				continue
			}
		}
		if err := r.processMaintainerWorkItemCommand(ctx, repository, command, githubClient, deliveryClient); err != nil {
			return err
		}
	}
	return nil
}

// maintainerCommandFailureBudget bounds how often a Failed command is
// reprocessed before it is terminally rejected, so persistently failing
// commands cannot burn GitHub quota on every reconcile forever.
const maintainerCommandFailureBudget = 5

func maintainerCommandFailureCount(command *triggersv1alpha1.MaintainerWorkItemCommand) int {
	count, err := strconv.Atoi(command.Annotations[triggersv1alpha1.MaintainerCommandFailureCountAnnotation])
	if err != nil || count < 0 {
		return 0
	}
	return count
}

// maintainerCommandHasDurableSideEffects reports whether a command already
// crossed a durable side-effect boundary (an attempted merge or an issued
// delivery attestation). Such commands must keep reconciling until GitHub
// reports a conclusive outcome; terminally rejecting them would orphan the
// side effect (for example a merge that later becomes visible would never be
// recorded in VerifiedMerges).
func (r *GitHubRepositoryReconciler) maintainerCommandHasDurableSideEffects(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand) (bool, error) {
	if command.Status.Result != nil && command.Status.Result.MergeAttemptedAt != nil {
		return true, nil
	}
	if command.Spec.Type != triggersv1alpha1.MaintainerWorkItemCommandTypeFinalizeWorkItem {
		return false, nil
	}
	item := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKey{Namespace: repository.Namespace, Name: command.Spec.Preconditions.WorkItemName}, item); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return item.Status.DeliveryAttestation != nil && item.Status.DeliveryAttestation.FinalizedByCommand.Name == command.Name && item.Status.DeliveryAttestation.CompletedAt == nil, nil
}

func (r *GitHubRepositoryReconciler) incrementMaintainerCommandFailureCount(ctx context.Context, command *triggersv1alpha1.MaintainerWorkItemCommand) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.MaintainerWorkItemCommand{}
		if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(command), fresh); err != nil {
			return client.IgnoreNotFound(err)
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[triggersv1alpha1.MaintainerCommandFailureCountAnnotation] = strconv.Itoa(maintainerCommandFailureCount(fresh) + 1)
		return r.Patch(ctx, fresh, patch)
	})
}

func maintainerCommandTerminal(phase triggersv1alpha1.MaintainerWorkItemCommandPhase) bool {
	return phase == triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || phase == triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected
}

type maintainerCommandRejectedError struct{ message string }

func (e maintainerCommandRejectedError) Error() string { return e.message }

func rejectMaintainerCommand(message string) error {
	return maintainerCommandRejectedError{message: message}
}

func (r *GitHubRepositoryReconciler) processMaintainerWorkItemCommand(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, githubClient GitHubTriageClient, deliveryClient maintainerGitHubDeliveryClient) error {
	pending := command.Status.Phase == "" || command.Status.Phase == triggersv1alpha1.MaintainerWorkItemCommandPhasePending
	item, err := r.validateMaintainerWorkItemCommand(ctx, repository, command, pending)
	if err != nil {
		var rejected maintainerCommandRejectedError
		if !asMaintainerCommandRejected(err, &rejected) {
			return err
		}
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, rejected.message)
	}
	if command.Spec.Type != triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue {
		return r.processMaintainerExecutionCommand(ctx, repository, command, item, githubClient, deliveryClient, pending)
	}

	if pending {
		item, err = r.applyMaintainerTriageIntent(ctx, repository, command)
		if err != nil {
			var rejected maintainerCommandRejectedError
			if asMaintainerCommandRejected(err, &rejected) {
				return r.rejectMaintainerWorkItemCommand(ctx, repository, command, rejected.message)
			}
			return err
		}
		if err := r.markMaintainerWorkItemTriaged(ctx, client.ObjectKeyFromObject(item), command.Name); err != nil {
			return err
		}
		if err := r.setMaintainerWorkItemCommandAccepted(ctx, command, item); err != nil {
			return err
		}
	} else {
		if err := r.markMaintainerWorkItemTriaged(ctx, client.ObjectKeyFromObject(item), command.Name); err != nil {
			return err
		}
		if err := r.setMaintainerWorkItemCommandAccepted(ctx, command, item); err != nil {
			return err
		}
	}

	if command.Spec.Triage.Disposition != triggersv1alpha1.MaintainerWorkItemDispositionNotActionable {
		return r.completeMaintainerWorkItemCommand(ctx, command, item, "triage intent recorded", "", observedIssueState(item))
	}
	if command.Spec.Triage.CloseReason == nil {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "NotActionable triage requires closeReason")
	}
	if githubClient == nil {
		return r.failMaintainerWorkItemCommand(ctx, command, item, "GitHub triage client is unavailable")
	}
	commentURL, state, err := r.applyNotActionableTriage(ctx, repository, item, command.Spec.Triage, githubClient)
	if err != nil {
		return r.failMaintainerWorkItemCommand(ctx, command, item, err.Error())
	}
	if err := r.markMaintainerWorkItemClosed(ctx, client.ObjectKeyFromObject(item)); err != nil {
		return err
	}
	return r.completeMaintainerWorkItemCommand(ctx, command, item, "issue triaged and closed", commentURL, state)
}

func asMaintainerCommandRejected(err error, target *maintainerCommandRejectedError) bool {
	if err == nil {
		return false
	}
	value, ok := err.(maintainerCommandRejectedError)
	if ok {
		*target = value
	}
	return ok
}

func (r *GitHubRepositoryReconciler) validateMaintainerWorkItemCommand(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, requirePreconditions bool) (*triggersv1alpha1.MaintainerWorkItem, error) {
	if command.Spec.RepositoryRef.Name != repository.Name || command.Spec.RepositoryRef.Name == "" {
		return nil, rejectMaintainerCommand("repositoryRef does not match GitHubRepository")
	}
	if command.Name != MaintainerWorkItemCommandName(repository.Name, command.Spec.IdempotencyKey) {
		return nil, rejectMaintainerCommand("command name does not match idempotency key")
	}
	if !metav1.IsControlledBy(command, repository) {
		return nil, rejectMaintainerCommand("command is not controller-owned by GitHubRepository")
	}
	issueNumber, err := validateMaintainerCommandPayload(command)
	if err != nil {
		return nil, err
	}
	expectedHash := triggersv1alpha1.MaintainerWorkItemCommandSpecPayloadHash(command.Spec)
	if command.Spec.Type == triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue {
		expectedHash = MaintainerWorkItemCommandPayloadHash(command.Spec.Type, command.Spec.Triage, command.Spec.Preconditions)
	}
	if command.Spec.PayloadHash != expectedHash {
		return nil, rejectMaintainerCommand("payloadHash does not match command payload")
	}
	if err := r.authorizeMaintainerCommand(ctx, repository, command); err != nil {
		return nil, err
	}
	name := MaintainerWorkItemName(repository.Name, issueNumber)
	if command.Spec.Preconditions.WorkItemName != name {
		return nil, rejectMaintainerCommand("precondition workItemName does not match command issue")
	}
	item := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKey{Namespace: repository.Namespace, Name: name}, item); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, rejectMaintainerCommand("work item does not exist")
		}
		return nil, fmt.Errorf("getting maintainer work item: %w", err)
	}
	if item.Spec.RepositoryRef.Name != repository.Name || item.Spec.IssueNumber != issueNumber {
		return nil, rejectMaintainerCommand("work item does not match command issue")
	}
	if command.Spec.Preconditions.WorkItemUID == "" || item.UID != command.Spec.Preconditions.WorkItemUID {
		return nil, rejectMaintainerCommand("target work-item UID does not match command preconditions")
	}
	alreadyApplied := maintainerCommandAlreadyApplied(item, command)
	if !requirePreconditions && command.Spec.Type == triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue && !alreadyApplied {
		return nil, rejectMaintainerCommand("command was superseded by newer triage intent; " + currentProjectionMessage(item))
	}
	if requirePreconditions && !alreadyApplied && !maintainerWorkItemObservationIsFresh(item) {
		return nil, rejectMaintainerCommand("work item issue observation is not fresh; " + currentProjectionMessage(item))
	}
	if requirePreconditions && !alreadyApplied && item.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence {
		return nil, rejectMaintainerCommand(currentProjectionMessage(item))
	}
	return item, nil
}

func validateMaintainerCommandPayload(command *triggersv1alpha1.MaintainerWorkItemCommand) (int32, error) {
	if command == nil {
		return 0, rejectMaintainerCommand("command is nil")
	}
	switch command.Spec.Type {
	case triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue:
		if command.Spec.Triage == nil || command.Spec.Triage.IssueNumber < 1 || strings.TrimSpace(command.Spec.Triage.EvidenceSummary) == "" {
			return 0, rejectMaintainerCommand("incomplete triage payload")
		}
		return command.Spec.Triage.IssueNumber, nil
	case triggersv1alpha1.MaintainerWorkItemCommandTypeBreakdownIssue:
		if command.Spec.Breakdown == nil || command.Spec.Breakdown.IssueNumber < 1 || len(command.Spec.Breakdown.Children) == 0 {
			return 0, rejectMaintainerCommand("incomplete breakdown payload")
		}
		return command.Spec.Breakdown.IssueNumber, nil
	case triggersv1alpha1.MaintainerWorkItemCommandTypeRequestDecision:
		if command.Spec.RequestDecision == nil || command.Spec.RequestDecision.IssueNumber < 1 || strings.TrimSpace(command.Spec.RequestDecision.DecisionID) == "" || strings.TrimSpace(command.Spec.RequestDecision.Question) == "" {
			return 0, rejectMaintainerCommand("incomplete requestDecision payload")
		}
		return command.Spec.RequestDecision.IssueNumber, nil
	case triggersv1alpha1.MaintainerWorkItemCommandTypeResolveDecision:
		return 0, rejectMaintainerCommand("resolveDecision commands from AgentRuns are not authorized; answer with an authenticated GitHub issue comment")
	case triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem:
		if command.Spec.Dispatch == nil || command.Spec.Dispatch.IssueNumber < 1 || strings.TrimSpace(command.Spec.Dispatch.Mode) == "" {
			return 0, rejectMaintainerCommand("incomplete dispatch payload")
		}
		return command.Spec.Dispatch.IssueNumber, nil
	case triggersv1alpha1.MaintainerWorkItemCommandTypeRequestMerge:
		merge := command.Spec.RequestMerge
		if merge == nil || merge.IssueNumber < 1 || merge.PullRequestNumber < 1 || strings.TrimSpace(merge.Repository) == "" || len(merge.ExpectedHeadSHA) != 40 || (merge.MergeMethod != triggersv1alpha1.MaintainerWorkItemMergeMethodSquash && merge.MergeMethod != triggersv1alpha1.MaintainerWorkItemMergeMethodMerge && merge.MergeMethod != triggersv1alpha1.MaintainerWorkItemMergeMethodRebase) {
			return 0, rejectMaintainerCommand("incomplete requestMerge payload")
		}
		return merge.IssueNumber, nil
	case triggersv1alpha1.MaintainerWorkItemCommandTypeFinalizeWorkItem:
		finalize := command.Spec.Finalize
		if finalize == nil || finalize.IssueNumber < 1 || len(finalize.AcceptedScopeHash) != 64 || strings.TrimSpace(finalize.DeliverySummary) == "" || strings.TrimSpace(finalize.DeliveryEvidence) == "" {
			return 0, rejectMaintainerCommand("incomplete finalize payload")
		}
		return finalize.IssueNumber, nil
	default:
		return 0, rejectMaintainerCommand("unsupported command type")
	}
}

func (r *GitHubRepositoryReconciler) authorizeMaintainerCommand(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand) error {
	issuer := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: repository.Namespace, Name: command.Spec.Issuer.RunName}, issuer); err != nil {
		if apierrors.IsNotFound(err) {
			return rejectMaintainerCommand("issuer AgentRun does not exist")
		}
		return fmt.Errorf("getting issuer AgentRun: %w", err)
	}
	if issuer.UID != command.Spec.Issuer.UID {
		return rejectMaintainerCommand("issuer UID does not match AgentRun")
	}
	if issuer.Labels[orchestration.StandingRunRoleLabel] != orchestration.StandingRunRoleMaintainer {
		return rejectMaintainerCommand("issuer is not a standing maintainer")
	}
	if issuer.Labels[orchestration.SupervisedRunLabel] != repository.Name {
		return rejectMaintainerCommand("issuer is not assigned to this repository")
	}
	ownedByRepository := false
	for _, owner := range issuer.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == gitHubRepositoryTriggerKind && owner.Name == repository.Name && owner.UID == repository.UID {
			ownedByRepository = true
			break
		}
	}
	if !ownedByRepository {
		return rejectMaintainerCommand("issuer is not controller-owned by this GitHubRepository")
	}
	capability := &corev1.Secret{}
	capabilityName := triggersv1alpha1.MaintainerCommandCapabilitySecretName(issuer.Name)
	if err := r.Get(ctx, client.ObjectKey{Namespace: issuer.Namespace, Name: capabilityName}, capability); err != nil {
		if apierrors.IsNotFound(err) {
			return rejectMaintainerCommand("issuer command capability does not exist")
		}
		return fmt.Errorf("getting issuer command capability: %w", err)
	}
	if !metav1.IsControlledBy(capability, issuer) {
		return rejectMaintainerCommand("issuer command capability is not owned by issuer AgentRun")
	}
	if string(capability.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryNameKey]) != repository.Name || string(capability.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryUIDKey]) != string(repository.UID) {
		return rejectMaintainerCommand("issuer command capability is bound to a different GitHubRepository")
	}
	key := capability.Data[triggersv1alpha1.MaintainerCommandCapabilitySecretKey]
	if len(key) < 32 {
		return rejectMaintainerCommand("issuer command capability is invalid")
	}
	expectedProof := triggersv1alpha1.MaintainerWorkItemCommandProof(key, repository.Name, repository.UID, command.Spec.IdempotencyKey, command.Spec.PayloadHash, issuer.Name, issuer.UID)
	if !hmac.Equal([]byte(expectedProof), []byte(command.Spec.Issuer.Proof)) {
		return rejectMaintainerCommand("issuer command proof is invalid")
	}
	return nil
}

func (r *GitHubRepositoryReconciler) applyMaintainerTriageIntent(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand) (*triggersv1alpha1.MaintainerWorkItem, error) {
	key := client.ObjectKey{Namespace: repository.Namespace, Name: command.Spec.Preconditions.WorkItemName}
	item := &triggersv1alpha1.MaintainerWorkItem{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.MaintainerWorkItem{}
		if err := r.Get(ctx, key, fresh); err != nil {
			if apierrors.IsNotFound(err) {
				return rejectMaintainerCommand("work item does not exist")
			}
			return err
		}
		if fresh.Spec.RepositoryRef.Name != repository.Name || fresh.Spec.IssueNumber != command.Spec.Triage.IssueNumber {
			return rejectMaintainerCommand("work item does not match command issue")
		}
		if maintainerTriageAlreadyApplied(fresh, command) {
			item = fresh
			return nil
		}
		if fresh.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence {
			return rejectMaintainerCommand(currentProjectionMessage(fresh))
		}
		fresh.Spec.Disposition = command.Spec.Triage.Disposition
		fresh.Spec.EvidenceSummary = command.Spec.Triage.EvidenceSummary
		fresh.Spec.AcceptedScope = command.Spec.Triage.AcceptedScope.DeepCopy()
		fresh.Spec.CloseReason = command.Spec.Triage.CloseReason
		fresh.Spec.TriagedByCommand = &corev1.LocalObjectReference{Name: command.Name}
		if err := r.Update(ctx, fresh); err != nil {
			return err
		}
		item = fresh
		return nil
	})
	return item, err
}

func maintainerTriageAlreadyApplied(item *triggersv1alpha1.MaintainerWorkItem, command *triggersv1alpha1.MaintainerWorkItemCommand) bool {
	return item.Spec.TriagedByCommand != nil && item.Spec.TriagedByCommand.Name == command.Name && item.Spec.Disposition == command.Spec.Triage.Disposition && item.Spec.EvidenceSummary == command.Spec.Triage.EvidenceSummary && equality.Semantic.DeepEqual(item.Spec.AcceptedScope, &command.Spec.Triage.AcceptedScope) && equality.Semantic.DeepEqual(item.Spec.CloseReason, command.Spec.Triage.CloseReason)
}

func maintainerCommandAlreadyApplied(item *triggersv1alpha1.MaintainerWorkItem, command *triggersv1alpha1.MaintainerWorkItemCommand) bool {
	if item == nil || command == nil {
		return false
	}
	switch command.Spec.Type {
	case triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue:
		return command.Spec.Triage != nil && maintainerTriageAlreadyApplied(item, command)
	case triggersv1alpha1.MaintainerWorkItemCommandTypeBreakdownIssue:
		return command.Spec.Breakdown != nil && equality.Semantic.DeepEqual(item.Spec.Children, command.Spec.Breakdown.Children) && equality.Semantic.DeepEqual(item.Spec.Dependencies, command.Spec.Breakdown.Dependencies)
	case triggersv1alpha1.MaintainerWorkItemCommandTypeRequestDecision:
		return item.Status.PendingDecision != nil && item.Status.PendingDecision.RequestedByCommand != nil && item.Status.PendingDecision.RequestedByCommand.Name == command.Name
	case triggersv1alpha1.MaintainerWorkItemCommandTypeResolveDecision:
		return item.Status.ResolvedDecision != nil && item.Status.ResolvedDecision.ResolvedByCommand.Name == command.Name
	case triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem:
		return item.Status.DispatchReservation != nil && item.Status.DispatchReservation.CommandRef.Name == command.Name
	case triggersv1alpha1.MaintainerWorkItemCommandTypeRequestMerge:
		return command.Spec.RequestMerge != nil && verifiedMaintainerMerge(item, command.Spec.RequestMerge.Repository, command.Spec.RequestMerge.PullRequestNumber) != nil
	case triggersv1alpha1.MaintainerWorkItemCommandTypeFinalizeWorkItem:
		return item.Status.DeliveryAttestation != nil && item.Status.DeliveryAttestation.FinalizedByCommand.Name == command.Name
	default:
		return false
	}
}

func (r *GitHubRepositoryReconciler) markMaintainerWorkItemTriaged(ctx context.Context, key client.ObjectKey, commandName string) error {
	now := metav1.Now()
	return retryMaintainerWorkItemStatusUpdate(ctx, r.Client, key, func(item *triggersv1alpha1.MaintainerWorkItem) bool {
		before := item.Status.DeepCopy()
		item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseTriaged
		setMaintainerWorkItemCondition(&item.Status.Conditions, metav1.Condition{
			Type:               triggersv1alpha1.ConditionMaintainerWorkItemCommandAccepted,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: item.Generation,
			Reason:             "Accepted",
			Message:            "Triage accepted from command " + commandName,
		}, now)
		if maintainerWorkItemStatusSemanticallyEqual(before, &item.Status) {
			return false
		}
		item.Status.ProjectionSequence++
		return true
	})
}

func (r *GitHubRepositoryReconciler) markMaintainerWorkItemClosed(ctx context.Context, key client.ObjectKey) error {
	now := metav1.Now()
	return retryMaintainerWorkItemStatusUpdate(ctx, r.Client, key, func(item *triggersv1alpha1.MaintainerWorkItem) bool {
		before := item.Status.DeepCopy()
		if item.Status.IssueObservation == nil {
			item.Status.IssueObservation = &triggersv1alpha1.MaintainerIssueObservation{Number: item.Spec.IssueNumber}
		}
		item.Status.IssueObservation.State = triggersv1alpha1.MaintainerIssueStateClosed
		item.Status.IssueObservation.ObservedAt = now
		if maintainerWorkItemStatusSemanticallyEqual(before, &item.Status) {
			return false
		}
		item.Status.ProjectionSequence++
		return true
	})
}

func (r *GitHubRepositoryReconciler) setMaintainerWorkItemCommandAccepted(ctx context.Context, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem) error {
	message := string(command.Spec.Type) + " command accepted; side effects are not yet verified"
	err := retryMaintainerWorkItemCommandStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(command), func(fresh *triggersv1alpha1.MaintainerWorkItemCommand) {
		fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemCommandPhaseAccepted
		fresh.Status.ObservedGeneration = fresh.Generation
		fresh.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{
			WorkItemRef: corev1.LocalObjectReference{Name: item.Name},
			Applied:     false,
			Message:     message,
			IssueState:  observedIssueState(item),
		}
	})
	return err
}

func (r *GitHubRepositoryReconciler) completeMaintainerWorkItemCommand(ctx context.Context, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, message, commentURL string, state triggersv1alpha1.MaintainerIssueState) error {
	now := metav1.Now()
	current := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(item), current); err != nil {
		return err
	}
	err := retryMaintainerWorkItemCommandStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(command), func(fresh *triggersv1alpha1.MaintainerWorkItemCommand) {
		fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded
		fresh.Status.ObservedGeneration = fresh.Generation
		var mergeAttemptedAt *metav1.Time
		if fresh.Status.Result != nil {
			mergeAttemptedAt = fresh.Status.Result.MergeAttemptedAt
		}
		result := &triggersv1alpha1.MaintainerWorkItemCommandResult{WorkItemRef: corev1.LocalObjectReference{Name: item.Name}, Applied: true, Message: message, CommentURL: commentURL, IssueState: state, MergeAttemptedAt: mergeAttemptedAt, CompletedAt: &now}
		if command.Spec.RequestMerge != nil {
			for i := range current.Status.VerifiedMerges {
				verified := &current.Status.VerifiedMerges[i]
				if verified.Repository == command.Spec.RequestMerge.Repository && verified.PullRequestNumber == command.Spec.RequestMerge.PullRequestNumber {
					result.VerifiedMerge = verified.DeepCopy()
				}
			}
		}
		result.DeliveryAttestation = current.Status.DeliveryAttestation.DeepCopy()
		fresh.Status.Result = result
	})
	if err != nil {
		return err
	}
	return r.projectMaintainerCommandObservation(ctx, item, command, triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded, true, message)
}

func (r *GitHubRepositoryReconciler) failMaintainerWorkItemCommand(ctx context.Context, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, message string) error {
	if err := r.incrementMaintainerCommandFailureCount(ctx, command); err != nil {
		return err
	}
	err := retryMaintainerWorkItemCommandStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(command), func(fresh *triggersv1alpha1.MaintainerWorkItemCommand) {
		fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemCommandPhaseFailed
		fresh.Status.ObservedGeneration = fresh.Generation
		var mergeAttemptedAt *metav1.Time
		if fresh.Status.Result != nil {
			mergeAttemptedAt = fresh.Status.Result.MergeAttemptedAt
		}
		fresh.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{WorkItemRef: corev1.LocalObjectReference{Name: item.Name}, Applied: false, Message: message, IssueState: observedIssueState(item), MergeAttemptedAt: mergeAttemptedAt}
	})
	if err != nil {
		return err
	}
	return r.projectMaintainerCommandObservation(ctx, item, command, triggersv1alpha1.MaintainerWorkItemCommandPhaseFailed, false, message)
}

func (r *GitHubRepositoryReconciler) rejectMaintainerWorkItemCommand(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, message string) error {
	message = currentMaintainerProjectionMessage(ctx, r.Client, repository, command, message)
	err := retryMaintainerWorkItemCommandStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(command), func(fresh *triggersv1alpha1.MaintainerWorkItemCommand) {
		fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected
		fresh.Status.ObservedGeneration = fresh.Generation
		fresh.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{WorkItemRef: corev1.LocalObjectReference{Name: fresh.Spec.Preconditions.WorkItemName}, Applied: false, Message: message}
	})
	if err != nil {
		return err
	}
	item := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKey{Namespace: repository.Namespace, Name: command.Spec.Preconditions.WorkItemName}, item); err != nil {
		return client.IgnoreNotFound(err)
	}
	return r.projectMaintainerCommandObservation(ctx, item, command, triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected, false, message)
}

func (r *GitHubRepositoryReconciler) projectMaintainerCommandObservation(ctx context.Context, item *triggersv1alpha1.MaintainerWorkItem, command *triggersv1alpha1.MaintainerWorkItemCommand, phase triggersv1alpha1.MaintainerWorkItemCommandPhase, applied bool, message string) error {
	if item == nil || command == nil {
		return nil
	}
	return r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
		if fresh.UID != command.Spec.Preconditions.WorkItemUID {
			// Never project an old command receipt into a replacement object.
			return false, nil
		}
		now := metav1.Now()
		desired := &triggersv1alpha1.MaintainerWorkItemCommandObservation{Name: command.Name, Type: command.Spec.Type, Phase: phase, Applied: applied, Message: message, ObservedAt: now}
		if fresh.Status.LatestCommand != nil && fresh.Status.LatestCommand.Name == desired.Name && fresh.Status.LatestCommand.Phase == desired.Phase && fresh.Status.LatestCommand.Applied == desired.Applied && fresh.Status.LatestCommand.Message == desired.Message {
			return false, nil
		}
		fresh.Status.LatestCommand = desired
		fresh.Status.ProjectionSequence++
		return true, nil
	})
}

func currentMaintainerProjectionMessage(ctx context.Context, c client.Client, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, prefix string) string {
	name := command.Spec.Preconditions.WorkItemName
	if command.Spec.Triage != nil && command.Spec.Triage.IssueNumber > 0 {
		name = MaintainerWorkItemName(repository.Name, command.Spec.Triage.IssueNumber)
	}
	item := &triggersv1alpha1.MaintainerWorkItem{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: repository.Namespace, Name: name}, item); err != nil {
		return prefix + "; current projection unavailable"
	}
	return prefix + "; " + currentProjectionMessage(item)
}

func currentProjectionMessage(item *triggersv1alpha1.MaintainerWorkItem) string {
	var message strings.Builder
	fmt.Fprintf(&message, "current projection sequence %d resourceVersion %s", item.Status.ProjectionSequence, item.ResourceVersion)
	for _, condition := range item.Status.Conditions {
		if condition.Type == triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh && condition.Status != metav1.ConditionTrue {
			fmt.Fprintf(&message, "; observation not fresh (%s)", condition.Reason)
			if item.Status.IssueObservation != nil {
				fmt.Fprintf(&message, "; last observation state %s at %s", item.Status.IssueObservation.State, item.Status.IssueObservation.ObservedAt.UTC().Format(time.RFC3339))
			}
		}
	}
	return message.String()
}

func maintainerWorkItemObservationIsFresh(item *triggersv1alpha1.MaintainerWorkItem) bool {
	if item == nil || item.Status.IssueObservation == nil || item.Status.IssueObservation.Number != item.Spec.IssueNumber {
		return false
	}
	for _, condition := range item.Status.Conditions {
		if condition.Type == triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh {
			return condition.Status == metav1.ConditionTrue
		}
	}
	return false
}

func observedIssueState(item *triggersv1alpha1.MaintainerWorkItem) triggersv1alpha1.MaintainerIssueState {
	if item != nil && item.Status.IssueObservation != nil {
		return item.Status.IssueObservation.State
	}
	return triggersv1alpha1.MaintainerIssueStateOpen
}

func (r *GitHubRepositoryReconciler) applyNotActionableTriage(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, item *triggersv1alpha1.MaintainerWorkItem, triage *triggersv1alpha1.MaintainerTriageCommand, githubClient GitHubTriageClient) (string, triggersv1alpha1.MaintainerIssueState, error) {
	body := maintainerDecisionBody(repository, item, triage)
	marker := maintainerDecisionMarkerFor(repository, item)
	comment, err := findMaintainerDecisionComment(ctx, githubClient, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber), marker)
	if err != nil {
		return "", "", err
	}
	if comment == nil {
		comment, _, err = githubClient.CreateIssueComment(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber), &github.IssueComment{Body: new(body)})
		if err != nil {
			return "", "", fmt.Errorf("creating triage comment: %w", err)
		}
	} else if comment.GetBody() != body {
		comment, _, err = githubClient.EditIssueComment(ctx, repository.Spec.Owner, repository.Spec.Repo, comment.GetID(), &github.IssueComment{Body: new(body)})
		if err != nil {
			return "", "", fmt.Errorf("editing triage comment: %w", err)
		}
	}
	issue, _, err := githubClient.GetIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber))
	if err != nil {
		return "", "", fmt.Errorf("getting issue for triage close: %w", err)
	}
	closedState := string(triggersv1alpha1.MaintainerIssueStateClosed)
	closeReason := string(*triage.CloseReason)
	if issue.GetState() != closedState || issue.GetStateReason() != closeReason {
		issue, _, err = githubClient.EditIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber), &github.IssueRequest{State: new(closedState), StateReason: new(closeReason)})
		if err != nil {
			return "", "", fmt.Errorf("closing triaged issue: %w", err)
		}
	}
	state := triggersv1alpha1.MaintainerIssueState(issue.GetState())
	if state == "" {
		state = triggersv1alpha1.MaintainerIssueStateClosed
	}
	return comment.GetHTMLURL(), state, nil
}

func findMaintainerDecisionComment(ctx context.Context, githubClient GitHubTriageClient, owner, repo string, issueNumber int, marker string) (*github.IssueComment, error) {
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		comments, response, err := githubClient.ListIssueComments(ctx, owner, repo, issueNumber, opts)
		if err != nil {
			return nil, fmt.Errorf("listing triage comments: %w", err)
		}
		for _, comment := range comments {
			if strings.Contains(comment.GetBody(), marker) {
				return comment, nil
			}
		}
		if response == nil || response.NextPage == 0 {
			return nil, nil
		}
		opts.Page = response.NextPage
	}
}

func maintainerDecisionMarkerFor(repository *triggersv1alpha1.GitHubRepository, item *triggersv1alpha1.MaintainerWorkItem) string {
	sum := sha256.Sum256([]byte(string(repository.UID) + "\x00" + item.Namespace + "/" + item.Name))
	return "<!-- " + maintainerDecisionMarker + ":" + hex.EncodeToString(sum[:16]) + " -->"
}

func maintainerDecisionBody(repository *triggersv1alpha1.GitHubRepository, item *triggersv1alpha1.MaintainerWorkItem, triage *triggersv1alpha1.MaintainerTriageCommand) string {
	var b strings.Builder
	b.WriteString("Maintainer triage decision: Not actionable.\n\nEvidence: ")
	b.WriteString(triage.EvidenceSummary)
	if statement := strings.TrimSpace(triage.AcceptedScope.Statement); statement != "" {
		b.WriteString("\n\nScope: ")
		b.WriteString(statement)
	}
	if len(triage.AcceptedScope.AcceptanceCriteria) > 0 {
		b.WriteString("\n\nAcceptance criteria:")
		for _, criterion := range triage.AcceptedScope.AcceptanceCriteria {
			b.WriteString("\n- ")
			b.WriteString(criterion)
		}
	}
	b.WriteString("\n\nClose reason: ")
	b.WriteString(string(*triage.CloseReason))
	b.WriteString("\n\n")
	b.WriteString(maintainerDecisionMarkerFor(repository, item))
	return b.String()
}
