// Package oauthrefresh provides a leader-elected background goroutine that
// periodically refreshes provider OAuth tokens stored in K8s Secrets.
//
// Pods and the dashboard read tokens from Secrets but never refresh them;
// this package is the single writer that rotates tokens before they expire.
// The provider-specific token exchange and material parsing live in the SDK
// (github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth); this package
// owns only the Kubernetes Secret discovery, rotation, and stale tracking.
package oauthrefresh

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/usercreds"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
)

const (
	// refreshInterval must stay below the SDK's CopilotRefreshLead (5 min):
	// GitHub mints Copilot API tokens with a ~25–30 minute lifetime, so a
	// coarser cadence (previously 45 min) let tokens expire between ticks and
	// every Copilot consumer 401'd until the next tick. OpenAI/Anthropic
	// checks are local no-ops outside their refresh windows, so the tighter
	// loop only adds cached reads.
	refreshInterval = 4 * time.Minute

	// copilotEditorVersion and copilotUserAgent brand the operator's Copilot
	// token-exchange requests so GitHub can attribute them to this operator.
	copilotEditorVersion = "gratefulagents-operator/unknown"
	copilotUserAgent     = "gratefulagents-operator"
)

// Refresher periodically refreshes OAuth tokens in K8s Secrets.
// It implements manager.LeaderElectionRunnable so that it only runs on the
// leader replica.
type Refresher struct {
	client     client.Client
	httpClient *http.Client
	now        func() time.Time

	// mu guards staleSecrets
	mu           sync.Mutex
	staleSecrets map[oauthSecretRef]string // ref → resourceVersion when marked stale
}

// New creates a Refresher.
func New(c client.Client) *Refresher {
	return &Refresher{
		client:       c,
		httpClient:   &http.Client{Timeout: 20 * time.Second},
		now:          time.Now,
		staleSecrets: make(map[oauthSecretRef]string),
	}
}

type oauthSecretRef struct {
	types.NamespacedName
	Provider string
}

// NeedLeaderElection returns true so the manager only starts this on the leader.
func (r *Refresher) NeedLeaderElection() bool { return true }

// Start runs the refresh loop until the context is cancelled.
func (r *Refresher) Start(ctx context.Context) error {
	log.Println("[oauthrefresh] started (leader-only)")
	// Run once immediately on startup, then on interval.
	r.refreshAll(ctx)
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("[oauthrefresh] stopped")
			return nil
		case <-ticker.C:
			r.refreshAll(ctx)
		}
	}
}

// refreshAll discovers all OAuth secret refs from trigger CRDs and refreshes
// tokens that are near expiry.
func (r *Refresher) refreshAll(ctx context.Context) {
	refs, err := r.collectOAuthSecretRefs(ctx)
	if err != nil {
		log.Printf("[oauthrefresh] error collecting OAuth secret refs: %v", err)
		return
	}
	if len(refs) == 0 {
		return
	}
	log.Printf("[oauthrefresh] checking %d OAuth secret(s)", len(refs))
	for _, ref := range refs {
		if err := r.refreshSecret(ctx, ref); err != nil {
			log.Printf("[oauthrefresh] error refreshing %s OAuth secret %s/%s: %v", ref.Provider, ref.Namespace, ref.Name, err)
		}
	}
}

func (r *Refresher) refreshSecret(ctx context.Context, ref oauthSecretRef) error {
	secret := &corev1.Secret{}
	if err := r.client.Get(ctx, ref.NamespacedName, secret); err != nil {
		return fmt.Errorf("get secret: %w", err)
	}

	// Skip secrets whose refresh token was already consumed and hasn't been
	// replaced (same resourceVersion as when we marked it stale).
	r.mu.Lock()
	staleRV, isStale := r.staleSecrets[ref]
	if isStale && staleRV == secret.ResourceVersion {
		r.mu.Unlock()
		return nil
	}
	// Secret was updated externally (new resourceVersion) — clear stale mark.
	if isStale {
		delete(r.staleSecrets, ref)
	}
	r.mu.Unlock()

	authJSON, ok := secret.Data[oauth.AuthJSONKey]
	if !ok || len(authJSON) == 0 {
		return fmt.Errorf("secret missing %s key", oauth.AuthJSONKey)
	}
	switch ref.Provider {
	case triggersv1alpha1.ProviderAnthropic:
		return r.refreshAnthropicSecret(ctx, ref, secret, authJSON)
	case triggersv1alpha1.ProviderCopilot:
		return r.refreshCopilotSecret(ctx, ref, secret, authJSON)
	default:
		return r.refreshOpenAISecret(ctx, ref, secret, authJSON)
	}
}

func (r *Refresher) refreshOpenAISecret(ctx context.Context, ref oauthSecretRef, secret *corev1.Secret, authJSON []byte) error {
	accountID := strings.TrimSpace(string(secret.Data["account-id"]))
	updated, changed, err := oauth.RefreshOpenAIAuthJSON(ctx, authJSON, accountID, r.refreshConfig())
	if err != nil {
		// Mark stale if the refresh token was already consumed so we don't keep
		// retrying until the user re-logs in and updates the secret.
		if oauth.IsTerminalRefreshError(err) {
			r.markStale(ref, secret.ResourceVersion)
			log.Printf("[oauthrefresh] marking openai OAuth secret %s/%s as stale (refresh token consumed); reconnect the provider and update the secret", ref.Namespace, ref.Name)
		}
		return fmt.Errorf("refresh: %w", err)
	}
	if !changed {
		return nil
	}
	return r.writeRefreshedSecret(ctx, ref, secret, updated, "openai")
}

func (r *Refresher) refreshAnthropicSecret(ctx context.Context, ref oauthSecretRef, secret *corev1.Secret, authJSON []byte) error {
	updated, changed, err := oauth.RefreshAnthropicAuthJSON(ctx, authJSON, r.refreshConfig())
	if err != nil {
		if oauth.IsTerminalRefreshError(err) {
			r.markStale(ref, secret.ResourceVersion)
			log.Printf("[oauthrefresh] marking anthropic OAuth secret %s/%s as stale (refresh token consumed); re-run Claude OAuth login and update the secret", ref.Namespace, ref.Name)
		}
		return fmt.Errorf("refresh: %w", err)
	}
	if !changed {
		return nil
	}
	return r.writeRefreshedSecret(ctx, ref, secret, updated, "anthropic")
}

func (r *Refresher) refreshCopilotSecret(ctx context.Context, ref oauthSecretRef, secret *corev1.Secret, authJSON []byte) error {
	updated, changed, err := oauth.RefreshCopilotAuthJSON(ctx, authJSON, r.refreshConfig())
	if err != nil {
		if oauth.IsTerminalRefreshError(err) {
			r.markStale(ref, secret.ResourceVersion)
			log.Printf("[oauthrefresh] marking copilot OAuth secret %s/%s as stale; reconnect the provider and update the secret", ref.Namespace, ref.Name)
		}
		return fmt.Errorf("refresh: %w", err)
	}
	if !changed {
		return nil
	}
	return r.writeRefreshedSecret(ctx, ref, secret, updated, "copilot")
}

// writeRefreshedSecret persists refreshed auth.json back to the Secret. K8s
// optimistic concurrency (resourceVersion) handles races with other writers.
func (r *Refresher) writeRefreshedSecret(ctx context.Context, ref oauthSecretRef, secret *corev1.Secret, updated []byte, provider string) error {
	secret.Data[oauth.AuthJSONKey] = updated
	if err := r.client.Update(ctx, secret); err != nil {
		return fmt.Errorf("update secret: %w", err)
	}
	log.Printf("[oauthrefresh] refreshed %s tokens for %s/%s", provider, ref.Namespace, ref.Name)
	return nil
}

// refreshConfig builds the SDK refresh configuration shared by every provider,
// branding Copilot token requests for this operator.
func (r *Refresher) refreshConfig() oauth.RefreshConfig {
	return oauth.RefreshConfig{
		HTTPClient:                 r.httpClient,
		Now:                        r.now,
		CopilotEditorVersion:       copilotEditorVersion,
		CopilotEditorPluginVersion: copilotEditorVersion,
		CopilotUserAgent:           copilotUserAgent,
		CopilotAuthorizationScheme: "token",
	}
}

func (r *Refresher) markStale(ref oauthSecretRef, resourceVersion string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.staleSecrets[ref] = resourceVersion
}

// collectOAuthSecretRefs scans for unique OAuth secret references to refresh.
// It collects secrets referenced by LinearProject and Project defaults, and also
// the per-user saved-credential Secrets (discovered by label) so a user's
// imported OAuth credentials are rotated even before any project references them.
func (r *Refresher) collectOAuthSecretRefs(ctx context.Context) ([]oauthSecretRef, error) {
	seen := map[oauthSecretRef]struct{}{}

	collect := func(namespace string, d *triggersv1alpha1.AgentRunDefaults) {
		provider := triggersv1alpha1.NormalizeProvider(d.Provider)
		authMode := triggersv1alpha1.NormalizeAuthMode(string(d.AuthMode))
		if !triggersv1alpha1.RequiresOpenAIOAuthSecret(provider, authMode) {
			return
		}
		secretName := strings.TrimSpace(d.Secrets.OpenAIOAuthSecret)
		if secretName == "" {
			return
		}
		seen[oauthSecretRef{
			NamespacedName: types.NamespacedName{Namespace: namespace, Name: secretName},
			Provider:       provider,
		}] = struct{}{}
	}

	var lps triggersv1alpha1.LinearProjectList
	if err := r.client.List(ctx, &lps); err != nil {
		return nil, fmt.Errorf("list LinearProjects: %w", err)
	}
	for i := range lps.Items {
		collect(lps.Items[i].Namespace, &lps.Items[i].Spec.Defaults)
	}

	var projects triggersv1alpha1.ProjectList
	if err := r.client.List(ctx, &projects); err != nil {
		return nil, fmt.Errorf("list Projects: %w", err)
	}
	for i := range projects.Items {
		collect(projects.Items[i].Namespace, &projects.Items[i].Spec.Defaults)
	}

	if err := r.collectUserCredentialSecretRefs(ctx, seen); err != nil {
		return nil, err
	}

	refs := make([]oauthSecretRef, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	return refs, nil
}

// collectUserCredentialSecretRefs adds per-user saved-credential Secrets (labeled
// by the dashboard) that hold OAuth material to the ref set. API-key-only and
// GitHub-token Secrets carry no auth.json and are skipped.
func (r *Refresher) collectUserCredentialSecretRefs(ctx context.Context, seen map[oauthSecretRef]struct{}) error {
	var secrets corev1.SecretList
	if err := r.client.List(ctx, &secrets, client.MatchingLabels{usercreds.LabelUserCredential: "true"}); err != nil {
		return fmt.Errorf("list user credential secrets: %w", err)
	}
	for i := range secrets.Items {
		sec := &secrets.Items[i]
		provider := strings.TrimSpace(sec.Labels[usercreds.LabelCredentialProvider])
		if provider == "" {
			continue
		}
		if v, ok := sec.Data[oauth.AuthJSONKey]; !ok || len(v) == 0 {
			continue
		}
		seen[oauthSecretRef{
			NamespacedName: types.NamespacedName{Namespace: sec.Namespace, Name: sec.Name},
			Provider:       triggersv1alpha1.NormalizeProvider(provider),
		}] = struct{}{}
	}
	return nil
}
