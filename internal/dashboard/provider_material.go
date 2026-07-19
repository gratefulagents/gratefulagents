package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/usercreds"
)

// oauthMaterialProvider identifies which provider a credential Secret's OAuth
// material belongs to, so provider/secret wiring mismatches (e.g. a project
// switched to anthropic while still pointing at a Copilot OAuth secret) can be
// caught at configuration time instead of crashing the agent pod at startup.
//
// Detection order: the credential-provider label written by the dashboard,
// the auth.json "type" field, then unambiguous shape markers. Returns "" when
// the provider cannot be determined; callers must treat "" as "unknown, allow".
func oauthMaterialProvider(secret *corev1.Secret) string {
	if secret == nil {
		return ""
	}
	// Match the label against known providers directly: NormalizeProvider
	// coerces unknown/empty values to openai, which would misclassify every
	// unlabeled secret.
	if label := strings.ToLower(strings.TrimSpace(secret.Labels[usercreds.LabelCredentialProvider])); isOAuthCapableProvider(label) {
		return label
	}
	authJSON, ok := secret.Data[userCredOAuthJSONKey]
	if !ok {
		return ""
	}
	return oauthMaterialProviderFromJSON(authJSON)
}

// oauthMaterialProviderFromJSON sniffs an auth.json document for markers that
// unambiguously identify its provider. Ambiguous shapes return "".
func oauthMaterialProviderFromJSON(authJSON []byte) string {
	var root map[string]any
	if err := json.Unmarshal(authJSON, &root); err != nil {
		return ""
	}
	// Explicit type marker written by the SDK's canonical marshallers.
	if typ, _ := root["type"].(string); typ != "" {
		switch strings.ToLower(strings.TrimSpace(typ)) {
		case "copilot":
			return triggersv1alpha1.ProviderCopilot
		case "claude", "anthropic":
			return triggersv1alpha1.ProviderAnthropic
		case "openai", "chatgpt", "codex":
			return triggersv1alpha1.ProviderOpenAI
		}
	}
	// Anthropic OAuth credential file shape.
	if _, ok := root["claudeAiOauth"]; ok {
		return triggersv1alpha1.ProviderAnthropic
	}
	// Copilot material carries a GitHub OAuth token (flat SDK shape) or
	// github.com host keys (apps.json/hosts.json shape).
	if hasNonEmptyString(root, "oauth_token") || hasNonEmptyString(root, "oauthToken") || hasNonEmptyString(root, "github_oauth_token") {
		return triggersv1alpha1.ProviderCopilot
	}
	for key := range root {
		if key == "github.com" || strings.HasPrefix(key, "github.com:") {
			return triggersv1alpha1.ProviderCopilot
		}
	}
	// The OpenAI OAuth auth.json nests tokens and carries an id_token.
	if tokens, ok := root["tokens"].(map[string]any); ok {
		if hasNonEmptyString(tokens, "id_token") || hasNonEmptyString(tokens, "account_id") {
			return triggersv1alpha1.ProviderOpenAI
		}
	}
	// Flat access/refresh tokens without any Copilot/OpenAI markers is the
	// Anthropic auth2api shape.
	if hasNonEmptyString(root, "refresh_token") || hasNonEmptyString(root, "refreshToken") {
		return triggersv1alpha1.ProviderAnthropic
	}
	return ""
}

func hasNonEmptyString(m map[string]any, key string) bool {
	value, _ := m[key].(string)
	return strings.TrimSpace(value) != ""
}

func isOAuthCapableProvider(provider string) bool {
	switch provider {
	case triggersv1alpha1.ProviderOpenAI, triggersv1alpha1.ProviderAnthropic, triggersv1alpha1.ProviderCopilot:
		return true
	default:
		return false
	}
}

// validateOAuthSecretProvider rejects OAuth secret references whose stored
// material demonstrably belongs to a different provider than the one selected.
// Missing secrets and undetermined material are allowed (references may be
// created later, and custom shapes are none of our business); only a provable
// mismatch fails, with an actionable message instead of the opaque agent-pod
// startup crash it would otherwise cause.
func (s *Server) validateOAuthSecretProvider(ctx context.Context, namespace, secretName, provider string) error {
	secretName = strings.TrimSpace(secretName)
	if secretName == "" {
		return nil
	}
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		// Best-effort guard: an unreadable secret must not block saving; a
		// genuinely missing/misconfigured reference surfaces elsewhere.
		if !k8serrors.IsNotFound(err) {
			log.Printf("WARN: skipping OAuth material check for secret %s/%s: %v", namespace, secretName, err)
		}
		return nil
	}
	material := oauthMaterialProvider(secret)
	if material == "" || strings.EqualFold(material, provider) {
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
		"OAuth secret %q holds %s OAuth material, but the selected provider is %s; pick the %s OAuth secret (e.g. %s) or enable \"Use my saved provider credentials\"",
		secretName, material, provider, provider, userCredentialSecretName(provider)))
}

// healProjectOAuthSecretForProvider resolves provider/OAuth-secret mismatches
// in a project's defaults, typically left by a provider switch that carried
// the old provider's saved-credential secret along. When the mismatched
// secret is the deterministic saved-credential Secret of the *material's*
// provider (usercred-<material>), the reference is provably carry-over rather
// than a deliberate choice, so it is repointed to the caller's saved
// credentials for the selected provider — flipping the auth mode to api-key
// when that is what they have saved. Any other mismatch is rejected with the
// actionable validation error. Returns the (possibly updated) auth mode.
func (s *Server) healProjectOAuthSecretForProvider(ctx context.Context, namespace, provider string, authMode platformv1alpha1.AgentRunAuthMode, secrets *triggersv1alpha1.AgentRunSecrets) (platformv1alpha1.AgentRunAuthMode, error) {
	if authMode != platformv1alpha1.AgentRunAuthModeOAuth || secrets == nil {
		return authMode, nil
	}
	secretName := strings.TrimSpace(secrets.OpenAIOAuthSecret)
	if secretName == "" {
		return authMode, nil
	}
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		// Best-effort guard: an unreadable secret must not block saving; a
		// genuinely missing/misconfigured reference surfaces elsewhere.
		if !k8serrors.IsNotFound(err) {
			log.Printf("WARN: skipping OAuth material check for project secret %s/%s: %v", namespace, secretName, err)
		}
		return authMode, nil
	}
	material := oauthMaterialProvider(secret)
	if material == "" || strings.EqualFold(material, provider) {
		return authMode, nil
	}
	if secretName != userCredentialSecretName(material) {
		// A custom-named secret was chosen explicitly; make the user fix it.
		return authMode, s.validateOAuthSecretProvider(ctx, namespace, secretName, provider)
	}
	creds, err := s.resolveSavedProviderCredentials(ctx, namespace, provider, "")
	if err != nil {
		// No saved credential for the new provider to repoint to: surface the
		// mismatch instead of silently persisting wiring that crashes runs.
		return authMode, s.validateOAuthSecretProvider(ctx, namespace, secretName, provider)
	}
	log.Printf("INFO: repointing project OAuth credentials in %s: secret %q holds %s material but provider is %s (auth mode %s)",
		namespace, secretName, material, provider, creds.authMode)
	secrets.OpenAIOAuthSecret = creds.oauthSecretName
	secrets.ProviderKeys = mergeProviderKeys(secrets.ProviderKeys, creds.providerKeys)
	return creds.authMode, nil
}

// repairRunOAuthSecretForProvider self-heals a new run's OAuth wiring when the
// effective OAuth secret provably holds another provider's material (drift
// left behind by earlier provider switches on the source project). Inherited
// mismatches are repointed to the caller's saved credentials for the run's
// provider; explicit request-level mismatches are rejected instead, since the
// caller asked for that exact secret. Returns the (possibly updated) auth mode.
func (s *Server) repairRunOAuthSecretForProvider(ctx context.Context, namespace, provider string, authMode platformv1alpha1.AgentRunAuthMode, secrets *platformv1alpha1.AgentRunSecrets, explicitSecret bool) (platformv1alpha1.AgentRunAuthMode, error) {
	if authMode != platformv1alpha1.AgentRunAuthModeOAuth || secrets == nil {
		return authMode, nil
	}
	secretName := strings.TrimSpace(secrets.OpenAIOAuthSecret)
	if secretName == "" {
		return authMode, nil
	}
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		// Opportunistic self-heal only: never block run creation because the
		// secret could not be read here.
		if !k8serrors.IsNotFound(err) {
			log.Printf("WARN: skipping OAuth material check for run secret %s/%s: %v", namespace, secretName, err)
		}
		return authMode, nil
	}
	material := oauthMaterialProvider(secret)
	if material == "" || strings.EqualFold(material, provider) {
		return authMode, nil
	}
	if explicitSecret {
		return authMode, s.validateOAuthSecretProvider(ctx, namespace, secretName, provider)
	}
	creds, err := s.resolveSavedProviderCredentials(ctx, namespace, provider, "")
	if err != nil {
		return authMode, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"the project's OAuth secret %q holds %s OAuth material but the run provider is %s, and no saved %s credential is available to repoint to; fix the project's Provider/OAuth Secret settings or save a %s credential",
			secretName, material, provider, provider, provider))
	}
	log.Printf("INFO: repointing run OAuth credentials in %s: secret %q holds %s material but provider is %s (auth mode %s)",
		namespace, secretName, material, provider, creds.authMode)
	secrets.OpenAIOAuthSecret = creds.oauthSecretName
	secrets.ProviderKeys = mergeProviderKeys(secrets.ProviderKeys, creds.providerKeys)
	return creds.authMode, nil
}
