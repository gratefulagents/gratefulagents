package triggers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maintainerPromoteSucceededAnnotation = "platform.gratefulagents.dev/promote-succeeded-requested"
	maintainerPromoteSucceededReason     = "platform.gratefulagents.dev/promote-succeeded-reason"
	maintainerGitHubIssueStateClosed     = "closed"
)

func effectiveMaintainerCutover(repository *triggersv1alpha1.GitHubRepository) triggersv1alpha1.MaintainerWorkItemCutoverMode {
	if repository != nil && repository.Spec.Maintainer != nil && repository.Spec.Maintainer.WorkItemCutover != "" {
		return repository.Spec.Maintainer.WorkItemCutover
	}
	return triggersv1alpha1.MaintainerWorkItemCutoverController
}

//nolint:gocyclo // Guard ordering is intentionally centralized around the irreversible merge boundary.
func (r *GitHubRepositoryReconciler) processMaintainerRequestMerge(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, githubClient maintainerGitHubDeliveryClient, pending bool) error {
	request := command.Spec.RequestMerge
	if effectiveMaintainerCutover(repository) != triggersv1alpha1.MaintainerWorkItemCutoverController {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "typed merge authority requires Controller work-item cutover after dual-read parity")
	}
	if githubClient == nil {
		return r.failMaintainerWorkItemCommand(ctx, command, item, "GitHub delivery client is unavailable")
	}
	if repository.Spec.Maintainer == nil || !repository.Spec.Maintainer.AllowPullRequestMerge {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "repository does not grant maintainer merge permission")
	}
	if request.Repository != repository.Spec.Owner+"/"+repository.Spec.Repo {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "request repository identity does not match GitHubRepository")
	}
	fresh := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(item), fresh); err != nil {
		return err
	}
	if fresh.UID != command.Spec.Preconditions.WorkItemUID || !fresh.DeletionTimestamp.IsZero() {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "target work-item UID changed or deletion is in progress")
	}
	if record := verifiedMaintainerMerge(fresh, request.Repository, request.PullRequestNumber); record != nil {
		if record.HeadSHA != request.ExpectedHeadSHA {
			return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "pull request was verified merged at a different head")
		}
		return r.completeMaintainerWorkItemCommand(ctx, command, fresh, "pull request merge was already verified", "", observedIssueState(fresh))
	}
	owner, repo, _ := strings.Cut(request.Repository, "/")
	mergeAttempted, err := r.maintainerMergeWasAttempted(ctx, command)
	if err != nil {
		return err
	}
	if mergeAttempted {
		pull, _, err := githubClient.GetPullRequest(ctx, owner, repo, int(request.PullRequestNumber), "")
		if err != nil {
			return r.failMaintainerWorkItemCommand(ctx, command, fresh, "merge verification GitHub read failed: "+err.Error())
		}
		if pull.Merged {
			return r.verifyAndRecordMaintainerMerge(ctx, command, fresh, request, pull)
		}
		if pull.HeadSHA != request.ExpectedHeadSHA || !strings.EqualFold(pull.State, "open") {
			return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "attempted merge is definitively closed-unmerged or no longer at the expected head")
		}
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "a merge attempt is durable but GitHub has not reported MERGED with mergedAt; no automatic duplicate merge was submitted")
	}
	projection := projectedMaintainerPullRequest(fresh, request.Repository, request.PullRequestNumber)
	if projection == nil {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "pull request is not required by this work item")
	}
	if projection.HeadSHA != request.ExpectedHeadSHA {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "expected head SHA does not match the current projection")
	}
	if fresh.Status.Readiness == nil || !fresh.Status.Readiness.ReadyToMerge || !projectedPullRequestReady(projection, time.Now()) {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "work item and pull request are not ReadyToMerge with fresh observations")
	}
	if pending {
		if err := r.setMaintainerWorkItemCommandAccepted(ctx, command, fresh); err != nil {
			return err
		}
	}
	pull, _, err := githubClient.GetPullRequest(ctx, owner, repo, int(request.PullRequestNumber), "")
	if err != nil {
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "pre-merge GitHub pull request read failed: "+err.Error())
	}
	if pull.Merged {
		return r.verifyAndRecordMaintainerMerge(ctx, command, fresh, request, pull)
	}
	if !strings.EqualFold(pull.State, "open") || pull.Draft || !pull.MergeableKnown || !pull.Mergeable || pull.HeadSHA != request.ExpectedHeadSHA || strings.TrimSpace(pull.BaseRef) == "" {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "pre-merge GitHub read no longer satisfies open, non-draft, mergeable, expected-head/base gates")
	}
	policy, err := githubClient.GetMergePolicy(ctx, owner, repo, pull.BaseRef)
	if err != nil {
		// A transient read failure is retryable; only a successfully read but
		// unproven policy is a terminal rejection.
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "pre-merge GitHub merge policy read failed: "+err.Error())
	}
	if !policy.RequiredReviews || !policy.RequiredChecks || !policy.CanMerge || policy.ActorCanBypass {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "server-enforced required-review/check policy and repository merge permission for a non-bypass actor could not be proven")
	}
	review, _, err := githubClient.GetReviewDecision(ctx, owner, repo, int(request.PullRequestNumber))
	if err != nil {
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "pre-merge GitHub review read failed: "+err.Error())
	}
	if review == triggersv1alpha1.PullRequestReviewDecisionUnknown {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "GitHub reported a blank review decision; required approval cannot be proven")
	}
	if review != triggersv1alpha1.PullRequestReviewDecisionApproved {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "GitHub review decision is not approved")
	}
	checks, _, err := githubClient.ListCheckRuns(ctx, owner, repo, request.ExpectedHeadSHA)
	if err != nil {
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "pre-merge GitHub check read failed: "+err.Error())
	}
	statuses, _, err := githubClient.GetCommitStatus(ctx, owner, repo, request.ExpectedHeadSHA)
	if err != nil {
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "pre-merge GitHub status read failed: "+err.Error())
	}
	if checks.HeadSHA != request.ExpectedHeadSHA || statuses.HeadSHA != request.ExpectedHeadSHA {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "GitHub checks or statuses are not bound to the expected head")
	}
	if checks.Count+statuses.Count == 0 {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "GitHub reported zero checks and zero commit statuses; merge fails closed")
	}
	if checks.State != gitHubRollupSuccess && checks.State != gitHubRollupNone || statuses.State != gitHubRollupSuccess && statuses.State != gitHubRollupNone {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "GitHub checks or commit statuses are pending or failing")
	}
	bound := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(fresh), bound); err != nil || bound.UID != command.Spec.Preconditions.WorkItemUID || !bound.DeletionTimestamp.IsZero() {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "target work-item UID changed before merge side effect")
	}
	if err := r.reserveMaintainerMergeAttempt(ctx, command); err != nil {
		return err
	}
	result, err := githubClient.MergePullRequest(ctx, owner, repo, int(request.PullRequestNumber), request.ExpectedHeadSHA, string(request.MergeMethod))
	if err != nil {
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "GitHub merge attempt outcome is ambiguous and will only be re-read, not resubmitted: "+err.Error())
	}
	post, _, err := githubClient.GetPullRequest(ctx, owner, repo, int(request.PullRequestNumber), "")
	if err != nil {
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "merge was requested but post-merge verification failed: "+err.Error())
	}
	if !post.Merged || post.MergedAt.IsZero() {
		message := "merge request was accepted but GitHub has not reported MERGED with mergedAt; work item remains active"
		if result != nil && strings.TrimSpace(result.GetMessage()) != "" {
			message += ": " + result.GetMessage()
		}
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, message)
	}
	return r.verifyAndRecordMaintainerMerge(ctx, command, fresh, request, post)
}

func (r *GitHubRepositoryReconciler) maintainerMergeWasAttempted(ctx context.Context, command *triggersv1alpha1.MaintainerWorkItemCommand) (bool, error) {
	fresh := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(command), fresh); err != nil {
		return false, err
	}
	return fresh.Status.Result != nil && fresh.Status.Result.MergeAttemptedAt != nil, nil
}

func (r *GitHubRepositoryReconciler) reserveMaintainerMergeAttempt(ctx context.Context, command *triggersv1alpha1.MaintainerWorkItemCommand) error {
	now := metav1.Now()
	return retryMaintainerWorkItemCommandStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(command), func(fresh *triggersv1alpha1.MaintainerWorkItemCommand) {
		if fresh.Status.Result == nil {
			fresh.Status.Result = &triggersv1alpha1.MaintainerWorkItemCommandResult{}
		}
		if fresh.Status.Result.MergeAttemptedAt == nil {
			fresh.Status.Result.MergeAttemptedAt = &now
		}
	})
}

func projectedMaintainerPullRequest(item *triggersv1alpha1.MaintainerWorkItem, repository string, number int32) *triggersv1alpha1.MaintainerWorkItemPullRequestProjection {
	for i := range item.Status.PullRequests {
		if item.Status.PullRequests[i].Repository == repository && item.Status.PullRequests[i].Number == number {
			return &item.Status.PullRequests[i]
		}
	}
	return nil
}

func projectedPullRequestReady(pr *triggersv1alpha1.MaintainerWorkItemPullRequestProjection, now time.Time) bool {
	if pr == nil || !pr.Fresh || pr.ObservationError != "" || pr.State != triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen || pr.Draft || pr.Mergeable == nil || !*pr.Mergeable || !strings.EqualFold(pr.ReviewDecision, string(triggersv1alpha1.PullRequestReviewDecisionApproved)) || pr.CheckState != triggersv1alpha1.MaintainerWorkItemCheckStatePassing {
		return false
	}
	for _, observed := range []*metav1.Time{pr.HeadObservedAt, pr.ReviewObservedAt, pr.ChecksObservedAt, pr.StatusesObservedAt} {
		if observed == nil || now.Sub(observed.Time) > maintainerProjectionFreshness {
			return false
		}
	}
	return true
}

func verifiedMaintainerMerge(item *triggersv1alpha1.MaintainerWorkItem, repository string, number int32) *triggersv1alpha1.MaintainerVerifiedPullRequestMerge {
	for i := range item.Status.VerifiedMerges {
		if item.Status.VerifiedMerges[i].Repository == repository && item.Status.VerifiedMerges[i].PullRequestNumber == number {
			return &item.Status.VerifiedMerges[i]
		}
	}
	return nil
}

func (r *GitHubRepositoryReconciler) verifyAndRecordMaintainerMerge(ctx context.Context, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, request *triggersv1alpha1.MaintainerRequestMergeCommand, pull *polledPullRequest) error {
	if pull == nil || !pull.Merged || pull.MergedAt.IsZero() || pull.HeadSHA != request.ExpectedHeadSHA {
		return r.failMaintainerWorkItemCommand(ctx, command, item, "post-merge GitHub verification did not confirm MERGED, mergedAt, and the expected head")
	}
	mergedAt := metav1.NewTime(pull.MergedAt)
	err := r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
		if existing := verifiedMaintainerMerge(fresh, request.Repository, request.PullRequestNumber); existing != nil {
			if existing.HeadSHA != request.ExpectedHeadSHA {
				return false, fmt.Errorf("verified merge head changed")
			}
			return false, nil
		}
		fresh.Status.VerifiedMerges = append(fresh.Status.VerifiedMerges, triggersv1alpha1.MaintainerVerifiedPullRequestMerge{Repository: request.Repository, PullRequestNumber: request.PullRequestNumber, HeadSHA: request.ExpectedHeadSHA, MergedAt: mergedAt, CommandRef: corev1.LocalObjectReference{Name: command.Name}})
		sort.Slice(fresh.Status.VerifiedMerges, func(i, j int) bool {
			if fresh.Status.VerifiedMerges[i].Repository == fresh.Status.VerifiedMerges[j].Repository {
				return fresh.Status.VerifiedMerges[i].PullRequestNumber < fresh.Status.VerifiedMerges[j].PullRequestNumber
			}
			return fresh.Status.VerifiedMerges[i].Repository < fresh.Status.VerifiedMerges[j].Repository
		})
		fresh.Status.ProjectionSequence++
		return true, nil
	})
	if err != nil {
		return err
	}
	return r.completeMaintainerWorkItemCommand(ctx, command, item, "GitHub confirmed pull request MERGED at the expected head", "", observedIssueState(item))
}

func maintainerAcceptedScopeHash(scope *triggersv1alpha1.MaintainerAcceptedScope) string {
	encoded, _ := json.Marshal(scope)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

//nolint:gocyclo // Finalization keeps structural gates and ordered idempotent side effects in one state machine.
func (r *GitHubRepositoryReconciler) processMaintainerFinalizeWorkItem(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, githubClient GitHubTriageClient, deliveryClient maintainerGitHubDeliveryClient, pending bool) error {
	request := command.Spec.Finalize
	if effectiveMaintainerCutover(repository) != triggersv1alpha1.MaintainerWorkItemCutoverController {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "typed finalization authority requires Controller work-item cutover after dual-read parity")
	}
	fresh := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(item), fresh); err != nil {
		return err
	}
	if fresh.UID != command.Spec.Preconditions.WorkItemUID || !fresh.DeletionTimestamp.IsZero() {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "target work-item UID changed or deletion is in progress")
	}
	if fresh.Status.DeliveryAttestation != nil && fresh.Status.DeliveryAttestation.CompletedAt != nil {
		if fresh.Status.DeliveryAttestation.FinalizedByCommand.Name != command.Name {
			return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "work item was finalized by another command")
		}
		return r.completeMaintainerWorkItemCommand(ctx, command, fresh, "work item finalization was already verified", "", triggersv1alpha1.MaintainerIssueStateClosed)
	}
	unmet, err := r.maintainerFinalizationUnmet(ctx, fresh, request)
	if err != nil {
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "finalization predicate read failed: "+err.Error())
	}
	if len(unmet) > 0 {
		return r.rejectMaintainerWorkItemCommand(ctx, repository, command, "finalization predicates are not met: "+strings.Join(unmet, "; "))
	}
	if githubClient == nil || deliveryClient == nil {
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "GitHub clients are unavailable for finalization verification")
	}
	// Re-read every required PR so a stale/closed-unmerged projection cannot finalize.
	for i := range fresh.Status.PullRequests {
		pr := &fresh.Status.PullRequests[i]
		owner, repo, _ := strings.Cut(pr.Repository, "/")
		live, _, err := deliveryClient.GetPullRequest(ctx, owner, repo, int(pr.Number), "")
		if err != nil {
			return r.failMaintainerWorkItemCommand(ctx, command, fresh, "finalization pull request verification failed: "+err.Error())
		}
		if live == nil || !live.Merged || live.MergedAt.IsZero() || live.HeadSHA != pr.HeadSHA {
			return r.rejectMaintainerWorkItemCommand(ctx, repository, command, fmt.Sprintf("%s#%d is not confirmed merged at projected head", pr.Repository, pr.Number))
		}
	}
	if pending {
		if err := r.setMaintainerWorkItemCommandAccepted(ctx, command, fresh); err != nil {
			return err
		}
	}
	if err := r.ensureMaintainerDeliveryAttestation(ctx, command, fresh, request); err != nil {
		return err
	}
	if err := r.requestMaintainerRunSuccess(ctx, fresh, request); err != nil {
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "requesting implementer run success: "+err.Error())
	}
	if err := r.recordMaintainerRunSuccessRequested(ctx, fresh, request.ImplementerRunNames); err != nil {
		return err
	}
	issue, _, err := githubClient.GetIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(fresh.Spec.IssueNumber))
	if err != nil {
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, "reading issue before finalization: "+err.Error())
	}
	closedState, completedReason := maintainerGitHubIssueStateClosed, string(triggersv1alpha1.MaintainerWorkItemCloseReasonCompleted)
	if !strings.EqualFold(issue.GetState(), closedState) || !strings.EqualFold(issue.GetStateReason(), completedReason) {
		if _, _, err := githubClient.EditIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(fresh.Spec.IssueNumber), &github.IssueRequest{State: &closedState, StateReason: &completedReason}); err != nil {
			return r.failMaintainerWorkItemCommand(ctx, command, fresh, "closing issue after run-success requests: "+err.Error())
		}
	}
	issue, _, err = githubClient.GetIssue(ctx, repository.Spec.Owner, repository.Spec.Repo, int(fresh.Spec.IssueNumber))
	if err != nil || !strings.EqualFold(issue.GetState(), closedState) || !strings.EqualFold(issue.GetStateReason(), completedReason) {
		message := "post-close verification did not confirm closed/completed"
		if err != nil {
			message += ": " + err.Error()
		}
		return r.failMaintainerWorkItemCommand(ctx, command, fresh, message)
	}
	if err := r.completeMaintainerDeliveryAttestation(ctx, fresh); err != nil {
		return err
	}
	return r.completeMaintainerWorkItemCommand(ctx, command, fresh, "all structural predicates, authenticated delivery attestation, run-success requests, and issue closure were verified", "", triggersv1alpha1.MaintainerIssueStateClosed)
}

//nolint:gocyclo // Each fail-closed predicate is reported independently to preserve actionable audit output.
func (r *GitHubRepositoryReconciler) maintainerFinalizationUnmet(ctx context.Context, item *triggersv1alpha1.MaintainerWorkItem, request *triggersv1alpha1.MaintainerFinalizeWorkItemCommand) ([]string, error) {
	var unmet []string
	if item.Spec.AcceptedScope == nil || maintainerAcceptedScopeHash(item.Spec.AcceptedScope) != request.AcceptedScopeHash {
		unmet = append(unmet, "accepted scope hash mismatch")
	}
	if item.Status.PendingDecision != nil {
		unmet = append(unmet, "pending decision exists")
	}
	if len(item.Spec.Children) != len(item.Status.Children) || len(item.Spec.Dependencies) != len(item.Status.Dependencies) {
		unmet = append(unmet, "child or dependency projection set is incomplete")
	}
	childProjections := map[string]types.UID{}
	for _, projection := range item.Status.Children {
		childProjections[projection.Name] = projection.UID
	}
	for _, ref := range item.Spec.Children {
		if childProjections[ref.Name] != ref.UID {
			unmet = append(unmet, "child projection "+ref.Name+" does not match its bound UID")
		}
	}
	dependencyProjections := map[string]types.UID{}
	for _, projection := range item.Status.Dependencies {
		dependencyProjections[projection.Name] = projection.UID
	}
	for _, ref := range item.Spec.Dependencies {
		if dependencyProjections[ref.Name] != ref.UID {
			unmet = append(unmet, "dependency projection "+ref.Name+" does not match its bound UID")
		}
	}
	for _, ref := range append(append([]triggersv1alpha1.MaintainerWorkItemReference(nil), item.Spec.Children...), item.Spec.Dependencies...) {
		if ref.UID == "" {
			unmet = append(unmet, ref.Name+" has no immutable UID binding")
			continue
		}
		target := &triggersv1alpha1.MaintainerWorkItem{}
		if err := r.maintainerReader().Get(ctx, client.ObjectKey{Namespace: item.Namespace, Name: ref.Name}, target); err != nil {
			return unmet, err
		}
		if target.UID != ref.UID || target.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseDelivered || target.Status.DeliveryAttestation == nil || target.Status.DeliveryAttestation.CompletedAt == nil {
			unmet = append(unmet, ref.Name+" is not finalized at the bound UID")
		}
	}
	if len(item.Status.PullRequests) == 0 && len(item.Spec.Children) == 0 {
		unmet = append(unmet, "no required pull requests or finalized children")
	}
	projectedIntents := map[string]int{}
	for _, pr := range item.Status.PullRequests {
		projectedIntents[pr.IntentName]++
		if pr.State != triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged || pr.MergedAt == nil || pr.Repository == "" || pr.Number < 1 || pr.HeadSHA == "" {
			unmet = append(unmet, fmt.Sprintf("%s#%d is not merged", pr.Repository, pr.Number))
		}
	}
	for _, intent := range item.Spec.RequiredPullRequests {
		if projectedIntents[intent.Name] != 1 {
			unmet = append(unmet, "required pull request intent "+intent.Name+" does not have exactly one projection")
		}
	}
	projectedRuns := map[string]triggersv1alpha1.MaintainerWorkItemAgentRunProjection{}
	for _, run := range item.Status.AgentRuns {
		if run.Role == triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer {
			projectedRuns[run.Name] = run
		}
	}
	requested := append([]string(nil), request.ImplementerRunNames...)
	sort.Strings(requested)
	actual := make([]string, 0, len(projectedRuns))
	for name := range projectedRuns {
		actual = append(actual, name)
	}
	sort.Strings(actual)
	if !slices.Equal(requested, actual) {
		unmet = append(unmet, "implementer run names do not exactly match the projection")
	}
	for name, run := range projectedRuns {
		if run.UID == "" {
			unmet = append(unmet, "implementer "+name+" has no immutable UID projection")
		}
		if run.Phase == string(platformv1alpha1.AgentRunPhaseFailed) || run.Phase == string(platformv1alpha1.AgentRunPhaseCancelled) {
			unmet = append(unmet, "implementer "+name+" is in invalid phase "+run.Phase)
		}
	}
	return unmet, nil
}

func (r *GitHubRepositoryReconciler) ensureMaintainerDeliveryAttestation(ctx context.Context, command *triggersv1alpha1.MaintainerWorkItemCommand, item *triggersv1alpha1.MaintainerWorkItem, request *triggersv1alpha1.MaintainerFinalizeWorkItemCommand) error {
	return r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
		if fresh.Generation != item.Generation || fresh.Status.ProjectionSequence != item.Status.ProjectionSequence {
			return false, fmt.Errorf("work-item projection changed after finalization predicates; re-evaluate before attesting")
		}
		if fresh.Status.DeliveryAttestation != nil {
			if fresh.Status.DeliveryAttestation.FinalizedByCommand.Name != command.Name || fresh.Status.DeliveryAttestation.AcceptedScopeHash != request.AcceptedScopeHash {
				return false, rejectMaintainerCommand("a different delivery attestation already exists")
			}
			return false, nil
		}
		fresh.Status.DeliveryAttestation = &triggersv1alpha1.MaintainerDeliveryAttestation{Issuer: command.Spec.Issuer, AcceptedScopeHash: request.AcceptedScopeHash, DeliverySummary: request.DeliverySummary, DeliveryEvidence: request.DeliveryEvidence, FinalizedByCommand: corev1.LocalObjectReference{Name: command.Name}}
		fresh.Status.ProjectionSequence++
		return true, nil
	})
}

func (r *GitHubRepositoryReconciler) requestMaintainerRunSuccess(ctx context.Context, item *triggersv1alpha1.MaintainerWorkItem, request *triggersv1alpha1.MaintainerFinalizeWorkItemCommand) error {
	projectedUIDs := map[string]types.UID{}
	for _, projected := range item.Status.AgentRuns {
		if projected.Role == triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer {
			projectedUIDs[projected.Name] = projected.UID
		}
	}
	for _, name := range request.ImplementerRunNames {
		key := client.ObjectKey{Namespace: item.Namespace, Name: name}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			run := &platformv1alpha1.AgentRun{}
			if err := r.maintainerReader().Get(ctx, key, run); err != nil {
				return err
			}
			if projectedUIDs[name] == "" || run.UID != projectedUIDs[name] || run.Labels[triggersv1alpha1.MaintainerWorkItemNameLabelKey] != item.Name || run.Labels[triggersv1alpha1.MaintainerWorkItemUIDLabelKey] != string(item.UID) || run.Labels[triggersv1alpha1.PRLoopRoleLabelKey] == triggersv1alpha1.PRLoopRoleReviewerValue {
				return fmt.Errorf("AgentRun %s is not the immutable projected implementer", name)
			}
			if run.Status.Phase == platformv1alpha1.AgentRunPhaseSucceeded {
				return nil
			}
			if run.Status.Phase == platformv1alpha1.AgentRunPhaseFailed || run.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
				return fmt.Errorf("AgentRun %s is terminal in phase %s", name, run.Status.Phase)
			}
			if run.Annotations[maintainerPromoteSucceededAnnotation] != "" && run.Annotations[maintainerPromoteSucceededReason] == request.DeliverySummary {
				return nil
			}
			patch := client.MergeFromWithOptions(run.DeepCopy(), client.MergeFromWithOptimisticLock{})
			if run.Annotations == nil {
				run.Annotations = map[string]string{}
			}
			run.Annotations[maintainerPromoteSucceededAnnotation] = time.Now().UTC().Format(time.RFC3339)
			run.Annotations[maintainerPromoteSucceededReason] = request.DeliverySummary
			return r.Patch(ctx, run, patch)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *GitHubRepositoryReconciler) recordMaintainerRunSuccessRequested(ctx context.Context, item *triggersv1alpha1.MaintainerWorkItem, names []string) error {
	return r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
		attestation := fresh.Status.DeliveryAttestation
		if attestation == nil || attestation.RunSuccessRequestedAt != nil {
			return false, nil
		}
		now := metav1.Now()
		attestation.RunSuccessRequestedAt = &now
		projectedUIDs := map[string]types.UID{}
		for _, projected := range fresh.Status.AgentRuns {
			if projected.Role == triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer {
				projectedUIDs[projected.Name] = projected.UID
			}
		}
		for _, name := range names {
			attestation.RunSuccessRequestedRefs = append(attestation.RunSuccessRequestedRefs, triggersv1alpha1.MaintainerWorkItemReference{Name: name, UID: projectedUIDs[name]})
		}
		fresh.Status.ProjectionSequence++
		return true, nil
	})
}

func (r *GitHubRepositoryReconciler) completeMaintainerDeliveryAttestation(ctx context.Context, item *triggersv1alpha1.MaintainerWorkItem) error {
	return r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(item), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
		attestation := fresh.Status.DeliveryAttestation
		if attestation == nil || attestation.CompletedAt != nil {
			return false, nil
		}
		now := metav1.Now()
		attestation.IssueClosedAt, attestation.CompletedAt = &now, &now
		if fresh.Status.IssueObservation != nil {
			fresh.Status.IssueObservation.State = triggersv1alpha1.MaintainerIssueStateClosed
			fresh.Status.IssueObservation.ObservedAt = now
		}
		fresh.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseDelivered
		fresh.Status.ProjectionSequence++
		return true, nil
	})
}
