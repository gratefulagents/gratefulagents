package triggers

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	monitorOpenInterval      = 45 * time.Second
	monitorResolvingInterval = 90 * time.Second
	monitorPendingInterval   = 30 * time.Second
	monitorClosedInterval    = 15 * time.Minute
	monitorMaxBackoff        = 15 * time.Minute
)

type PullRequestMonitorReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Engine          *PRLoopEngine
	GitHubAppMinter gitHubAppTokenMinter
	Recorder        record.EventRecorder
	Poller          pullRequestGitHubPoller
	Now             func() time.Time
}

// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=githubrepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=pullrequestmonitors,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=pullrequestmonitors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *PullRequestMonitorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	monitor := &triggersv1alpha1.PullRequestMonitor{}
	if err := r.Get(ctx, req.NamespacedName, monitor); err != nil {
		observeMonitorStopped(req.String())
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	observeMonitorStarted(req.String())
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	if monitor.Status.State == "" {
		if err := r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
			if status.State == "" {
				status.State = triggersv1alpha1.PullRequestMonitorStatePending
			}
		}); err != nil {
			return ctrl.Result{}, err
		}
		monitor.Status.State = triggersv1alpha1.PullRequestMonitorStatePending
		if r.Recorder != nil {
			r.Recorder.Event(monitor, corev1.EventTypeNormal, "MonitoringStarted", "Started pull request monitoring")
		}
	}
	if monitor.Status.RetryAfter != nil && now.Before(monitor.Status.RetryAfter.Time) {
		return ctrl.Result{RequeueAfter: monitor.Status.RetryAfter.Sub(now)}, nil
	}
	if monitor.Status.RateLimitRemaining == 0 && monitor.Status.RateLimitReset != nil && now.Before(monitor.Status.RateLimitReset.Time) {
		return ctrl.Result{RequeueAfter: monitor.Status.RateLimitReset.Sub(now)}, nil
	}

	run, err := r.validatedImplementer(ctx, monitor)
	if err != nil {
		return r.fail(ctx, monitor, "ValidationFailed", err, now)
	}
	repository, token, err := r.resolveAuth(ctx, monitor, run)
	if err != nil {
		return r.fail(ctx, monitor, "AuthenticationFailed", err, now)
	}
	owner, repo, ok := strings.Cut(normalizeRepositoryName(monitor.Spec.Repository), "/")
	if !ok || owner == "" || repo == "" {
		return r.fail(ctx, monitor, "InvalidRepository", fmt.Errorf("invalid repository %q", monitor.Spec.Repository), now)
	}
	poller := r.Poller
	if poller == nil {
		poller = newPullRequestGitHubPoller(github.NewClient(nil).WithAuthToken(token))
	}

	pullETag := monitor.Status.ETags.Pull
	if monitor.Status.Lifecycle == "" || (!monitor.Status.OpenedDispatched && feedbackDispatchEnabled(run, repository)) {
		// Opened delivery must be retryable. Force a body until its dispatch
		// cursor is durable instead of allowing a 304 to suppress the event.
		pullETag = ""
	}
	started := time.Now()
	pull, response, err := poller.GetPullRequest(ctx, owner, repo, int(monitor.Spec.Number), pullETag)
	observePoll("pull", pollResult(response, err), time.Since(started))
	r.observeResponse("pull", response)
	if err != nil {
		if statusErr := r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
			status.PullObservedAt, status.PullError = metav1.NewTime(now), err.Error()
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return r.failPoll(ctx, monitor, "PullRequest", err, response, now)
	}
	if response.StatusCode == http.StatusNotModified {
		observeNotModified("pull")
		if err := r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
			status.PullObservedAt, status.PullError = metav1.NewTime(now), ""
		}); err != nil {
			return ctrl.Result{}, err
		}
		monitor.Status.PullObservedAt, monitor.Status.PullError = metav1.NewTime(now), ""
	}
	if pull != nil && !strings.EqualFold(pull.URL, monitor.Spec.URL) {
		return r.fail(ctx, monitor, "IdentityMismatch", fmt.Errorf("GitHub returned pull request URL %q, expected %q", pull.URL, monitor.Spec.URL), now)
	}
	lifecycle, headSHA := monitor.Status.Lifecycle, monitor.Status.HeadSHA
	if pull != nil {
		headChanged := monitor.Status.HeadSHA != "" && monitor.Status.HeadSHA != pull.HeadSHA
		lifecycle, headSHA = pullRequestLifecycle(pull), pull.HeadSHA
		if err := r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
			applyPullMetadata(status, pull, response)
			status.Lifecycle = lifecycle
			status.MergedAt = metav1.NewTime(pull.MergedAt)
			status.Mergeability = pullRequestMergeability(pull)
			status.PullObservedAt, status.PullError = metav1.NewTime(now), ""
			if headChanged {
				status.Checks, status.Statuses = triggersv1alpha1.PullRequestMonitorHeadRollup{}, triggersv1alpha1.PullRequestMonitorHeadRollup{}
			}
		}); err != nil {
			return ctrl.Result{}, err
		}
		monitor.Status.HeadSHA, monitor.Status.Lifecycle = pull.HeadSHA, lifecycle
	}
	if lifecycle == triggersv1alpha1.PullRequestLifecycleMerged || lifecycle == triggersv1alpha1.PullRequestLifecycleClosed {
		if err := r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
			status.LastPollTime = ptrTime(now)
			status.LastError = ""
			status.ConsecutiveErrors = 0
			status.RetryAfter = nil
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: triggersv1alpha1.ConditionPullRequestMonitorReady, Status: metav1.ConditionTrue, Reason: "LifecycleObserved", ObservedGeneration: monitor.Generation, LastTransitionTime: metav1.NewTime(now)})
		}); err != nil {
			return ctrl.Result{}, err
		}
		if lifecycle == triggersv1alpha1.PullRequestLifecycleMerged {
			return ctrl.Result{}, nil
		}
		// GitHub permits reopening a closed pull request, so keep its lifecycle observation alive.
		return ctrl.Result{RequeueAfter: jitter(monitorClosedInterval)}, nil
	}
	if headSHA == "" {
		return r.fail(ctx, monitor, "PullRequestNotObserved", fmt.Errorf("pull request head SHA is unavailable"), now)
	}

	if pull != nil && !monitor.Status.OpenedDispatched && monitor.Status.State != triggersv1alpha1.PullRequestMonitorStateInactive && feedbackDispatchEnabled(run, repository) {
		event := pullRequestEvent(monitor, pull, PREventOpened)
		handled, dispatchErr := r.dispatch(ctx, repository, event)
		if dispatchErr != nil {
			return r.fail(ctx, monitor, "DispatchOpened", dispatchErr, now)
		}
		if err := r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
			status.OpenedDispatched = handled
			if handled {
				status.State = monitorStateForRun(run, monitor)
			} else {
				status.State = triggersv1alpha1.PullRequestMonitorStateInactive
			}
		}); err != nil {
			return ctrl.Result{}, err
		}
		monitor.Status.OpenedDispatched = handled
		if !handled {
			monitor.Status.State = triggersv1alpha1.PullRequestMonitorStateInactive
		}
	}

	started = time.Now()
	checks, checksResponse, err := poller.ListCheckRuns(ctx, owner, repo, headSHA)
	observePoll("checks", pollResult(checksResponse, err), time.Since(started))
	r.observeResponse("checks", checksResponse)
	if err != nil {
		if statusErr := r.recordHeadRollup(ctx, monitor, true, polledHeadRollup{HeadSHA: headSHA}, err, now); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return r.failPoll(ctx, monitor, "Checks", err, checksResponse, now)
	}
	if err := r.recordHeadRollup(ctx, monitor, true, checks, nil, now); err != nil {
		return ctrl.Result{}, err
	}

	started = time.Now()
	statuses, statusesResponse, err := poller.GetCommitStatus(ctx, owner, repo, headSHA)
	observePoll("statuses", pollResult(statusesResponse, err), time.Since(started))
	r.observeResponse("statuses", statusesResponse)
	if err != nil {
		if statusErr := r.recordHeadRollup(ctx, monitor, false, polledHeadRollup{HeadSHA: headSHA}, err, now); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return r.failPoll(ctx, monitor, "Statuses", err, statusesResponse, now)
	}
	if err := r.recordHeadRollup(ctx, monitor, false, statuses, nil, now); err != nil {
		return ctrl.Result{}, err
	}

	discoveredAt := monitor.Spec.DiscoveredAt.Time
	if discoveredAt.IsZero() {
		discoveredAt = now
	}
	initial := discoveredAt.Add(-24 * time.Hour)
	if pull != nil && pull.CreatedAt.After(initial) {
		initial = pull.CreatedAt
	}
	reviewCursor := cursorOrInitial(monitor.Status.LastReviewCursor, initial)
	commentCursor := cursorOrInitial(monitor.Status.LastIssueCommentCursor, initial)

	started = time.Now()
	reviews, reviewResponse, err := poller.ListReviews(ctx, owner, repo, int(monitor.Spec.Number), time.Time{})
	observePoll("reviews", pollResult(reviewResponse, err), time.Since(started))
	r.observeResponse("reviews", reviewResponse)
	if err := r.recordReviewObservation(ctx, monitor, err, now); err != nil {
		return ctrl.Result{}, err
	}
	if err != nil {
		return r.failPoll(ctx, monitor, "Reviews", err, reviewResponse, now)
	}
	started = time.Now()
	reviewDecision, decisionResponse, err := poller.GetReviewDecision(ctx, owner, repo, int(monitor.Spec.Number))
	observePoll("review-decision", pollResult(decisionResponse, err), time.Since(started))
	r.observeResponse("review-decision", decisionResponse)
	if err := r.recordReviewObservation(ctx, monitor, err, now); err != nil {
		return ctrl.Result{}, err
	}
	if err != nil {
		return r.failPoll(ctx, monitor, "ReviewDecision", err, decisionResponse, now)
	}
	started = time.Now()
	comments, commentResponse, err := poller.ListIssueComments(ctx, owner, repo, int(monitor.Spec.Number), commentCursor.Timestamp.Time.Add(-time.Nanosecond))
	observePoll("comments", pollResult(commentResponse, err), time.Since(started))
	r.observeResponse("comments", commentResponse)
	if err := r.recordCommentObservation(ctx, monitor, err, now); err != nil {
		return ctrl.Result{}, err
	}
	if err != nil {
		return r.failPoll(ctx, monitor, "Comments", err, commentResponse, now)
	}

	feedback := combinedFeedback(monitor, pull, reviews, comments, reviewCursor, commentCursor)
	for _, item := range feedback {
		handled := false
		var dispatchErr error
		if monitor.Status.State != triggersv1alpha1.PullRequestMonitorStateInactive && feedbackDispatchEnabled(run, repository) {
			handled, dispatchErr = r.dispatch(ctx, repository, item.event)
		}
		if dispatchErr != nil {
			return r.fail(ctx, monitor, "DispatchFeedback", dispatchErr, now)
		}
		if handled {
			observeFeedbackDispatched()
			if r.Recorder != nil {
				kind := "comment"
				if item.kind == feedbackReview {
					kind = "review"
				}
				r.Recorder.Eventf(monitor, corev1.EventTypeNormal, "FeedbackDispatched", "Dispatched GitHub %s %d from @%s", kind, item.id, item.event.SenderLogin)
			}
		} else {
			observeFeedbackIgnored()
		}
		key := client.ObjectKeyFromObject(monitor)
		if err := r.updateStatus(ctx, key, func(status *triggersv1alpha1.PullRequestMonitorStatus) {
			cursor := &triggersv1alpha1.GitHubObjectCursor{Timestamp: metav1.NewTime(item.at), ID: item.id}
			if item.kind == feedbackReview {
				status.LastReviewCursor = cursor
			} else {
				status.LastIssueCommentCursor = cursor
			}
		}); err != nil {
			return ctrl.Result{}, err
		}
	}

	nextPoll := jitter(intervalForLifecycle(lifecycle, monitorStateForRun(run, monitor)))
	rateLimited := false
	for _, observed := range []gitHubPollResponse{response, checksResponse, statusesResponse, reviewResponse, decisionResponse, commentResponse} {
		if observed.RateLimit > 0 && observed.RateRemaining == 0 && observed.RateReset.After(now) && observed.RateReset.Sub(now) > nextPoll {
			nextPoll = observed.RateReset.Sub(now)
			rateLimited = true
		}
	}
	if rateLimited && r.Recorder != nil {
		r.Recorder.Eventf(monitor, corev1.EventTypeWarning, "RateLimitDelayed", "GitHub rate limit delays polling for %s", nextPoll.Round(time.Second))
	}
	if err := r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
		if pull != nil {
			applyPullMetadata(status, pull, response)
		}
		if status.LastReviewCursor == nil {
			status.LastReviewCursor = reviewCursor
		}
		if status.LastIssueCommentCursor == nil {
			status.LastIssueCommentCursor = commentCursor
		}
		status.State = monitorStateForRun(run, monitor)
		status.ReviewDecision = reviewDecision
		status.LastPollTime = ptrTime(now)
		status.LastError = ""
		status.ConsecutiveErrors = 0
		status.RetryAfter = nil
		applyRate(status, response, checksResponse, statusesResponse, reviewResponse, decisionResponse, commentResponse)
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: triggersv1alpha1.ConditionPullRequestMonitorReady, Status: metav1.ConditionTrue, Reason: "Polling", ObservedGeneration: monitor.Generation, LastTransitionTime: metav1.NewTime(now)})
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: nextPoll}, nil
}

func (r *PullRequestMonitorReconciler) validatedImplementer(ctx context.Context, monitor *triggersv1alpha1.PullRequestMonitor) (*platformv1alpha1.AgentRun, error) {
	owner := metav1.GetControllerOf(monitor)
	if owner == nil || owner.APIVersion != platformv1alpha1.GroupVersion.String() || owner.Kind != "AgentRun" || owner.Name != monitor.Spec.ImplementerRef.Name || owner.UID == "" {
		return nil, fmt.Errorf("monitor controller owner is not the referenced AgentRun")
	}
	run := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: monitor.Namespace, Name: monitor.Spec.ImplementerRef.Name}, run); err != nil {
		return nil, fmt.Errorf("getting implementer: %w", err)
	}
	if run.UID != owner.UID {
		return nil, fmt.Errorf("implementer UID %q does not match controller owner UID %q", run.UID, owner.UID)
	}
	if !runRecordsPullRequestURL(run, monitor.Spec.URL) {
		return nil, fmt.Errorf("implementer does not record pull request URL %q", monitor.Spec.URL)
	}
	if !runDeclaresRepository(run, monitor.Spec.Repository) {
		return nil, fmt.Errorf("implementer does not declare repository %q", monitor.Spec.Repository)
	}
	return run, nil
}

func (r *PullRequestMonitorReconciler) resolveAuth(ctx context.Context, monitor *triggersv1alpha1.PullRequestMonitor, run *platformv1alpha1.AgentRun) (*triggersv1alpha1.GitHubRepository, string, error) {
	if monitor.Spec.GitHubRepositoryRef != nil {
		gh := &triggersv1alpha1.GitHubRepository{}
		key := client.ObjectKey{Namespace: monitor.Namespace, Name: monitor.Spec.GitHubRepositoryRef.Name}
		if err := r.Get(ctx, key, gh); err != nil {
			return nil, "", fmt.Errorf("explicit GitHubRepository %s/%s: %w", key.Namespace, key.Name, err)
		}
		if normalizeRepositoryName(gh.Spec.Owner+"/"+gh.Spec.Repo) != normalizeRepositoryName(monitor.Spec.Repository) {
			return nil, "", fmt.Errorf("explicit GitHubRepository %s/%s does not match %s", key.Namespace, key.Name, monitor.Spec.Repository)
		}
		token, err := resolveGitHubPollingToken(ctx, r.Client, gh, r.GitHubAppMinter)
		return gh, token, err
	}
	list := &triggersv1alpha1.GitHubRepositoryList{}
	if err := r.List(ctx, list, client.InNamespace(monitor.Namespace)); err != nil {
		return nil, "", err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	var match *triggersv1alpha1.GitHubRepository
	for i := range list.Items {
		gh := &list.Items[i]
		if normalizeRepositoryName(gh.Spec.Owner+"/"+gh.Spec.Repo) != normalizeRepositoryName(monitor.Spec.Repository) {
			continue
		}
		if match != nil {
			return nil, "", fmt.Errorf("multiple GitHubRepositories in namespace %s match %s", monitor.Namespace, monitor.Spec.Repository)
		}
		match = gh
	}
	if match != nil {
		token, err := resolveGitHubPollingToken(ctx, r.Client, match, r.GitHubAppMinter)
		return match, token, err
	}
	if run.Spec.Secrets == nil || strings.TrimSpace(run.Spec.Secrets.GitHubTokenSecret) == "" {
		return nil, "", fmt.Errorf("no matching GitHubRepository or implementer GitHub token Secret")
	}
	token, err := ReadSecretValue(ctx, r.Client, run.Namespace, run.Spec.Secrets.GitHubTokenSecret, "token")
	return nil, token, err
}

type feedbackKind int

const (
	feedbackReview feedbackKind = iota
	feedbackComment
)

type monitorFeedback struct {
	kind  feedbackKind
	at    time.Time
	id    int64
	event PullRequestEvent
}

func combinedFeedback(monitor *triggersv1alpha1.PullRequestMonitor, pull *polledPullRequest, reviews []polledPullRequestReview, comments []polledIssueComment, reviewCursor, commentCursor *triggersv1alpha1.GitHubObjectCursor) []monitorFeedback {
	result := make([]monitorFeedback, 0, len(reviews)+len(comments))
	for _, review := range reviews {
		if !afterCursor(review.SubmittedAt, review.ID, reviewCursor) {
			observeFeedbackIgnored()
			continue
		}
		event := pullRequestEvent(monitor, pull, PREventReviewSubmitted)
		event.SenderLogin, event.SenderAuthorAssociation = review.AuthorLogin, review.AuthorAssociation
		event.ReviewState, event.Body, event.SourceID, event.SourceCreatedAt = review.State, review.Body, review.ID, review.SubmittedAt
		result = append(result, monitorFeedback{kind: feedbackReview, at: review.SubmittedAt, id: review.ID, event: event})
	}
	for _, comment := range comments {
		if !afterCursor(comment.CreatedAt, comment.ID, commentCursor) {
			observeFeedbackIgnored()
			continue
		}
		event := pullRequestEvent(monitor, pull, PREventComment)
		event.SenderLogin, event.SenderAuthorAssociation = comment.AuthorLogin, comment.AuthorAssociation
		event.Body, event.SourceID, event.SourceCreatedAt = comment.Body, comment.ID, comment.CreatedAt
		result = append(result, monitorFeedback{kind: feedbackComment, at: comment.CreatedAt, id: comment.ID, event: event})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].at.Equal(result[j].at) {
			return result[i].at.Before(result[j].at)
		}
		if result[i].id != result[j].id {
			return result[i].id < result[j].id
		}
		return result[i].kind < result[j].kind
	})
	return result
}

func pullRequestEvent(monitor *triggersv1alpha1.PullRequestMonitor, pull *polledPullRequest, eventType PullRequestEventType) PullRequestEvent {
	event := PullRequestEvent{
		Type: eventType, Repository: monitor.Spec.Repository, Number: int(monitor.Spec.Number), URL: monitor.Spec.URL,
		Title: monitor.Status.Title, HeadRef: monitor.Status.HeadRef, BaseRef: monitor.Status.BaseRef, AuthorLogin: monitor.Status.AuthorLogin,
		TargetImplementerNamespace: monitor.Namespace, TargetImplementerName: monitor.Spec.ImplementerRef.Name,
	}
	if pull != nil {
		event.Title, event.HeadRef, event.BaseRef, event.AuthorLogin = pull.Title, pull.HeadRef, pull.BaseRef, pull.AuthorLogin
	}
	return event
}

func (r *PullRequestMonitorReconciler) dispatch(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, event PullRequestEvent) (bool, error) {
	if r.Engine == nil {
		return false, fmt.Errorf("PR loop engine is required")
	}
	return r.Engine.HandlePullRequestEvent(ctx, repository, event)
}

func feedbackDispatchEnabled(run *platformv1alpha1.AgentRun, repository *triggersv1alpha1.GitHubRepository) bool {
	return !reviewLoopDisabledForRun(run, repository)
}

func monitorStateForRun(run *platformv1alpha1.AgentRun, monitor *triggersv1alpha1.PullRequestMonitor) triggersv1alpha1.PullRequestMonitorState {
	if monitor != nil {
		switch monitor.Status.State {
		case triggersv1alpha1.PullRequestMonitorStateApproved, triggersv1alpha1.PullRequestMonitorStateBlocked, triggersv1alpha1.PullRequestMonitorStateCancelled, triggersv1alpha1.PullRequestMonitorStateInactive:
			return monitor.Status.State
		}
	}
	if run.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		return triggersv1alpha1.PullRequestMonitorStateCancelled
	}
	loopKey := ""
	if monitor != nil {
		loopKey = prLoopKey(monitor.Spec.Repository, int(monitor.Spec.Number))
	}
	switch loopState(run, loopKey, PRLoopStateLabel) {
	case PRLoopStateResolving:
		return triggersv1alpha1.PullRequestMonitorStateResolving
	case PRLoopStateApproved:
		return triggersv1alpha1.PullRequestMonitorStateApproved
	case PRLoopStateBlocked:
		return triggersv1alpha1.PullRequestMonitorStateBlocked
	default:
		return triggersv1alpha1.PullRequestMonitorStateOpen
	}
}

func (r *PullRequestMonitorReconciler) fail(ctx context.Context, monitor *triggersv1alpha1.PullRequestMonitor, reason string, cause error, now time.Time) (ctrl.Result, error) {
	observeMonitorError(reason)
	delay := backoff(monitor.Status.ConsecutiveErrors + 1)
	err := r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
		status.LastError = cause.Error()
		status.ConsecutiveErrors++
		status.RetryAfter = ptrTime(now.Add(delay))
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: triggersv1alpha1.ConditionPullRequestMonitorReady, Status: metav1.ConditionFalse, Reason: reason, Message: cause.Error(), ObservedGeneration: monitor.Generation, LastTransitionTime: metav1.NewTime(now)})
	})
	if r.Recorder != nil {
		r.Recorder.Event(monitor, corev1.EventTypeWarning, reason, cause.Error())
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: delay}, nil
}

func (r *PullRequestMonitorReconciler) failPoll(ctx context.Context, monitor *triggersv1alpha1.PullRequestMonitor, operation string, cause error, response gitHubPollResponse, now time.Time) (ctrl.Result, error) {
	result, err := r.fail(ctx, monitor, operation+"Failed", cause, now)
	if err != nil {
		return result, err
	}
	delay := result.RequeueAfter
	if response.RetryAfter > delay {
		delay = response.RetryAfter
	}
	rateLimited := response.RateRemaining == 0 && response.RateLimit > 0 && response.RateReset.After(now)
	if rateLimited && response.RateReset.Sub(now) > delay {
		delay = response.RateReset.Sub(now)
	}
	if delay != result.RequeueAfter {
		err = r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
			status.RetryAfter = ptrTime(now.Add(delay))
			applyRate(status, response)
		})
		result.RequeueAfter = delay
	}
	if err == nil && rateLimited && r.Recorder != nil {
		r.Recorder.Eventf(monitor, corev1.EventTypeWarning, "RateLimitDelayed", "GitHub rate limit delays polling for %s", delay.Round(time.Second))
	}
	return result, err
}

func (r *PullRequestMonitorReconciler) updateStatus(ctx context.Context, key types.NamespacedName, mutate func(*triggersv1alpha1.PullRequestMonitorStatus)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.PullRequestMonitor{}
		if err := r.Get(ctx, key, fresh); err != nil {
			return err
		}
		mutate(&fresh.Status)
		return r.Status().Update(ctx, fresh)
	})
}

func (r *PullRequestMonitorReconciler) observeResponse(endpoint string, response gitHubPollResponse) {
	if response.RateLimit != 0 {
		observeRateRemaining(endpoint, response.RateRemaining)
	}
}

func pullRequestLifecycle(pull *polledPullRequest) triggersv1alpha1.PullRequestLifecycle {
	if pull.Merged {
		return triggersv1alpha1.PullRequestLifecycleMerged
	}
	if strings.EqualFold(pull.State, "closed") {
		return triggersv1alpha1.PullRequestLifecycleClosed
	}
	if pull.Draft {
		return triggersv1alpha1.PullRequestLifecycleDraft
	}
	return triggersv1alpha1.PullRequestLifecycleOpen
}

func pullRequestMergeability(pull *polledPullRequest) triggersv1alpha1.PullRequestMergeability {
	if !pull.MergeableKnown || strings.EqualFold(pull.MergeableState, "unknown") {
		return triggersv1alpha1.PullRequestMergeabilityUnknown
	}
	if pull.Mergeable {
		return triggersv1alpha1.PullRequestMergeabilityMergeable
	}
	return triggersv1alpha1.PullRequestMergeabilityConflicting
}

func aggregateReviewDecision(reviews []polledPullRequestReview) triggersv1alpha1.PullRequestReviewDecision {
	latest := map[string]polledPullRequestReview{}
	for _, review := range reviews {
		login := strings.ToLower(strings.TrimSpace(review.AuthorLogin))
		if login == "" || review.SubmittedAt.IsZero() {
			continue
		}
		current, ok := latest[login]
		if !ok || review.SubmittedAt.After(current.SubmittedAt) || review.SubmittedAt.Equal(current.SubmittedAt) && review.ID > current.ID {
			latest[login] = review
		}
	}
	if len(latest) == 0 {
		return triggersv1alpha1.PullRequestReviewDecisionReviewRequired
	}
	approved := false
	for _, review := range latest {
		switch strings.ToLower(review.State) {
		case "changes_requested":
			return triggersv1alpha1.PullRequestReviewDecisionChangesRequested
		case "approved":
			approved = true
		}
	}
	if approved {
		return triggersv1alpha1.PullRequestReviewDecisionApproved
	}
	return triggersv1alpha1.PullRequestReviewDecisionReviewRequired
}

func (r *PullRequestMonitorReconciler) recordHeadRollup(ctx context.Context, monitor *triggersv1alpha1.PullRequestMonitor, checks bool, rollup polledHeadRollup, cause error, now time.Time) error {
	return r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
		value := triggersv1alpha1.PullRequestMonitorHeadRollup{HeadSHA: rollup.HeadSHA, State: rollup.State, Count: int32(rollup.Count), ObservedAt: metav1.NewTime(now)}
		if cause != nil {
			value.Error = cause.Error()
		}
		if checks {
			status.Checks = value
		} else {
			status.Statuses = value
		}
	})
}

func (r *PullRequestMonitorReconciler) recordReviewObservation(ctx context.Context, monitor *triggersv1alpha1.PullRequestMonitor, cause error, now time.Time) error {
	return r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
		status.ReviewsObservedAt, status.ReviewsError = metav1.NewTime(now), ""
		if cause != nil {
			status.ReviewsError = cause.Error()
		}
	})
}

func (r *PullRequestMonitorReconciler) recordCommentObservation(ctx context.Context, monitor *triggersv1alpha1.PullRequestMonitor, cause error, now time.Time) error {
	return r.updateStatus(ctx, client.ObjectKeyFromObject(monitor), func(status *triggersv1alpha1.PullRequestMonitorStatus) {
		status.CommentsObservedAt, status.CommentsError = metav1.NewTime(now), ""
		if cause != nil {
			status.CommentsError = cause.Error()
		}
	})
}

func applyPullMetadata(status *triggersv1alpha1.PullRequestMonitorStatus, pull *polledPullRequest, response gitHubPollResponse) {
	status.Title, status.HeadRef, status.HeadSHA, status.BaseRef, status.AuthorLogin = pull.Title, pull.HeadRef, pull.HeadSHA, pull.BaseRef, pull.AuthorLogin
	if response.ETag != "" {
		status.ETags.Pull = response.ETag
	}
}

func applyRate(status *triggersv1alpha1.PullRequestMonitorStatus, responses ...gitHubPollResponse) {
	for _, response := range responses {
		if response.RateLimit != 0 {
			status.RateLimitRemaining = int32(response.RateRemaining)
		}
		if !response.RateReset.IsZero() {
			status.RateLimitReset = ptrTime(response.RateReset)
		}
	}
}

func cursorOrInitial(cursor *triggersv1alpha1.GitHubObjectCursor, initial time.Time) *triggersv1alpha1.GitHubObjectCursor {
	if cursor != nil {
		return cursor.DeepCopy()
	}
	return &triggersv1alpha1.GitHubObjectCursor{Timestamp: metav1.NewTime(initial)}
}

func afterCursor(timestamp time.Time, id int64, cursor *triggersv1alpha1.GitHubObjectCursor) bool {
	return timestamp.After(cursor.Timestamp.Time) || timestamp.Equal(cursor.Timestamp.Time) && id > cursor.ID
}

func ptrTime(value time.Time) *metav1.Time {
	result := metav1.NewTime(value)
	return &result
}

func pollResult(response gitHubPollResponse, err error) string {
	if err != nil {
		return "error"
	}
	if response.StatusCode == http.StatusNotModified {
		return "not_modified"
	}
	return "success"
}

func intervalForLifecycle(lifecycle triggersv1alpha1.PullRequestLifecycle, state triggersv1alpha1.PullRequestMonitorState) time.Duration {
	if lifecycle == triggersv1alpha1.PullRequestLifecycleClosed {
		return monitorClosedInterval
	}
	return intervalForState(state)
}

func intervalForState(state triggersv1alpha1.PullRequestMonitorState) time.Duration {
	switch state {
	case triggersv1alpha1.PullRequestMonitorStateResolving:
		return monitorResolvingInterval
	case triggersv1alpha1.PullRequestMonitorStateOpen, triggersv1alpha1.PullRequestMonitorStateApproved, triggersv1alpha1.PullRequestMonitorStateBlocked, triggersv1alpha1.PullRequestMonitorStateCancelled, triggersv1alpha1.PullRequestMonitorStateInactive:
		return monitorOpenInterval
	default:
		return monitorPendingInterval
	}
}

func jitter(base time.Duration) time.Duration {
	return time.Duration(float64(base) * (0.9 + rand.Float64()*0.2))
}

func backoff(errors int32) time.Duration {
	delay := monitorPendingInterval
	for i := int32(1); i < errors && delay < monitorMaxBackoff; i++ {
		delay *= 2
	}
	if delay > monitorMaxBackoff {
		return monitorMaxBackoff
	}
	return delay
}

func (r *PullRequestMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.PullRequestMonitor{}).
		Named("pull-request-monitor").
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		Complete(r)
}
