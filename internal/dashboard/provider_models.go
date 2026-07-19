package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
	openai "github.com/gratefulagents/sdk/pkg/agentsdk/providers/openai"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	anthropicModelsBaseURL = "https://api.anthropic.com/v1"
	anthropicModelsPath    = "/models"
	anthropicVersion       = "2023-06-01"

	copilotChatVersion      = "0.35.0"
	copilotEditorVersion    = "vscode/1.107.0"
	copilotGitHubAPIVersion = "2026-06-01"
)

var providerModelHTTPClient = &http.Client{Timeout: 20 * time.Second}

var copilotModelOAuthRefreshConfig = func() oauth.RefreshConfig {
	return oauth.RefreshConfig{
		HTTPClient:                 providerModelHTTPClient,
		CopilotEditorVersion:       "gratefulagents-operator/unknown",
		CopilotEditorPluginVersion: "gratefulagents-operator/unknown",
		CopilotUserAgent:           "gratefulagents-operator",
		CopilotAuthorizationScheme: "token",
	}
}

// modelQuery describes the credentials and provider configuration needed to list
// models for ListAvailableModels (project-scoped credentials).
type modelQuery struct {
	namespace        string
	provider         string
	authMode         platformv1alpha1.AgentRunAuthMode
	apiKeySecretName string // secret holding the anthropic/openai api key
	apiKeySecretKey  string // key within apiKeySecretName
	oauthSecretName  string // secret holding provider OAuth material
	openAIBaseURL    string // resolved OpenAI-compatible base URL
	allowedModels    []string
}

// fetchProviderModels resolves the provider's model list using the credentials
// described by q. It returns the resolved base URL and the (optionally filtered)
// model IDs.
func (s *Server) fetchProviderModels(ctx context.Context, q modelQuery) (string, []string, error) {
	var (
		models  []string
		baseURL string
	)
	if q.provider == triggersv1alpha1.ProviderAnthropic {
		headers, err := s.anthropicModelHeaders(ctx, q)
		if err != nil {
			return "", nil, err
		}
		models, err = fetchAnthropicModels(ctx, headers)
		if err != nil {
			return "", nil, connect.NewError(connect.CodeUnavailable, err)
		}
		baseURL = anthropicModelsBaseURL
	} else {
		var authSession *openai.OpenAIAuthSession
		if q.provider == triggersv1alpha1.ProviderCopilot {
			var err error
			authSession, err = s.copilotModelAuthSession(ctx, q)
			if err != nil {
				return "", nil, err
			}
		} else if triggersv1alpha1.RequiresOpenAIOAuthSecret(q.provider, q.authMode) {
			authSecret, err := s.readSecret(ctx, q.namespace, q.oauthSecretName)
			if err != nil {
				return "", nil, mapK8sError(fmt.Sprintf("read OAuth secret %s/%s", q.namespace, q.oauthSecretName), err)
			}
			authJSON, ok := authSecret.Data["auth.json"]
			if !ok {
				return "", nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("secret %s/%s missing key %q", q.namespace, q.oauthSecretName, "auth.json"))
			}
			accountID := strings.TrimSpace(string(authSecret.Data["account-id"]))
			authSession, err = openai.NewOAuthAuthSessionFromSecretData(authJSON, accountID)
			if err != nil {
				return "", nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("invalid OAuth material in secret %s/%s: %w", q.namespace, q.oauthSecretName, err))
			}
			authSession.DisableRefresh() // refresh is handled centrally by the controller
		} else {
			apiKey, err := s.readRequiredSecretValue(ctx, q.namespace, q.apiKeySecretName, q.apiKeySecretKey)
			if err != nil {
				return "", nil, err
			}
			authSession = openai.NewAPIKeyAuthSession(apiKey)
		}

		var err error
		baseURL = normalizeOpenAIModelsBaseURL(q.provider, q.openAIBaseURL)
		models, err = fetchOpenAICompatibleModels(ctx, baseURL, authSession)
		if err != nil {
			return "", nil, connect.NewError(connect.CodeUnavailable, err)
		}
	}
	if len(q.allowedModels) > 0 {
		allowed := make(map[string]bool, len(q.allowedModels))
		for _, m := range q.allowedModels {
			allowed[m] = true
		}
		filtered := models[:0]
		for _, m := range models {
			if allowed[m] {
				filtered = append(filtered, m)
			}
		}
		models = filtered
	}
	return baseURL, models, nil
}

func (s *Server) ListAvailableModels(ctx context.Context, req *platform.ListAvailableModelsRequest) (*platform.ListAvailableModelsResponse, error) {
	// Resolve and authorize the namespace whose credential Secrets back the
	// model listing: regular users may not point this RPC at another user's
	// personal namespace (their saved provider credentials live there).
	namespace, err := s.authorizeRequestNamespace(ctx, req.Namespace, req.Source)
	if err != nil {
		return nil, err
	}
	if req.Source == nil {
		if hasModelCredentialRefs(req) {
			return s.listAvailableModelsForCredentialRefs(ctx, namespace, req)
		}
		return s.listAvailableModelsForSavedCredential(ctx, namespace, req)
	}
	if namespace == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace is required"))
	}
	if strings.TrimSpace(req.Source.Kind) == "" || strings.TrimSpace(req.Source.Name) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source kind and name are required"))
	}

	defaults, _, err := s.resolveSourceDefaults(ctx, namespace, req.Source)
	if err != nil {
		return nil, err
	}

	provider, err := resolveProvider(req.Provider, defaults.Provider)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var query modelQuery
	if provider == triggersv1alpha1.NormalizeProvider(defaults.Provider) { //nolint:staticcheck // defaults.Provider identifies which provider the stored credentials belong to
		authMode := triggersv1alpha1.NormalizeAuthMode(string(defaults.AuthMode))
		if err := validateProviderAuthConfiguration(provider, authMode, defaults.Secrets.ClaudeApiKey, defaults.Secrets.OpenAIOAuthSecret, defaults.Secrets.ProviderKeys); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		query = modelQueryForSecrets(namespace, provider, authMode, defaults.Secrets.ClaudeApiKey, defaults.Secrets.OpenAIOAuthSecret, defaults.Secrets.ProviderKeys)
		query.openAIBaseURL = triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, defaults.OpenAIBaseURL, authMode)
		query.allowedModels = defaults.AllowedModels
	} else {
		// The source's stored credentials (and base URL / model allow-list)
		// belong to its own provider; resolve the requested provider from the
		// source's per-provider keys or the caller's saved credentials instead.
		query, err = s.savedCredentialModelQuery(ctx, namespace, provider, "", defaults.Secrets.ProviderKeys)
		if err != nil {
			return nil, err
		}
	}
	baseURL, models, err := s.fetchProviderModels(ctx, query)
	if err != nil {
		return nil, err
	}

	return &platform.ListAvailableModelsResponse{
		Provider: provider,
		BaseUrl:  baseURL,
		Models:   models,
	}, nil
}

// hasModelCredentialRefs reports whether the request carries explicit
// credential secret refs. AuthMode alone does not count: a request with only
// namespace+provider+auth_mode still lists models from the caller's saved
// credentials, with auth_mode as a preference.
func hasModelCredentialRefs(req *platform.ListAvailableModelsRequest) bool {
	return strings.TrimSpace(req.GetClaudeApiKeySecret()) != "" ||
		strings.TrimSpace(req.GetOpenaiOauthSecret()) != "" ||
		len(req.GetProviderKeys()) > 0
}

func (s *Server) listAvailableModelsForCredentialRefs(ctx context.Context, namespace string, req *platform.ListAvailableModelsRequest) (*platform.ListAvailableModelsResponse, error) {
	if namespace == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace is required"))
	}
	provider, err := resolveProvider(req.Provider, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	authMode := triggersv1alpha1.NormalizeAuthMode(req.GetAuthMode())
	if provider == triggersv1alpha1.ProviderCopilot {
		authMode = platformv1alpha1.AgentRunAuthModeOAuth
	}
	providerKeys := providerKeysFromProto(req.GetProviderKeys())
	claudeSecretName := strings.TrimSpace(req.GetClaudeApiKeySecret())
	oauthSecretName := strings.TrimSpace(req.GetOpenaiOauthSecret())
	if err := validateProviderAuthConfiguration(provider, authMode, claudeSecretName, oauthSecretName, providerKeys); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	query := modelQueryForSecrets(namespace, provider, authMode, claudeSecretName, oauthSecretName, providerKeys)
	query.openAIBaseURL = triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, req.GetOpenaiBaseUrl(), authMode)
	query.allowedModels = append([]string(nil), req.GetAllowedModels()...)
	baseURL, models, err := s.fetchProviderModels(ctx, query)
	if err != nil {
		return nil, err
	}

	return &platform.ListAvailableModelsResponse{
		Provider: provider,
		BaseUrl:  baseURL,
		Models:   models,
	}, nil
}

func modelQueryForSecrets(namespace, provider string, authMode platformv1alpha1.AgentRunAuthMode, apiKeySecret, oauthSecret string, providerKeys []platformv1alpha1.ProviderKeyRef) modelQuery {
	query := modelQuery{
		namespace:       namespace,
		provider:        provider,
		authMode:        authMode,
		oauthSecretName: strings.TrimSpace(oauthSecret),
	}
	if triggersv1alpha1.RequiresOpenAIOAuthSecret(provider, authMode) {
		return query
	}
	if secretName, secretKey, ok := providerKeyFor(providerKeys, provider); ok {
		query.apiKeySecretName = secretName
		query.apiKeySecretKey = secretKey
		return query
	}
	query.apiKeySecretName = strings.TrimSpace(apiKeySecret)
	query.apiKeySecretKey = "api-key"
	return query
}

// providerKeyFor returns the API-key Secret reference for provider from a
// providerKeys list, defaulting the key name to "api-key".
func providerKeyFor(keys []platformv1alpha1.ProviderKeyRef, provider string) (secretName, secretKey string, ok bool) {
	for _, key := range keys {
		if !strings.EqualFold(strings.TrimSpace(key.Provider), provider) || strings.TrimSpace(key.SecretName) == "" {
			continue
		}
		secretKey = strings.TrimSpace(key.SecretKey)
		if secretKey == "" {
			secretKey = userCredAPIKeyKey
		}
		return strings.TrimSpace(key.SecretName), secretKey, true
	}
	return "", "", false
}

// savedCredentialModelQuery builds a modelQuery for provider backed by the
// caller's saved credentials in namespace. A per-provider API key from
// projectKeys (e.g. a source's providerKeys) takes precedence when present.
// An authModeOverride expresses a preference: when the caller has no saved
// credential of that kind, resolution falls back to whatever saved credential
// exists (OAuth preferred) instead of failing.
func (s *Server) savedCredentialModelQuery(ctx context.Context, namespace, provider, authModeOverride string, projectKeys []platformv1alpha1.ProviderKeyRef) (modelQuery, error) {
	query := modelQuery{namespace: namespace, provider: provider}
	if secretName, secretKey, ok := providerKeyFor(projectKeys, provider); ok {
		query.authMode = platformv1alpha1.AgentRunAuthModeAPIKey
		query.apiKeySecretName = secretName
		query.apiKeySecretKey = secretKey
	} else {
		creds, err := s.resolveSavedProviderCredentials(ctx, namespace, provider, authModeOverride)
		if err != nil && strings.TrimSpace(authModeOverride) != "" && connect.CodeOf(err) == connect.CodeFailedPrecondition {
			creds, err = s.resolveSavedProviderCredentials(ctx, namespace, provider, "")
		}
		if err != nil {
			return modelQuery{}, err
		}
		query.authMode = creds.authMode
		query.oauthSecretName = creds.oauthSecretName
		if len(creds.providerKeys) > 0 {
			query.apiKeySecretName = creds.providerKeys[0].SecretName
			query.apiKeySecretKey = creds.providerKeys[0].SecretKey
		}
	}
	query.openAIBaseURL = triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, "", query.authMode)
	return query, nil
}

// listAvailableModelsForSavedCredential lists models for a provider using the
// caller's saved per-provider credentials (usercred-<provider> Secrets) in
// their personal namespace.
func (s *Server) listAvailableModelsForSavedCredential(ctx context.Context, namespace string, req *platform.ListAvailableModelsRequest) (*platform.ListAvailableModelsResponse, error) {
	provider, err := resolveProvider(req.Provider, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if namespace == "" {
		actor := requestActorFromContext(ctx)
		namespace, err = s.ensureUserNamespace(ctx, actor)
		if err != nil {
			return nil, err
		}
	}

	query, err := s.savedCredentialModelQuery(ctx, namespace, provider, req.GetAuthMode(), nil)
	if err != nil {
		return nil, err
	}
	baseURL, models, err := s.fetchProviderModels(ctx, query)
	if err != nil {
		return nil, err
	}

	return &platform.ListAvailableModelsResponse{
		Provider: provider,
		BaseUrl:  baseURL,
		Models:   models,
	}, nil
}

func (s *Server) anthropicModelHeaders(ctx context.Context, q modelQuery) (map[string]string, error) {
	if triggersv1alpha1.RequiresOpenAIOAuthSecret(q.provider, q.authMode) {
		authSecret, err := s.readSecret(ctx, q.namespace, q.oauthSecretName)
		if err != nil {
			return nil, mapK8sError(fmt.Sprintf("read OAuth secret %s/%s", q.namespace, q.oauthSecretName), err)
		}
		authJSON, ok := authSecret.Data[oauth.AuthJSONKey]
		if !ok {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("secret %s/%s missing key %q", q.namespace, q.oauthSecretName, oauth.AuthJSONKey))
		}
		auth, err := oauth.ParseAnthropicAuthJSON(authJSON)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("invalid OAuth material in secret %s/%s: %w", q.namespace, q.oauthSecretName, err))
		}
		if strings.TrimSpace(auth.AccessToken) == "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("OAuth material in secret %s/%s is missing access token", q.namespace, q.oauthSecretName))
		}
		return map[string]string{
			"Authorization":     "Bearer " + strings.TrimSpace(auth.AccessToken),
			"anthropic-beta":    "oauth-2025-04-20",
			"anthropic-version": anthropicVersion,
		}, nil
	}
	apiKey, err := s.readRequiredSecretValue(ctx, q.namespace, q.apiKeySecretName, q.apiKeySecretKey)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"x-api-key":         strings.TrimSpace(apiKey),
		"anthropic-version": anthropicVersion,
	}, nil
}

func (s *Server) copilotModelAuthSession(ctx context.Context, q modelQuery) (*openai.OpenAIAuthSession, error) {
	var token string
	if triggersv1alpha1.RequiresOpenAIOAuthSecret(q.provider, q.authMode) {
		var err error
		token, err = s.copilotAPITokenFromOAuthSecret(ctx, q)
		if err != nil {
			return nil, err
		}
	} else {
		apiKey, err := s.readRequiredSecretValue(ctx, q.namespace, q.apiKeySecretName, q.apiKeySecretKey)
		if err != nil {
			return nil, err
		}
		token = normalizeCopilotBearerToken(apiKey)
	}
	return openai.NewCustomAuthSession(openai.CustomAuthSessionConfig{
		SDKAPIKey: "copilot-placeholder",
		RequestHeaders: func(context.Context) (map[string]string, error) {
			if token == "" {
				return nil, fmt.Errorf("Copilot API token is required")
			}
			return copilotModelHeaders(token), nil
		},
	}), nil
}

func (s *Server) copilotAPITokenFromOAuthSecret(ctx context.Context, q modelQuery) (string, error) {
	authSecret, err := s.readSecret(ctx, q.namespace, q.oauthSecretName)
	if err != nil {
		return "", mapK8sError(fmt.Sprintf("read OAuth secret %s/%s", q.namespace, q.oauthSecretName), err)
	}
	authJSON, ok := authSecret.Data[oauth.AuthJSONKey]
	if !ok {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("secret %s/%s missing key %q", q.namespace, q.oauthSecretName, oauth.AuthJSONKey))
	}

	refreshedJSON, changed, err := oauth.RefreshCopilotAuthJSON(ctx, authJSON, copilotModelOAuthRefreshConfig())
	if err != nil {
		code := connect.CodeUnavailable
		if oauth.IsTerminalRefreshError(err) {
			code = connect.CodeFailedPrecondition
		}
		return "", connect.NewError(code, fmt.Errorf("refresh Copilot OAuth material in secret %s/%s: %w", q.namespace, q.oauthSecretName, err))
	}
	if changed {
		authSecret.Data[oauth.AuthJSONKey] = refreshedJSON
		if err := s.k8sClient.Update(ctx, authSecret); err != nil {
			return "", mapK8sError(fmt.Sprintf("update refreshed Copilot OAuth secret %s/%s", q.namespace, q.oauthSecretName), err)
		}
		authJSON = refreshedJSON
	}

	token, err := oauth.CopilotAPIToken(authJSON)
	if err != nil {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("OAuth material in secret %s/%s is missing a usable Copilot API token: %w", q.namespace, q.oauthSecretName, err))
	}
	token = normalizeCopilotBearerToken(token)
	if token == "" {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("OAuth material in secret %s/%s is missing a usable Copilot API token", q.namespace, q.oauthSecretName))
	}
	return token, nil
}

func copilotModelHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization":          "Bearer " + normalizeCopilotBearerToken(token),
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Version":         copilotEditorVersion,
		"Editor-Plugin-Version":  "copilot-chat/" + copilotChatVersion,
		"User-Agent":             "GitHubCopilotChat/" + copilotChatVersion,
		"Openai-Intent":          "conversation-edits",
		"X-GitHub-Api-Version":   copilotGitHubAPIVersion,
		"X-Initiator":            "user",
	}
}

func normalizeCopilotBearerToken(token string) string {
	token = strings.TrimSpace(token)
	for {
		lower := strings.ToLower(token)
		switch {
		case strings.HasPrefix(lower, "bearer "):
			token = strings.TrimSpace(token[len("bearer "):])
		case strings.HasPrefix(lower, "token "):
			token = strings.TrimSpace(token[len("token "):])
		default:
			return token
		}
	}
}

func (s *Server) readSecret(ctx context.Context, namespace, secretName string) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		return nil, err
	}
	return secret, nil
}

func (s *Server) readRequiredSecretValue(ctx context.Context, namespace, secretName, key string) (string, error) {
	secret, err := s.readSecret(ctx, namespace, secretName)
	if err != nil {
		return "", mapK8sError(fmt.Sprintf("read secret %s/%s", namespace, secretName), err)
	}
	value, ok := secret.Data[key]
	if !ok {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("secret %s/%s missing key %q", namespace, secretName, key))
	}
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("secret %s/%s key %q is empty", namespace, secretName, key))
	}
	return trimmed, nil
}

func fetchOpenAICompatibleModels(ctx context.Context, baseURL string, session *openai.OpenAIAuthSession) ([]string, error) {
	models, err := openai.FetchModelMetadata(ctx, baseURL, session)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if id := strings.TrimSpace(model.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return uniqueSorted(ids), nil
}

func fetchAnthropicModels(ctx context.Context, headers map[string]string) ([]string, error) {
	endpoint := anthropicModelsBaseURL + anthropicModelsPath
	ids, err := fetchModelIDs(ctx, endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("querying Anthropic models from %s: %w", endpoint, err)
	}
	return ids, nil
}

func fetchModelIDs(ctx context.Context, endpoint string, headers map[string]string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := providerModelHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, snippet)
	}

	// The standard OpenAI /v1/models returns {"data":[{"id":"..."}]}.
	// The OAuth backend model endpoint returns {"models":[{"slug":"..."}]}.
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	var models []string
	if len(payload.Models) > 0 {
		models = make([]string, 0, len(payload.Models))
		for _, item := range payload.Models {
			if slug := strings.TrimSpace(item.Slug); slug != "" {
				models = append(models, slug)
			}
		}
	} else {
		models = make([]string, 0, len(payload.Data))
		for _, item := range payload.Data {
			if id := strings.TrimSpace(item.ID); id != "" {
				models = append(models, id)
			}
		}
	}
	models = uniqueSorted(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("provider returned no models")
	}
	return models, nil
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeOpenAIModelsBaseURL(provider, raw string) string {
	fallback := triggersv1alpha1.DefaultOpenAIBaseURLForProvider(provider)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = fallback
	}

	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		u, err = url.Parse(fallback)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fallback
		}
	}

	path := strings.TrimSuffix(strings.TrimSpace(u.Path), "/")
	path = strings.TrimSuffix(path, "/chat/completions")
	path = strings.TrimSuffix(path, "/responses")
	if path == "" && !isGitHubCopilotHost(u.Host) {
		path = "/v1"
	}
	// Skip /v1 forcing for non-versioned APIs.
	if !isChatGPTBackendHost(u.Host) && !isGitHubCopilotHost(u.Host) && !strings.Contains(path, "/v1") {
		path = strings.TrimSuffix(path, "/") + "/v1"
	}
	u.Path = path
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""

	return strings.TrimSuffix(u.String(), "/")
}

func isChatGPTBackendHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "chatgpt.com" || strings.HasSuffix(h, ".chatgpt.com")
}

func isGitHubCopilotHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "api.githubcopilot.com" || h == "api.individual.githubcopilot.com"
}
