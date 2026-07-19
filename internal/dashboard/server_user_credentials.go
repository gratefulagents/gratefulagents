package dashboard

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/usercreds"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
)

const (
	// userCredentialLabel marks a Secret as one of a user's saved provider
	// credentials so the OAuth refresher can discover it independently of any
	// project that references it.
	userCredentialLabel = usercreds.LabelUserCredential
	// userCredentialProviderLabel records which provider a credential Secret holds
	// so the refresher knows how to rotate its OAuth material.
	userCredentialProviderLabel = usercreds.LabelCredentialProvider

	userCredentialSecretPrefix = "usercred-"

	// credentialGitHub is the pseudo-provider key for a saved GitHub token.
	credentialGitHub = "github"

	userCredAPIKeyKey      = "api-key"
	userCredOAuthJSONKey   = "auth.json"
	userCredAccountIDKey   = "account-id"
	userCredGithubTokenKey = "token"
)

// userCredentialSecretName returns the deterministic Secret name holding a user's
// saved credential for the given provider, within their personal namespace.
func userCredentialSecretName(provider string) string {
	return userCredentialSecretPrefix + provider
}

// userCredentialState reports which saved credentials a user has, read from their
// per-provider Secrets in their personal namespace.
type userCredentialState struct {
	anthropicAPIKey  bool
	openaiAPIKey     bool
	openrouterAPIKey bool
	xaiAPIKey        bool
	anthropicOAuth   bool
	openaiOAuth      bool
	copilotOAuth     bool
	githubToken      bool
}

func (s *Server) userCredentialState(ctx context.Context, namespace string) userCredentialState {
	anthropic := userCredentialSecretName(triggersv1alpha1.ProviderAnthropic)
	openai := userCredentialSecretName(triggersv1alpha1.ProviderOpenAI)
	openrouter := userCredentialSecretName(triggersv1alpha1.ProviderOpenRouter)
	xai := userCredentialSecretName(triggersv1alpha1.ProviderXAI)
	copilot := userCredentialSecretName(triggersv1alpha1.ProviderCopilot)
	github := userCredentialSecretName(credentialGitHub)
	return userCredentialState{
		anthropicAPIKey:  s.secretKeyPresent(ctx, namespace, anthropic, userCredAPIKeyKey),
		openaiAPIKey:     s.secretKeyPresent(ctx, namespace, openai, userCredAPIKeyKey),
		openrouterAPIKey: s.secretKeyPresent(ctx, namespace, openrouter, userCredAPIKeyKey),
		xaiAPIKey:        s.secretKeyPresent(ctx, namespace, xai, userCredAPIKeyKey),
		anthropicOAuth:   s.secretKeyPresent(ctx, namespace, anthropic, userCredOAuthJSONKey),
		openaiOAuth:      s.secretKeyPresent(ctx, namespace, openai, userCredOAuthJSONKey),
		copilotOAuth:     s.secretKeyPresent(ctx, namespace, copilot, userCredOAuthJSONKey),
		githubToken:      s.secretKeyPresent(ctx, namespace, github, userCredGithubTokenKey),
	}
}

func (st userCredentialState) toProto(namespace string) *platform.MyCredentials {
	return &platform.MyCredentials{
		Namespace:               namespace,
		AnthropicApiKeyPresent:  st.anthropicAPIKey,
		OpenaiApiKeyPresent:     st.openaiAPIKey,
		OpenrouterApiKeyPresent: st.openrouterAPIKey,
		XaiApiKeyPresent:        st.xaiAPIKey,
		AnthropicOauthPresent:   st.anthropicOAuth,
		OpenaiOauthPresent:      st.openaiOAuth,
		CopilotOauthPresent:     st.copilotOAuth,
		GithubTokenPresent:      st.githubToken,
	}
}

// ListMyCredentials returns the calling user's saved credential presence and the
// personal namespace they are stored in (provisioning it if needed).
func (s *Server) ListMyCredentials(ctx context.Context, _ *platform.ListMyCredentialsRequest) (*platform.MyCredentials, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	return s.myCredentialsProto(ctx, namespace), nil
}

// myCredentialsProto assembles the full credentials view: built-in provider
// presence plus free-form integration credentials.
func (s *Server) myCredentialsProto(ctx context.Context, namespace string) *platform.MyCredentials {
	out := s.userCredentialState(ctx, namespace).toProto(namespace)
	out.Integrations = s.integrationCredentialStates(ctx, namespace)
	out.Secrets = s.userSecretStates(ctx, namespace)
	return out
}

// userSecretStates returns reference-safe metadata for every Secret in the
// caller's personal namespace. Values never leave the API. Keeping this list
// server-side guarantees pickers cannot discover Secrets from another user's
// namespace even if a client tampers with requests.
func (s *Server) userSecretStates(ctx context.Context, namespace string) []*platform.UserSecretState {
	var secrets corev1.SecretList
	if err := s.k8sClient.List(ctx, &secrets, client.InNamespace(namespace)); err != nil {
		return nil
	}
	out := make([]*platform.UserSecretState, 0, len(secrets.Items))
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		keySet := make(map[string]struct{}, len(secret.Data)+len(secret.StringData))
		for key := range secret.Data {
			keySet[key] = struct{}{}
		}
		for key := range secret.StringData {
			keySet[key] = struct{}{}
		}
		keys := make([]string, 0, len(keySet))
		for key := range keySet {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out = append(out, &platform.UserSecretState{Name: secret.Name, Keys: keys})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// UpdateMyCredentials sets or clears the calling user's saved provider
// credentials in their personal namespace. Non-empty values are written; empty
// values are left unchanged. OAuth JSON is validated and normalized.
func (s *Server) UpdateMyCredentials(ctx context.Context, req *platform.UpdateMyCredentialsRequest) (*platform.MyCredentials, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}

	clears := map[string]bool{}
	for _, c := range req.GetClear() {
		clears[strings.ToLower(strings.TrimSpace(c))] = true
	}

	// API keys.
	if err := s.applyCredentialValue(ctx, namespace, triggersv1alpha1.ProviderAnthropic, userCredAPIKeyKey, req.GetAnthropicApiKey(), clears["anthropic-api-key"]); err != nil {
		return nil, err
	}
	if err := s.applyCredentialValue(ctx, namespace, triggersv1alpha1.ProviderOpenAI, userCredAPIKeyKey, req.GetOpenaiApiKey(), clears["openai-api-key"]); err != nil {
		return nil, err
	}
	if err := s.applyCredentialValue(ctx, namespace, triggersv1alpha1.ProviderOpenRouter, userCredAPIKeyKey, req.GetOpenrouterApiKey(), clears["openrouter-api-key"]); err != nil {
		return nil, err
	}
	if err := s.applyCredentialValue(ctx, namespace, triggersv1alpha1.ProviderXAI, userCredAPIKeyKey, req.GetXaiApiKey(), clears["xai-api-key"]); err != nil {
		return nil, err
	}
	if err := s.applyCredentialValue(ctx, namespace, credentialGitHub, userCredGithubTokenKey, req.GetGithubToken(), clears["github-token"]); err != nil {
		return nil, err
	}

	// OAuth material (validated + normalized).
	if err := s.applyCredentialOAuth(ctx, namespace, triggersv1alpha1.ProviderAnthropic, req.GetAnthropicOauthJson(), "", clears["anthropic-oauth"]); err != nil {
		return nil, err
	}
	if err := s.applyCredentialOAuth(ctx, namespace, triggersv1alpha1.ProviderOpenAI, req.GetOpenaiOauthJson(), req.GetOpenaiAccountId(), clears["openai-oauth"]); err != nil {
		return nil, err
	}
	if err := s.applyCredentialOAuth(ctx, namespace, triggersv1alpha1.ProviderCopilot, req.GetCopilotOauthJson(), "", clears["copilot-oauth"]); err != nil {
		return nil, err
	}

	// Free-form integration credentials (e.g. grafana url/token).
	for _, upd := range req.GetIntegrations() {
		if err := s.applyIntegrationCredential(ctx, namespace, upd); err != nil {
			return nil, err
		}
	}

	return s.myCredentialsProto(ctx, namespace), nil
}

// applyCredentialValue writes or clears a single key in a provider's credential
// Secret. Clearing removes the key (and the Secret if it becomes empty).
func (s *Server) applyCredentialValue(ctx context.Context, namespace, provider, key, value string, clear bool) error {
	if clear {
		return s.deleteCredentialKey(ctx, namespace, provider, key)
	}
	v := strings.TrimSpace(value)
	if v == "" {
		return nil
	}
	return s.writeCredentialData(ctx, namespace, provider, map[string][]byte{key: []byte(v)})
}

// applyCredentialOAuth writes or clears a provider's OAuth material, validating
// and normalizing the raw CLI JSON into the canonical auth.json shape.
func (s *Server) applyCredentialOAuth(ctx context.Context, namespace, provider, rawJSON, accountID string, clear bool) error {
	if clear {
		if err := s.deleteCredentialKey(ctx, namespace, provider, userCredOAuthJSONKey); err != nil {
			return err
		}
		return s.deleteCredentialKey(ctx, namespace, provider, userCredAccountIDKey)
	}
	raw := strings.TrimSpace(rawJSON)
	if raw == "" {
		return nil
	}
	normalized, err := normalizeUserOAuthJSON(provider, raw)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid %s OAuth credentials: %w", provider, err))
	}
	data := map[string][]byte{userCredOAuthJSONKey: normalized}
	if id := strings.TrimSpace(accountID); id != "" {
		data[userCredAccountIDKey] = []byte(id)
	}
	return s.writeCredentialData(ctx, namespace, provider, data)
}

// normalizeUserOAuthJSON validates raw provider OAuth material (accepting the CLI
// file shapes) and re-serializes it into the canonical auth.json form. OpenAI
// material is stored verbatim (already in auth.json shape) after a validity check.
func normalizeUserOAuthJSON(provider, raw string) ([]byte, error) {
	switch provider {
	case triggersv1alpha1.ProviderAnthropic:
		auth, err := oauth.ParseAnthropicAuthJSON([]byte(raw))
		if err != nil {
			return nil, err
		}
		return oauth.MarshalAnthropicAuthJSON(auth)
	case triggersv1alpha1.ProviderCopilot:
		auth, err := oauth.ParseCopilotAuthJSON([]byte(raw))
		if err != nil {
			return nil, err
		}
		return oauth.MarshalCopilotAuthJSON(auth)
	case triggersv1alpha1.ProviderOpenAI:
		if _, err := oauth.OpenAINeedsRefresh([]byte(raw), time.Now()); err != nil {
			return nil, err
		}
		return []byte(raw), nil
	default:
		return nil, fmt.Errorf("unsupported OAuth provider %q", provider)
	}
}

// writeCredentialData creates or updates a provider's credential Secret, merging
// the given data. The Secret carries discovery + provider labels.
func (s *Server) writeCredentialData(ctx context.Context, namespace, provider string, data map[string][]byte) error {
	name := userCredentialSecretName(provider)
	secret := &corev1.Secret{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return mapK8sError(fmt.Sprintf("read credential secret %s/%s", namespace, name), err)
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					userCredentialLabel:         "true",
					userCredentialProviderLabel: provider,
				},
			},
			Data: data,
		}
		if err := s.k8sClient.Create(ctx, secret); err != nil {
			return mapK8sError("create credential secret", err)
		}
		return nil
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	maps.Copy(secret.Data, data)
	ensureCredentialLabels(secret, provider)
	if err := s.k8sClient.Update(ctx, secret); err != nil {
		return mapK8sError("update credential secret", err)
	}
	return nil
}

// deleteCredentialKey removes a key from a provider's credential Secret, deleting
// the Secret entirely when no data remains.
func (s *Server) deleteCredentialKey(ctx context.Context, namespace, provider, key string) error {
	name := userCredentialSecretName(provider)
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return mapK8sError(fmt.Sprintf("read credential secret %s/%s", namespace, name), err)
	}
	delete(secret.Data, key)
	if len(secret.Data) == 0 {
		if err := s.k8sClient.Delete(ctx, secret); err != nil && !k8serrors.IsNotFound(err) {
			return mapK8sError("delete credential secret", err)
		}
		return nil
	}
	if err := s.k8sClient.Update(ctx, secret); err != nil {
		return mapK8sError("update credential secret", err)
	}
	return nil
}

func ensureCredentialLabels(secret *corev1.Secret, provider string) {
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels[userCredentialLabel] = "true"
	secret.Labels[userCredentialProviderLabel] = provider
}

// applyProjectSavedCredentials wires a project's Defaults to the caller's saved
// per-provider credentials (in their personal namespace) for the selected
// provider, plus their saved GitHub token. It returns an error when no saved
// credential exists for the provider/auth-mode combination.
func (s *Server) applyProjectSavedCredentials(ctx context.Context, namespace, provider string, authMode platformv1alpha1.AgentRunAuthMode, secrets *triggersv1alpha1.AgentRunSecrets) error {
	state := s.userCredentialState(ctx, namespace)

	if state.githubToken {
		secrets.GithubToken = userCredentialSecretName(credentialGitHub)
	}

	creds, err := s.resolveSavedProviderCredentials(ctx, namespace, provider, string(authMode))
	if err != nil {
		return err
	}
	secrets.OpenAIOAuthSecret = creds.oauthSecretName
	secrets.ProviderKeys = creds.providerKeys
	return nil
}

// savedProviderCredentials is the provider-credential slice of a user's saved
// credentials resolved for one provider: the effective auth mode plus either
// the OAuth secret name (OAuth) or provider API-key refs (api-key).
type savedProviderCredentials struct {
	authMode        platformv1alpha1.AgentRunAuthMode
	oauthSecretName string
	providerKeys    []platformv1alpha1.ProviderKeyRef
}

// appendAllSavedProviderCredentials mounts every saved provider credential in
// namespace onto the run's secrets, in addition to the run's own primary
// credential, so the run can switch providers mid-run without a compute
// restart: API keys for every keyed provider plus OAuth material refs for
// each OAuth-capable provider. Existing entries win; missing saved
// credentials are simply skipped.
func (s *Server) appendAllSavedProviderCredentials(ctx context.Context, namespace string, secrets *platformv1alpha1.AgentRunSecrets) {
	if secrets == nil {
		return
	}
	state := s.userCredentialState(ctx, namespace)

	hasKey := func(provider string) bool {
		for _, pk := range secrets.ProviderKeys {
			if strings.EqualFold(strings.TrimSpace(pk.Provider), provider) {
				return true
			}
		}
		return false
	}
	addKey := func(provider, keyProvider string) {
		if hasKey(provider) {
			return
		}
		secrets.ProviderKeys = append(secrets.ProviderKeys, platformv1alpha1.ProviderKeyRef{
			Provider:   provider,
			SecretName: userCredentialSecretName(keyProvider),
			SecretKey:  userCredAPIKeyKey,
		})
	}
	if state.anthropicAPIKey {
		addKey(triggersv1alpha1.ProviderAnthropic, triggersv1alpha1.ProviderAnthropic)
	}
	if state.openaiAPIKey {
		// Gemini and Groq retain the legacy OpenAI-key fallback until they gain
		// dedicated saved credential slots.
		for _, p := range []string{
			triggersv1alpha1.ProviderOpenAI,
			triggersv1alpha1.ProviderGemini,
			triggersv1alpha1.ProviderGroq,
		} {
			addKey(p, triggersv1alpha1.ProviderOpenAI)
		}
	}
	if state.openrouterAPIKey {
		addKey(triggersv1alpha1.ProviderOpenRouter, triggersv1alpha1.ProviderOpenRouter)
	}
	if state.xaiAPIKey {
		addKey(triggersv1alpha1.ProviderXAI, triggersv1alpha1.ProviderXAI)
	}

	hasOAuth := func(provider string) bool {
		for _, ref := range secrets.ProviderOAuthSecrets {
			if strings.EqualFold(strings.TrimSpace(ref.Provider), provider) {
				return true
			}
		}
		return false
	}
	addOAuth := func(provider string, saved bool) {
		if !saved || hasOAuth(provider) {
			return
		}
		secrets.ProviderOAuthSecrets = append(secrets.ProviderOAuthSecrets, platformv1alpha1.ProviderOAuthSecretRef{
			Provider:   provider,
			SecretName: userCredentialSecretName(provider),
		})
	}
	addOAuth(triggersv1alpha1.ProviderOpenAI, state.openaiOAuth)
	addOAuth(triggersv1alpha1.ProviderAnthropic, state.anthropicOAuth)
	addOAuth(triggersv1alpha1.ProviderCopilot, state.copilotOAuth)
}

// resolveSavedProviderCredentials maps a provider to the caller's saved
// per-provider credential Secrets in namespace. When authModeOverride is empty
// the auth mode is derived from which credentials exist (OAuth preferred for
// anthropic/openai; copilot is always OAuth; other providers are api-key,
// reusing the saved OpenAI API key by convention).
func (s *Server) resolveSavedProviderCredentials(ctx context.Context, namespace, provider, authModeOverride string) (savedProviderCredentials, error) {
	provider = triggersv1alpha1.NormalizeProvider(provider)
	forced := strings.TrimSpace(authModeOverride) != ""
	authMode := triggersv1alpha1.NormalizeAuthMode(authModeOverride)
	if forced {
		if err := triggersv1alpha1.ValidateProviderAuthMode(provider, authMode); err != nil {
			return savedProviderCredentials{}, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	state := s.userCredentialState(ctx, namespace)

	wantOAuth := func(saved bool) bool {
		if forced {
			return authMode == platformv1alpha1.AgentRunAuthModeOAuth
		}
		return saved
	}
	missing := func(what string) error {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no saved %s in %s; add it in Settings", what, namespace))
	}
	apiKey := func(keyProvider string) savedProviderCredentials {
		return savedProviderCredentials{
			authMode: platformv1alpha1.AgentRunAuthModeAPIKey,
			providerKeys: []platformv1alpha1.ProviderKeyRef{{
				Provider:   provider,
				SecretName: userCredentialSecretName(keyProvider),
				SecretKey:  userCredAPIKeyKey,
			}},
		}
	}

	switch provider {
	case triggersv1alpha1.ProviderCopilot:
		if !state.copilotOAuth {
			return savedProviderCredentials{}, missing("Copilot credentials")
		}
		return savedProviderCredentials{
			authMode:        platformv1alpha1.AgentRunAuthModeOAuth,
			oauthSecretName: userCredentialSecretName(triggersv1alpha1.ProviderCopilot),
		}, nil
	case triggersv1alpha1.ProviderAnthropic:
		if wantOAuth(state.anthropicOAuth) {
			if !state.anthropicOAuth {
				return savedProviderCredentials{}, missing("Anthropic OAuth credentials")
			}
			return savedProviderCredentials{
				authMode:        platformv1alpha1.AgentRunAuthModeOAuth,
				oauthSecretName: userCredentialSecretName(triggersv1alpha1.ProviderAnthropic),
			}, nil
		}
		if !state.anthropicAPIKey {
			return savedProviderCredentials{}, missing("Anthropic API key")
		}
		return apiKey(triggersv1alpha1.ProviderAnthropic), nil
	case triggersv1alpha1.ProviderOpenAI:
		if wantOAuth(state.openaiOAuth) {
			if !state.openaiOAuth {
				return savedProviderCredentials{}, missing("OpenAI OAuth credentials")
			}
			return savedProviderCredentials{
				authMode:        platformv1alpha1.AgentRunAuthModeOAuth,
				oauthSecretName: userCredentialSecretName(triggersv1alpha1.ProviderOpenAI),
			}, nil
		}
		if !state.openaiAPIKey {
			return savedProviderCredentials{}, missing("OpenAI API key")
		}
		return apiKey(triggersv1alpha1.ProviderOpenAI), nil
	case triggersv1alpha1.ProviderOpenRouter:
		if !state.openrouterAPIKey {
			return savedProviderCredentials{}, missing("OpenRouter API key")
		}
		return apiKey(triggersv1alpha1.ProviderOpenRouter), nil
	case triggersv1alpha1.ProviderXAI:
		if !state.xaiAPIKey {
			return savedProviderCredentials{}, missing("xAI API key")
		}
		return apiKey(triggersv1alpha1.ProviderXAI), nil
	default: // gemini and groq remain backed by the saved OpenAI API key.
		if !state.openaiAPIKey {
			return savedProviderCredentials{}, missing(provider + " API key")
		}
		return apiKey(triggersv1alpha1.ProviderOpenAI), nil
	}
}
