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

func (r *GitHubRepositoryReconciler) reconcileMaintainerWorkItems(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, issues []*github.Issue) error {
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
		if err := r.observeMaintainerWorkItem(ctx, client.ObjectKey{Namespace: repository.Namespace, Name: name}, issue); err != nil {
			return err
		}
	}

	for i := range items.Items {
		if _, ok := seen[items.Items[i].Name]; ok {
			continue
		}
		if err := r.markMaintainerWorkItemObservationStale(ctx, client.ObjectKeyFromObject(&items.Items[i])); err != nil {
			return err
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

func (r *GitHubRepositoryReconciler) observeMaintainerWorkItem(ctx context.Context, key client.ObjectKey, issue *github.Issue) error {
	now := metav1.Now()
	return retryMaintainerWorkItemStatusUpdate(ctx, r.Client, key, func(item *triggersv1alpha1.MaintainerWorkItem) bool {
		before := item.Status.DeepCopy()
		observation := maintainerIssueObservation(issue, now)
		if maintainerObservationEqual(item.Status.IssueObservation, &observation) {
			observation.ObservedAt = item.Status.IssueObservation.ObservedAt
		}
		item.Status.IssueObservation = &observation
		item.Status.Phase = maintainerWorkItemPhase(item)
		setMaintainerWorkItemCondition(&item.Status.Conditions, metav1.Condition{
			Type:               triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: item.Generation,
			Reason:             "Observed",
			Message:            "Issue observed in open issue list",
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
	return triggersv1alpha1.MaintainerIssueObservation{
		Number:          int32(issue.GetNumber()),
		URL:             issue.GetHTMLURL(),
		Title:           issue.GetTitle(),
		BodyHash:        hex.EncodeToString(bodyHash[:]),
		AuthorLogin:     issue.GetUser().GetLogin(),
		State:           triggersv1alpha1.MaintainerIssueStateOpen,
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
	return equality.Semantic.DeepEqual(leftCopy, rightCopy)
}

func maintainerWorkItemPhase(item *triggersv1alpha1.MaintainerWorkItem) triggersv1alpha1.MaintainerWorkItemPhase {
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
	if left.Phase != right.Phase || !maintainerObservationEqual(left.IssueObservation, right.IssueObservation) {
		return false
	}
	if len(left.Conditions) != len(right.Conditions) {
		return false
	}
	leftConditions := append([]metav1.Condition(nil), left.Conditions...)
	rightConditions := append([]metav1.Condition(nil), right.Conditions...)
	sort.Slice(leftConditions, func(i, j int) bool { return leftConditions[i].Type < leftConditions[j].Type })
	sort.Slice(rightConditions, func(i, j int) bool { return rightConditions[i].Type < rightConditions[j].Type })
	for i := range leftConditions {
		if leftConditions[i].Type != rightConditions[i].Type || leftConditions[i].Status != rightConditions[i].Status || leftConditions[i].Reason != rightConditions[i].Reason || leftConditions[i].Message != rightConditions[i].Message {
			return false
		}
	}
	return true
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

func (r *GitHubRepositoryReconciler) reconcileMaintainerWorkItemCommands(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, githubClient GitHubTriageClient) error {
	commands := &triggersv1alpha1.MaintainerWorkItemCommandList{}
	if err := r.List(ctx, commands, client.InNamespace(repository.Namespace)); err != nil {
		return fmt.Errorf("listing maintainer work item commands: %w", err)
	}
	for i := range commands.Items {
		command := &commands.Items[i]
		if command.Spec.RepositoryRef.Name != repository.Name || maintainerCommandTerminal(command.Status.Phase) {
			continue
		}
		if err := r.processMaintainerWorkItemCommand(ctx, repository, command, githubClient); err != nil {
			return err
		}
	}
	return nil
}

func maintainerCommandTerminal(phase triggersv1alpha1.MaintainerWorkItemCommandPhase) bool {
	return phase == triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || phase == triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected
}

type maintainerCommandRejectedError struct{ message string }

func (e maintainerCommandRejectedError) Error() string { return e.message }

func rejectMaintainerCommand(message string) error {
	return maintainerCommandRejectedError{message: message}
}

func (r *GitHubRepositoryReconciler) processMaintainerWorkItemCommand(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, githubClient GitHubTriageClient) error {
	pending := command.Status.Phase == "" || command.Status.Phase == triggersv1alpha1.MaintainerWorkItemCommandPhasePending
	item, err := r.validateMaintainerWorkItemCommand(ctx, repository, command, pending)
	if err != nil {
		var rejected maintainerCommandRejectedError
		if !asMaintainerCommandRejected(err, &rejected) {
			return err
		}
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, rejected.message)
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
	if command.Spec.Type != triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue || command.Spec.Triage == nil {
		return nil, rejectMaintainerCommand("unsupported or incomplete command payload")
	}
	if command.Spec.PayloadHash != MaintainerWorkItemCommandPayloadHash(command.Spec.Type, command.Spec.Triage, command.Spec.Preconditions) {
		return nil, rejectMaintainerCommand("payloadHash does not match command payload")
	}
	if command.Spec.Triage.IssueNumber < 1 {
		return nil, rejectMaintainerCommand("triage issueNumber must be positive")
	}
	if strings.TrimSpace(command.Spec.Triage.EvidenceSummary) == "" {
		return nil, rejectMaintainerCommand("triage evidenceSummary is required")
	}
	switch command.Spec.Triage.Disposition {
	case triggersv1alpha1.MaintainerWorkItemDispositionNotActionable:
		if command.Spec.Triage.CloseReason == nil {
			return nil, rejectMaintainerCommand("NotActionable triage requires closeReason")
		}
	case triggersv1alpha1.MaintainerWorkItemDispositionBounded,
		triggersv1alpha1.MaintainerWorkItemDispositionDecomposable,
		triggersv1alpha1.MaintainerWorkItemDispositionDiscovery,
		triggersv1alpha1.MaintainerWorkItemDispositionEscalated:
		if command.Spec.Triage.CloseReason != nil {
			return nil, rejectMaintainerCommand("closeReason is only valid for NotActionable triage")
		}
	default:
		return nil, rejectMaintainerCommand("unsupported triage disposition")
	}
	if err := r.authorizeMaintainerCommand(ctx, repository, command); err != nil {
		return nil, err
	}

	name := MaintainerWorkItemName(repository.Name, command.Spec.Triage.IssueNumber)
	if command.Spec.Preconditions.WorkItemName != name {
		return nil, rejectMaintainerCommand("precondition workItemName does not match triage issue")
	}
	item := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: repository.Namespace, Name: name}, item); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, rejectMaintainerCommand("work item does not exist")
		}
		return nil, fmt.Errorf("getting maintainer work item: %w", err)
	}
	if item.Spec.RepositoryRef.Name != repository.Name || item.Spec.IssueNumber != command.Spec.Triage.IssueNumber {
		return nil, rejectMaintainerCommand("work item does not match command issue")
	}
	if !requirePreconditions && !maintainerTriageAlreadyApplied(item, command) {
		return nil, rejectMaintainerCommand("command was superseded by newer triage intent; " + currentProjectionMessage(item))
	}
	if requirePreconditions && !maintainerWorkItemObservationIsFresh(item) {
		return nil, rejectMaintainerCommand("work item issue observation is not fresh; " + currentProjectionMessage(item))
	}
	if requirePreconditions && !maintainerTriageAlreadyApplied(item, command) && (item.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence || item.ResourceVersion != command.Spec.Preconditions.ResourceVersion) {
		return nil, rejectMaintainerCommand(currentProjectionMessage(item))
	}
	return item, nil
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
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == "GitHubRepository" && owner.Name == repository.Name && owner.UID == repository.UID {
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
		if fresh.Status.ProjectionSequence != command.Spec.Preconditions.ProjectionSequence || fresh.ResourceVersion != command.Spec.Preconditions.ResourceVersion {
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
	return retryMaintainerWorkItemCommandStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(command), func(fresh *triggersv1alpha1.MaintainerWorkItemCommand) {
		fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemCommandPhaseAccepted
		fresh.Status.ObservedGeneration = fresh.Generation
		fresh.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{
			WorkItemRef: corev1.LocalObjectReference{Name: item.Name},
			Applied:     true,
			Message:     "triage intent accepted",
			IssueState:  observedIssueState(item),
		}
	})
}

func (r *GitHubRepositoryReconciler) completeMaintainerWorkItemCommand(ctx context.Context, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, message, commentURL string, state triggersv1alpha1.MaintainerIssueState) error {
	now := metav1.Now()
	return retryMaintainerWorkItemCommandStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(command), func(fresh *triggersv1alpha1.MaintainerWorkItemCommand) {
		fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded
		fresh.Status.ObservedGeneration = fresh.Generation
		fresh.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{WorkItemRef: corev1.LocalObjectReference{Name: item.Name}, Applied: true, Message: message, CommentURL: commentURL, IssueState: state, CompletedAt: &now}
	})
}

func (r *GitHubRepositoryReconciler) failMaintainerWorkItemCommand(ctx context.Context, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, message string) error {
	return retryMaintainerWorkItemCommandStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(command), func(fresh *triggersv1alpha1.MaintainerWorkItemCommand) {
		fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemCommandPhaseFailed
		fresh.Status.ObservedGeneration = fresh.Generation
		fresh.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{WorkItemRef: corev1.LocalObjectReference{Name: item.Name}, Applied: true, Message: message, IssueState: observedIssueState(item)}
	})
}

func (r *GitHubRepositoryReconciler) rejectMaintainerWorkItemCommand(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, message string) error {
	message = currentMaintainerProjectionMessage(ctx, r.Client, repository, command, message)
	return retryMaintainerWorkItemCommandStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(command), func(fresh *triggersv1alpha1.MaintainerWorkItemCommand) {
		fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected
		fresh.Status.ObservedGeneration = fresh.Generation
		fresh.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{WorkItemRef: corev1.LocalObjectReference{Name: fresh.Spec.Preconditions.WorkItemName}, Message: message}
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
	return fmt.Sprintf("current projection sequence %d resourceVersion %s", item.Status.ProjectionSequence, item.ResourceVersion)
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
		comment, _, err = githubClient.CreateIssueComment(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber), &github.IssueComment{Body: github.String(body)})
		if err != nil {
			return "", "", fmt.Errorf("creating triage comment: %w", err)
		}
	} else if comment.GetBody() != body {
		comment, _, err = githubClient.EditIssueComment(ctx, repository.Spec.Owner, repository.Spec.Repo, comment.GetID(), &github.IssueComment{Body: github.String(body)})
		if err != nil {
			return "", "", fmt.Errorf("editing triage comment: %w", err)
		}
	}
	issue, _, err := githubClient.GetIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber))
	if err != nil {
		return "", "", fmt.Errorf("getting issue for triage close: %w", err)
	}
	if issue.GetState() != "closed" || issue.GetStateReason() != string(*triage.CloseReason) {
		issue, _, err = githubClient.EditIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(item.Spec.IssueNumber), &github.IssueRequest{State: github.String("closed"), StateReason: github.String(string(*triage.CloseReason))})
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
