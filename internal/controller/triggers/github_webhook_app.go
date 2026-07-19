package triggers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// GitHubAppWebhookHandler is the single app-level webhook endpoint. A GitHub
// App has exactly one webhook URL for every repository it is installed on, so
// this handler routes each delivery by the payload's repository.full_name to
// every GitHubRepository CR (across namespaces) that watches that repository,
// then reuses the per-CR dispatch pipeline. Pull-request events with no matching
// CR still reach the PR loop, which resolves an AgentRun from repository URL.
type GitHubAppWebhookHandler struct {
	// Inner carries the k8s client, reconciler, PR sink, recorder and the
	// delivery deduper shared with per-CR dispatch.
	Inner *GitHubWebhookHandler
	// WebhookSecretNamespace/WebhookSecretName locate the K8s Secret holding
	// the GitHub App's webhook secret under the key "secret".
	WebhookSecretNamespace string
	WebhookSecretName      string
	// AllowUnauthenticated permits unsigned deliveries when WebhookSecretName
	// is empty. The default is fail-closed.
	AllowUnauthenticated bool
}

// githubRepositoryEnvelope decodes only the routing fields shared by all
// repository-scoped webhook payloads.
type githubRepositoryEnvelope struct {
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (h *GitHubAppWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logf.FromContext(ctx).WithName("github-app-webhook")

	inner := h.Inner
	if inner == nil || inner.Reconciler == nil {
		http.Error(w, "webhook handler not configured", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Validate the app-level webhook secret. App deliveries are signed once
	// with the App's secret; per-CR spec.webhookSecret does not apply here.
	if h.WebhookSecretName != "" {
		secret := &corev1.Secret{}
		if err := inner.Client.Get(ctx, types.NamespacedName{
			Namespace: h.WebhookSecretNamespace,
			Name:      h.WebhookSecretName,
		}, secret); err != nil {
			log.Error(err, "failed to read app webhook secret", "secret", h.WebhookSecretName)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		secretValue, ok := secret.Data["secret"]
		if !ok {
			log.Error(nil, "app webhook secret missing 'secret' key", "secret", h.WebhookSecretName)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !validateGitHubSignature(body, r.Header.Get("X-Hub-Signature-256"), secretValue) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	} else {
		if !h.AllowUnauthenticated {
			log.Error(nil, "GITHUB_APP_WEBHOOK_SECRET not configured — rejecting unauthenticated app webhook",
				"namespace", h.WebhookSecretNamespace, "secretEnv", "GITHUB_APP_WEBHOOK_SECRET")
			http.Error(w, "webhook secret not configured", http.StatusUnauthorized)
			return
		}
		log.Error(nil, "GITHUB_APP_WEBHOOK_SECRET not configured — accepting unauthenticated app webhook",
			"namespace", h.WebhookSecretNamespace, "secretEnv", "GITHUB_APP_WEBHOOK_SECRET")
	}

	inner.dedupOnce.Do(func() { inner.dedup = newDeliveryDeduper() })
	if inner.dedup.isDuplicate(r.Header.Get("X-GitHub-Delivery")) {
		w.WriteHeader(http.StatusOK)
		return
	}

	var envelope githubRepositoryEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	fullName := strings.TrimSpace(envelope.Repository.FullName)
	if fullName == "" {
		// Non-repository events (installation lifecycle, ping, …) are fine.
		w.WriteHeader(http.StatusOK)
		return
	}

	matches, err := h.matchingRepositories(r, fullName)
	if err != nil {
		log.Error(err, "failed to list GitHubRepositories", "repository", fullName)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	eventType := r.Header.Get("X-GitHub-Event")
	if len(matches) == 0 {
		// Trigger-independent PR loop fallback. Non-PR events still require a
		// GitHubRepository because issue triggering needs repository policy.
		if isPullRequestWebhookEvent(eventType) && inner.PRSink != nil {
			writeWebhookResult(w, inner.dispatchEvent(ctx, nil, eventType, body))
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	var dispatchErr error
	for i := range matches {
		gh := &matches[i]
		if err := inner.dispatchEvent(ctx, gh, eventType, body); err != nil {
			log.Error(err, "app webhook dispatch failed",
				"repository", fullName, "namespace", gh.Namespace, "name", gh.Name, "event", eventType)
			dispatchErr = err
		}
	}
	writeWebhookResult(w, dispatchErr)
}

func isPullRequestWebhookEvent(eventType string) bool {
	switch eventType {
	case "pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment":
		return true
	default:
		return false
	}
}

// matchingRepositories returns every GitHubRepository CR whose owner/repo
// matches the delivery's repository full name (case-insensitive, GitHub
// treats both as case-insensitive).
func (h *GitHubAppWebhookHandler) matchingRepositories(r *http.Request, fullName string) ([]triggersv1alpha1.GitHubRepository, error) {
	list := &triggersv1alpha1.GitHubRepositoryList{}
	if err := h.Inner.Client.List(r.Context(), list); err != nil {
		return nil, err
	}
	var matches []triggersv1alpha1.GitHubRepository
	for _, gh := range list.Items {
		crFullName := strings.TrimSpace(gh.Spec.Owner) + "/" + strings.TrimSpace(gh.Spec.Repo)
		if strings.EqualFold(crFullName, fullName) {
			matches = append(matches, gh)
		}
	}
	return matches, nil
}

// RegisterGitHubAppWebhookRoutes registers the app-level webhook endpoint.
func RegisterGitHubAppWebhookRoutes(mux *http.ServeMux, handler *GitHubAppWebhookHandler) {
	mux.Handle("POST /webhooks/github/app", handler)
}
