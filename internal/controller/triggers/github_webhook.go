package triggers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// maxWebhookBodyBytes is the maximum payload size accepted from GitHub.
const maxWebhookBodyBytes = 5 * 1024 * 1024 // 5 MiB

// GitHubWebhookHandler handles incoming GitHub webhook events and dispatches
// them to the GitHubRepositoryReconciler.
type GitHubWebhookHandler struct {
	Client     client.Client
	Reconciler *GitHubRepositoryReconciler
	Recorder   record.EventRecorder
	// AllowUnauthenticated permits unsigned deliveries when spec.webhookSecret
	// is empty. The default is fail-closed.
	AllowUnauthenticated bool
	// PRSink receives normalized pull-request lifecycle events (PR opened,
	// review submitted, review comments). Nil drops PR events.
	PRSink PullRequestEventSink

	dedupOnce sync.Once
	dedup     *deliveryDeduper
}

// Webhook payload types — minimal structs for decoding GitHub events.

type githubIssueCommentEnvelope struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue   githubIssue `json:"issue"`
	Comment struct {
		ID                int64      `json:"id"`
		Body              string     `json:"body"`
		User              githubUser `json:"user"`
		AuthorAssociation string     `json:"author_association"`
		CreatedAt         time.Time  `json:"created_at"`
	} `json:"comment"`
}

type githubIssuesEvent struct {
	Action string      `json:"action"`
	Issue  githubIssue `json:"issue"`
	Label  githubLabel `json:"label"`
}

type githubIssue struct {
	Number            int           `json:"number"`
	Title             string        `json:"title"`
	Body              string        `json:"body"`
	HTMLURL           string        `json:"html_url"`
	Labels            []githubLabel `json:"labels"`
	User              githubUser    `json:"user"`
	AuthorAssociation string        `json:"author_association"`
}

type githubUser struct {
	Login string `json:"login"`
}

type githubLabel struct {
	Name string `json:"name"`
}

// errBadWebhookPayload marks payloads that fail to decode; mapped to 400.
var errBadWebhookPayload = errors.New("invalid webhook payload")

func (h *GitHubWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logf.FromContext(ctx).WithName("github-webhook")

	namespace := r.PathValue("namespace")
	name := r.PathValue("name")
	if namespace == "" || name == "" {
		http.Error(w, "missing namespace or name", http.StatusBadRequest)
		return
	}

	// Look up the GitHubRepository CRD.
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, gh); err != nil {
		log.Error(err, "GitHubRepository not found", "namespace", namespace, "name", name)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Read the request body (bounded).
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Validate signature when a webhookSecret is configured.
	if gh.Spec.WebhookSecret != "" {
		secret := &corev1.Secret{}
		if err := h.Client.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      gh.Spec.WebhookSecret,
		}, secret); err != nil {
			log.Error(err, "failed to read webhook secret", "secret", gh.Spec.WebhookSecret)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		secretValue, ok := secret.Data["secret"]
		if !ok {
			log.Error(nil, "webhook secret missing 'secret' key", "secret", gh.Spec.WebhookSecret)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		signature := r.Header.Get("X-Hub-Signature-256")
		if !validateGitHubSignature(body, signature, secretValue) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	} else {
		if !h.AllowUnauthenticated {
			h.warnUnauthenticatedWebhook(ctx, gh, false)
			http.Error(w, "webhook secret not configured", http.StatusUnauthorized)
			return
		}
		h.warnUnauthenticatedWebhook(ctx, gh, true)
	}

	if h.Reconciler == nil {
		http.Error(w, "no reconciler registered", http.StatusInternalServerError)
		return
	}

	// Drop GitHub webhook redeliveries (same delivery GUID).
	h.dedupOnce.Do(func() { h.dedup = newDeliveryDeduper() })
	if h.dedup.isDuplicate(r.Header.Get("X-GitHub-Delivery")) {
		w.WriteHeader(http.StatusOK)
		return
	}

	writeWebhookResult(w, h.dispatchEvent(ctx, gh, r.Header.Get("X-GitHub-Event"), body))
}

// dispatchEvent routes one decoded delivery to the trigger pipeline for a
// single GitHubRepository. Shared by the per-CR endpoint and the app-level
// endpoint, which fans a delivery out to every matching CR.
func (h *GitHubWebhookHandler) dispatchEvent(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, eventType string, body []byte) error {
	switch eventType {
	case "issue_comment":
		if gh == nil {
			return h.handleUnconfiguredPRComment(ctx, body)
		}
		return h.handleIssueComment(ctx, gh, h.Reconciler, body)
	case "issues":
		if gh == nil {
			return nil
		}
		return h.handleIssuesEvent(ctx, gh, h.Reconciler, body)
	case "pull_request":
		return h.handlePullRequestEvent(ctx, gh, body)
	case "pull_request_review":
		return h.handlePullRequestReviewEvent(ctx, gh, body)
	case "pull_request_review_comment":
		return h.handlePullRequestReviewCommentEvent(ctx, gh, body)
	default:
		return nil
	}
}

// writeWebhookResult maps dispatch errors onto HTTP statuses.
func writeWebhookResult(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusOK)
	case errors.Is(err, errBadWebhookPayload):
		http.Error(w, "invalid payload", http.StatusBadRequest)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (h *GitHubWebhookHandler) handleUnconfiguredPRComment(ctx context.Context, payload []byte) error {
	var event githubIssueCommentEnvelope
	if err := json.Unmarshal(payload, &event); err != nil {
		return errBadWebhookPayload
	}
	if h.PRSink == nil || event.Action != githubActionCreated || !strings.Contains(event.Issue.HTMLURL, "/pull/") {
		return nil
	}
	_, err := h.PRSink.HandlePullRequestEvent(ctx, nil, PullRequestEvent{
		Type:                    PREventComment,
		Repository:              event.Repository.FullName,
		Number:                  event.Issue.Number,
		Title:                   event.Issue.Title,
		URL:                     event.Issue.HTMLURL,
		AuthorLogin:             event.Issue.User.Login,
		SenderLogin:             event.Comment.User.Login,
		SenderAuthorAssociation: event.Comment.AuthorAssociation,
		Body:                    event.Comment.Body,
		SourceID:                event.Comment.ID,
		SourceCreatedAt:         event.Comment.CreatedAt,
	})
	return err
}

func (h *GitHubWebhookHandler) handleIssueComment(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, reconciler *GitHubRepositoryReconciler, payload []byte) error {
	// Unmarshal into go-github type for HandleIssueComment compatibility.
	var event github.IssueCommentEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return errBadWebhookPayload
	}

	// Comments on pull requests created by the loop wake the linked
	// implementer run instead of spawning a fresh run. Unlinked PRs fall
	// through to the legacy keyword-triggered run creation.
	if event.GetIssue().IsPullRequest() && h.PRSink != nil && event.GetAction() == githubActionCreated {
		createdAt := time.Time{}
		if event.GetComment().CreatedAt != nil {
			createdAt = event.GetComment().CreatedAt.Time
		}
		handled, err := h.PRSink.HandlePullRequestEvent(ctx, gh, PullRequestEvent{
			Type:                    PREventComment,
			Repository:              strings.TrimSpace(gh.Spec.Owner) + "/" + strings.TrimSpace(gh.Spec.Repo),
			Number:                  event.GetIssue().GetNumber(),
			Title:                   event.GetIssue().GetTitle(),
			URL:                     event.GetIssue().GetHTMLURL(),
			AuthorLogin:             event.GetIssue().GetUser().GetLogin(),
			SenderLogin:             event.GetComment().GetUser().GetLogin(),
			SenderAuthorAssociation: event.GetComment().GetAuthorAssociation(),
			Body:                    event.GetComment().GetBody(),
			SourceID:                event.GetComment().GetID(),
			SourceCreatedAt:         createdAt,
		})
		if err != nil {
			logf.FromContext(ctx).WithName("github-webhook").Error(err, "PR comment sink failed")
			return err
		}
		if handled {
			return nil
		}
	}

	if err := reconciler.HandleIssueComment(ctx, gh, &event); err != nil {
		logf.FromContext(ctx).WithName("github-webhook").Error(err, "HandleIssueComment failed")
		return err
	}
	return nil
}

func (h *GitHubWebhookHandler) handleIssuesEvent(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, reconciler *GitHubRepositoryReconciler, payload []byte) error {
	var event githubIssuesEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return errBadWebhookPayload
	}

	switch event.Action {
	case "labeled":
	case "closed", "deleted":
		return h.cancelRunsForClosedIssue(ctx, gh, event.Issue.Number)
	default:
		return nil
	}

	// Check if the newly added label matches a known mode template.
	modeRef := ResolveModeFromLabels([]string{event.Label.Name}, ModeExistsFromK8s(ctx, h.Client))
	if modeRef == nil {
		return nil
	}

	if !gh.Spec.Auth.IsGitHubActorAllowed(event.Issue.User.Login, event.Issue.AuthorAssociation) {
		recordTriggerActorRejected(ctx, h.Client, h.Recorder, gh, event.Issue.User.Login, event.Issue.AuthorAssociation)
		return nil
	}

	userRequest := fmt.Sprintf("# %s\n\n%s", event.Issue.Title, event.Issue.Body)
	issueID := fmt.Sprintf("%d", event.Issue.Number)

	existing, err := ExistingTriggerIssueIDs(ctx, h.Client, gh.Namespace, gitHubRepositoryTriggerKind, gh.Name)
	if err != nil {
		return err
	}
	if _, ok := existing[issueID]; ok || hasProcessedIssueID(gh.Status.ProcessedIssueIDs, issueID) {
		return nil
	}

	createdRun, err := reconciler.createAgentRun(ctx, gh, issueID, event.Issue.Number, event.Issue.HTMLURL, userRequest, event.Issue.User.Login, modeRef)
	if err != nil {
		logf.FromContext(ctx).WithName("github-webhook").Error(err, "createAgentRun failed", "issue", event.Issue.Number)
		return err
	}
	// Mark the issue processed even when the run already existed (created by
	// another trigger or an earlier delivery) so redeliveries stop retrying.
	if err := retryGitHubRepositoryStatusUpdate(ctx, h.Client, client.ObjectKeyFromObject(gh), func(fresh *triggersv1alpha1.GitHubRepository) {
		if createdRun {
			fresh.Status.IssuesProcessed++
		}
		fresh.Status.ProcessedIssueIDs = appendProcessedIssueIDs(fresh.Status.ProcessedIssueIDs, issueID)
	}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("updating GitHubRepository processed issue status: %w", err)
	}
	return nil
}

func (h *GitHubWebhookHandler) cancelRunsForClosedIssue(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, issueNumber int) error {
	if !gh.Spec.CancelRunsOnIssueClose {
		return nil
	}
	issueID := fmt.Sprintf("%d", issueNumber)
	runs := &platformv1alpha1.AgentRunList{}
	if err := h.Client.List(ctx, runs, client.InNamespace(gh.Namespace)); err != nil {
		return fmt.Errorf("listing AgentRuns: %w", err)
	}
	cancelled := 0
	for i := range runs.Items {
		run := &runs.Items[i]
		if !TriggerRunMatches(run, gitHubRepositoryTriggerKind, gh.Name) || run.Spec.Trigger.ExternalRef == nil {
			continue
		}
		if strings.TrimSpace(run.Spec.Trigger.ExternalRef.ID) != issueID || isTerminalAgentRunPhase(run.Status.Phase) {
			continue
		}
		key := client.ObjectKey{Namespace: run.Namespace, Name: run.Name}
		requested := false
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			fresh := &platformv1alpha1.AgentRun{}
			if err := h.Client.Get(ctx, key, fresh); err != nil {
				return err
			}
			if isTerminalAgentRunPhase(fresh.Status.Phase) {
				return nil
			}
			if fresh.Annotations != nil && strings.TrimSpace(fresh.Annotations[cancelRequestedAnnotation]) != "" {
				return nil
			}
			before := fresh.DeepCopy()
			if fresh.Annotations == nil {
				fresh.Annotations = map[string]string{}
			}
			fresh.Annotations[cancelRequestedAnnotation] = "true"
			if err := h.Client.Patch(ctx, fresh, client.MergeFrom(before)); err != nil {
				return err
			}
			requested = true
			return nil
		}); err != nil {
			return fmt.Errorf("requesting AgentRun cancellation: %w", err)
		}
		if requested {
			cancelled++
		}
	}
	if cancelled > 0 {
		message := fmt.Sprintf("Requested graceful cancellation for %d active AgentRun(s) for closed issue #%d", cancelled, issueNumber)
		if h.Recorder != nil {
			h.Recorder.Event(gh, corev1.EventTypeNormal, "IssueClosedRunCancelled", message)
		}
	}
	return nil
}

func isTerminalAgentRunPhase(phase platformv1alpha1.AgentRunPhase) bool {
	switch phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled:
		return true
	default:
		return false
	}
}

func recordTriggerActorRejected(ctx context.Context, k8sClient client.Client, recorder record.EventRecorder, gh *triggersv1alpha1.GitHubRepository, login, authorAssociation string) {
	const reason = "TriggerActorRejected"
	message := fmt.Sprintf("GitHub actor %q rejected for author_association %q", login, authorAssociation)
	log := logf.FromContext(ctx).WithName("github-trigger-auth")
	log.Info(message, "namespace", gh.Namespace, "name", gh.Name, "login", login, "authorAssociation", authorAssociation)
	if recorder != nil {
		recorder.Event(gh, corev1.EventTypeWarning, reason, message)
		return
	}
	if k8sClient == nil {
		return
	}
	now := metav1.Now()
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    gh.Namespace,
			GenerateName: gh.Name + "-trigger-rejected-",
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: triggersv1alpha1.GroupVersion.String(),
			Kind:       "GitHubRepository",
			Namespace:  gh.Namespace,
			Name:       gh.Name,
			UID:        gh.UID,
		},
		Reason:         reason,
		Message:        message,
		Type:           corev1.EventTypeWarning,
		Source:         corev1.EventSource{Component: "github-trigger-auth"},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	if err := k8sClient.Create(ctx, event); err != nil {
		log.Error(err, "failed to record trigger actor rejection event")
	}
}

// validateGitHubSignature checks the HMAC-SHA256 signature sent by GitHub in
// the X-Hub-Signature-256 header against the expected value derived from the
// shared webhook secret.
func validateGitHubSignature(payload []byte, signature string, secret []byte) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(signature[len("sha256="):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hmac.Equal(sig, mac.Sum(nil))
}

func (h *GitHubWebhookHandler) warnUnauthenticatedWebhook(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, accepting bool) {
	const reason = "WebhookSecretNotConfigured"
	message := "webhook secret not configured — rejecting unauthenticated webhook"
	if accepting {
		message = "webhook secret not configured — accepting unauthenticated webhook"
	}
	log := logf.FromContext(ctx).WithName("github-webhook")
	log.Error(nil, message, "namespace", gh.Namespace, "name", gh.Name)
	if h.Recorder != nil {
		h.Recorder.Event(gh, corev1.EventTypeWarning, reason, message)
	}
	if h.Client == nil {
		return
	}
	if err := retryGitHubRepositoryStatusUpdate(ctx, h.Client, client.ObjectKeyFromObject(gh), func(fresh *triggersv1alpha1.GitHubRepository) {
		apimeta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{
			Type:               triggersv1alpha1.ConditionGitHubRepositoryReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: fresh.Generation,
			Reason:             reason,
			Message:            message,
		})
	}); err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "failed to record unauthenticated webhook warning status")
	}
}

// RegisterGitHubWebhookRoutes registers the GitHub webhook endpoint on the
// given ServeMux.
func RegisterGitHubWebhookRoutes(mux *http.ServeMux, handler *GitHubWebhookHandler) {
	mux.Handle("POST /webhooks/github/{namespace}/{name}", handler)
}
