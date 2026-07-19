package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
)

const (
	anthropicAuthorizeURL = "https://claude.ai/oauth/authorize"
	anthropicRedirectURI  = "https://platform.claude.com/oauth/code/callback"
	anthropicOAuthScope   = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	openAIOAuthIssuer     = "https://auth.openai.com"
	openAIOAuthClientID   = "app_EMoamEEZ73f0CkXaXp7hrann"
	providerOAuthTTL      = 15 * time.Minute
)

type providerOAuthSession struct {
	id           string
	provider     string
	started      time.Time
	verifier     string
	state        string
	deviceAuthID string
	userCode     string
	pollInterval time.Duration
	nextPollAt   time.Time
	inFlight     bool
}

// StartProviderOAuth starts a browser-safe OAuth flow whose verifier and token
// exchange remain on the platform server. A user can have one active provider
// login; starting another replaces it.
func (s *Server) StartProviderOAuth(ctx context.Context, req *platform.StartProviderOAuthRequest) (*platform.ProviderOAuthStart, error) {
	actor, err := providerOAuthActor(ctx)
	if err != nil {
		return nil, err
	}
	provider := strings.ToLower(strings.TrimSpace(req.GetProvider()))
	if provider != triggersv1alpha1.ProviderAnthropic && provider != triggersv1alpha1.ProviderOpenAI {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider must be anthropic or openai"))
	}
	sessionID, err := randomProviderOAuthValue(24)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generate OAuth session ID: %w", err))
	}
	if err := s.reserveProviderOAuthStart(actor.Subject, provider, sessionID); err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			s.deleteProviderOAuthSession(actor.Subject, sessionID)
		}
	}()

	var start *platform.ProviderOAuthStart
	var session providerOAuthSession
	switch provider {
	case triggersv1alpha1.ProviderAnthropic:
		verifier, challenge, err := generateProviderPKCE()
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		state, err := randomProviderOAuthValue(32)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		authorize, _ := url.Parse(anthropicAuthorizeURL)
		query := authorize.Query()
		query.Set("code", "true")
		query.Set("client_id", oauth.AnthropicOAuthClientID)
		query.Set("response_type", "code")
		query.Set("redirect_uri", anthropicRedirectURI)
		query.Set("scope", anthropicOAuthScope)
		query.Set("code_challenge", challenge)
		query.Set("code_challenge_method", "S256")
		query.Set("state", state)
		authorize.RawQuery = query.Encode()
		session = providerOAuthSession{id: sessionID, provider: provider, started: time.Now(), verifier: verifier, state: state}
		start = &platform.ProviderOAuthStart{Provider: provider, Mode: "manual-code", AuthorizeUrl: authorize.String(), SessionId: sessionID}

	case triggersv1alpha1.ProviderOpenAI:
		var body struct {
			DeviceAuthID string          `json:"device_auth_id"`
			UserCode     string          `json:"user_code"`
			UserCodeAlt  string          `json:"usercode"`
			Interval     json.RawMessage `json:"interval"`
		}
		if err := s.providerOAuthJSON(ctx, http.MethodPost, openAIOAuthIssuer+"/api/accounts/deviceauth/usercode", map[string]string{"client_id": openAIOAuthClientID}, &body); err != nil {
			return nil, err
		}
		userCode := strings.TrimSpace(body.UserCode)
		if userCode == "" {
			userCode = strings.TrimSpace(body.UserCodeAlt)
		}
		if strings.TrimSpace(body.DeviceAuthID) == "" || userCode == "" {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("ChatGPT device login response was missing required fields"))
		}
		interval := parseOAuthPollInterval(body.Interval)
		session = providerOAuthSession{
			id: sessionID, provider: provider, started: time.Now(), deviceAuthID: body.DeviceAuthID,
			userCode: userCode, pollInterval: time.Duration(interval) * time.Second,
		}
		start = &platform.ProviderOAuthStart{
			Provider: provider, Mode: "device", AuthorizeUrl: openAIOAuthIssuer + "/codex/device",
			UserCode: userCode, IntervalSeconds: uint32(interval), SessionId: sessionID,
		}

	}

	if !s.publishProviderOAuthStart(actor.Subject, sessionID, session) {
		return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("provider sign-in was replaced by a newer attempt"))
	}
	published = true
	return start, nil
}

// CompleteProviderOAuth exchanges Anthropic's pasted code and saves the
// resulting refreshable credential directly to the caller's namespace.
func (s *Server) CompleteProviderOAuth(ctx context.Context, req *platform.CompleteProviderOAuthRequest) (*platform.ProviderOAuthResult, error) {
	actor, err := providerOAuthActor(ctx)
	if err != nil {
		return nil, err
	}
	provider := strings.ToLower(strings.TrimSpace(req.GetProvider()))
	if provider != triggersv1alpha1.ProviderAnthropic {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("only anthropic uses code completion"))
	}
	code, returnedState, err := parseProviderOAuthCode(req.GetCode())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if returnedState == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("pasted value is missing the OAuth state; copy the complete code#state value"))
	}
	session, err := s.acquireProviderOAuthSession(actor.Subject, provider, req.GetSessionId(), false)
	if err != nil {
		return nil, err
	}
	if returnedState != session.state {
		s.releaseProviderOAuthSession(actor.Subject, session.id)
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("pasted code does not match this sign-in attempt; start again"))
	}
	completed := false
	defer func() {
		if completed {
			s.deleteProviderOAuthSession(actor.Subject, session.id)
		} else {
			s.releaseProviderOAuthSession(actor.Subject, session.id)
		}
	}()
	state := returnedState
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Account      struct {
			UUID         string `json:"uuid"`
			EmailAddress string `json:"email_address"`
		} `json:"account"`
	}
	if err := s.providerOAuthJSON(ctx, http.MethodPost, oauth.AnthropicOAuthTokenURL, map[string]string{
		"grant_type": "authorization_code", "code": code, "redirect_uri": anthropicRedirectURI,
		"client_id": oauth.AnthropicOAuthClientID, "code_verifier": session.verifier, "state": state,
	}, &token); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token.AccessToken) == "" || strings.TrimSpace(token.RefreshToken) == "" {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("Claude token response was missing tokens"))
	}
	now := time.Now().UTC()
	authJSON, err := oauth.MarshalAnthropicAuthJSON(oauth.AnthropicAuth{
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		Email: token.Account.EmailAddress, AccountUUID: token.Account.UUID,
		ExpiresAt: now.Add(time.Duration(token.ExpiresIn) * time.Second), LastRefresh: now,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("serialize Claude credentials: %w", err))
	}
	if !s.providerOAuthSessionIsCurrent(actor.Subject, session.id) {
		return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("this Claude sign-in was replaced by a newer attempt"))
	}
	credentials, err := s.storeProviderOAuth(ctx, actor, provider, authJSON, "")
	if err != nil {
		return nil, err
	}
	completed = true
	return &platform.ProviderOAuthResult{Status: "completed", Provider: provider, Email: token.Account.EmailAddress, Credentials: credentials}, nil
}

// PollProviderOAuth advances OpenAI's device flow. Pending approval is a normal
// response; successful token material is persisted before completion is returned.
func (s *Server) PollProviderOAuth(ctx context.Context, req *platform.PollProviderOAuthRequest) (*platform.ProviderOAuthResult, error) {
	actor, err := providerOAuthActor(ctx)
	if err != nil {
		return nil, err
	}
	provider := strings.ToLower(strings.TrimSpace(req.GetProvider()))
	if provider != triggersv1alpha1.ProviderOpenAI {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("only openai uses device polling"))
	}
	session, err := s.acquireProviderOAuthSession(actor.Subject, provider, req.GetSessionId(), true)
	if err != nil {
		return nil, err
	}
	if session.id == "" {
		// The server-enforced provider cadence has not elapsed yet.
		return &platform.ProviderOAuthResult{Status: "pending", Provider: provider}, nil
	}
	terminal := false
	defer func() {
		if terminal {
			s.deleteProviderOAuthSession(actor.Subject, session.id)
		} else {
			s.releaseProviderOAuthSession(actor.Subject, session.id)
		}
	}()

	payload, _ := json.Marshal(map[string]string{"device_auth_id": session.deviceAuthID, "user_code": session.userCode})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIOAuthIssuer+"/api/accounts/deviceauth/token", strings.NewReader(string(payload)))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "gratefulagents")
	response, err := s.providerOAuthClient().Do(request)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("poll ChatGPT device login: %w", err))
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
		return &platform.ProviderOAuthResult{Status: "pending", Provider: provider}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		terminal = true
		return &platform.ProviderOAuthResult{Status: "error", Provider: provider, Error: fmt.Sprintf("ChatGPT device login failed with status %s", response.Status)}, nil
	}
	// Approval codes are single-use. Any failure from here requires a new flow.
	terminal = true
	var approval struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&approval); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("parse ChatGPT device approval: %w", err))
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", approval.AuthorizationCode)
	form.Set("redirect_uri", openAIOAuthIssuer+"/deviceauth/callback")
	form.Set("client_id", openAIOAuthClientID)
	form.Set("code_verifier", approval.CodeVerifier)
	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIOAuthIssuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRequest.Header.Set("User-Agent", "gratefulagents")
	tokenResponse, err := s.providerOAuthClient().Do(tokenRequest)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("exchange ChatGPT authorization code: %w", err))
	}
	defer tokenResponse.Body.Close()
	if tokenResponse.StatusCode < 200 || tokenResponse.StatusCode >= 300 {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("ChatGPT token exchange failed with status %s", tokenResponse.Status))
	}
	var tokens struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(io.LimitReader(tokenResponse.Body, 1<<20)).Decode(&tokens); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("parse ChatGPT token response: %w", err))
	}
	if strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("ChatGPT token response was missing tokens"))
	}
	claims := providerOAuthJWTClaims(tokens.IDToken)
	email, _ := claims["email"].(string)
	accountID := ""
	if authClaims, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		accountID, _ = authClaims["chatgpt_account_id"].(string)
	}
	authJSON, err := json.Marshal(map[string]any{
		"OPENAI_API_KEY": nil,
		"tokens":         map[string]string{"id_token": tokens.IDToken, "access_token": tokens.AccessToken, "refresh_token": tokens.RefreshToken, "account_id": accountID},
		"last_refresh":   time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("serialize ChatGPT credentials: %w", err))
	}
	if !s.providerOAuthSessionIsCurrent(actor.Subject, session.id) {
		return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("this ChatGPT sign-in was replaced by a newer attempt"))
	}
	credentials, err := s.storeProviderOAuth(ctx, actor, provider, authJSON, accountID)
	if err != nil {
		return nil, err
	}
	return &platform.ProviderOAuthResult{Status: "completed", Provider: provider, Email: email, Credentials: credentials}, nil
}

func providerOAuthActor(ctx context.Context) (requestActor, error) {
	actor := requestActorFromContext(ctx)
	if strings.TrimSpace(actor.Subject) == "" {
		return requestActor{}, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	return actor, nil
}

func (s *Server) storeProviderOAuth(ctx context.Context, actor requestActor, provider string, authJSON []byte, accountID string) (*platform.MyCredentials, error) {
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	if err := s.applyCredentialOAuth(ctx, namespace, provider, string(authJSON), accountID, false); err != nil {
		return nil, err
	}
	credentials := s.myCredentialsProto(ctx, namespace)
	// The controller-runtime client may read from an informer cache that has not
	// observed the write yet. The successful write is authoritative for the
	// provider completed by this request.
	switch provider {
	case triggersv1alpha1.ProviderAnthropic:
		credentials.AnthropicOauthPresent = true
	case triggersv1alpha1.ProviderOpenAI:
		credentials.OpenaiOauthPresent = true
	}
	return credentials, nil
}

func (s *Server) reserveProviderOAuthStart(subject, provider, sessionID string) error {
	s.providerOAuthMu.Lock()
	defer s.providerOAuthMu.Unlock()
	if s.providerOAuthSessions == nil {
		s.providerOAuthSessions = make(map[string]providerOAuthSession)
	}
	now := time.Now()
	for key, existing := range s.providerOAuthSessions {
		if now.Sub(existing.started) > providerOAuthTTL {
			delete(s.providerOAuthSessions, key)
		}
	}
	if existing, ok := s.providerOAuthSessions[subject]; ok {
		if existing.inFlight {
			return connect.NewError(connect.CodeAborted, fmt.Errorf("a provider sign-in request is already in progress"))
		}
		if now.Sub(existing.started) < time.Second {
			return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("wait a moment before restarting provider sign-in"))
		}
	}
	s.providerOAuthSessions[subject] = providerOAuthSession{
		id: sessionID, provider: provider, started: now, inFlight: true,
	}
	return nil
}

func (s *Server) publishProviderOAuthStart(subject, sessionID string, session providerOAuthSession) bool {
	s.providerOAuthMu.Lock()
	defer s.providerOAuthMu.Unlock()
	reservation, ok := s.providerOAuthSessions[subject]
	if !ok || reservation.id != sessionID || !reservation.inFlight {
		return false
	}
	session.inFlight = false
	s.providerOAuthSessions[subject] = session
	return true
}

// acquireProviderOAuthSession validates the per-attempt ID and atomically
// serializes provider calls. For rate-limited or concurrent polls it returns an
// empty session, which callers translate to a normal pending response.
func (s *Server) acquireProviderOAuthSession(subject, provider, sessionID string, poll bool) (providerOAuthSession, error) {
	s.providerOAuthMu.Lock()
	defer s.providerOAuthMu.Unlock()
	session, ok := s.providerOAuthSessions[subject]
	if !ok || session.provider != provider || session.id == "" || session.id != strings.TrimSpace(sessionID) {
		return providerOAuthSession{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no matching %s sign-in in progress; start again", provider))
	}
	if time.Since(session.started) > providerOAuthTTL {
		delete(s.providerOAuthSessions, subject)
		return providerOAuthSession{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s sign-in expired; start again", provider))
	}
	if session.inFlight {
		if poll {
			return providerOAuthSession{}, nil
		}
		return providerOAuthSession{}, connect.NewError(connect.CodeAborted, fmt.Errorf("%s sign-in completion is already in progress", provider))
	}
	now := time.Now()
	if poll && now.Before(session.nextPollAt) {
		return providerOAuthSession{}, nil
	}
	session.inFlight = true
	if poll {
		session.nextPollAt = now.Add(session.pollInterval)
	}
	s.providerOAuthSessions[subject] = session
	return session, nil
}

func (s *Server) providerOAuthSessionIsCurrent(subject, sessionID string) bool {
	s.providerOAuthMu.Lock()
	defer s.providerOAuthMu.Unlock()
	session, ok := s.providerOAuthSessions[subject]
	return ok && session.id == sessionID && session.inFlight
}

func (s *Server) releaseProviderOAuthSession(subject, sessionID string) {
	s.providerOAuthMu.Lock()
	defer s.providerOAuthMu.Unlock()
	if session, ok := s.providerOAuthSessions[subject]; ok && session.id == sessionID {
		session.inFlight = false
		s.providerOAuthSessions[subject] = session
	}
}

func (s *Server) deleteProviderOAuthSession(subject, sessionID string) {
	s.providerOAuthMu.Lock()
	defer s.providerOAuthMu.Unlock()
	if session, ok := s.providerOAuthSessions[subject]; ok && session.id == sessionID {
		delete(s.providerOAuthSessions, subject)
	}
}

func (s *Server) providerOAuthClient() *http.Client {
	if s.providerOAuthHTTP != nil {
		return s.providerOAuthHTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (s *Server) providerOAuthJSON(ctx context.Context, method, endpoint string, input any, output any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "gratefulagents")
	response, err := s.providerOAuthClient().Do(request)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("provider sign-in request: %w", err))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("provider sign-in failed with status %s", response.Status))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("parse provider sign-in response: %w", err))
	}
	return nil
}

func generateProviderPKCE() (verifier, challenge string, err error) {
	verifier, err = randomProviderOAuthValue(64)
	if err != nil {
		return "", "", fmt.Errorf("generate PKCE verifier: %w", err)
	}
	digest := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func randomProviderOAuthValue(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func parseProviderOAuthCode(raw string) (code, state string, err error) {
	trimmed := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), `"`))
	if trimmed == "" {
		return "", "", fmt.Errorf("paste the code shown after approving access")
	}
	if before, after, ok := strings.Cut(trimmed, "#"); ok {
		if strings.TrimSpace(before) == "" {
			return "", "", fmt.Errorf("pasted value is missing the authorization code")
		}
		return strings.TrimSpace(before), strings.TrimSpace(after), nil
	}
	return trimmed, "", nil
}

func parseOAuthPollInterval(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 5
	}
	var number int
	if json.Unmarshal(raw, &number) == nil && number > 0 {
		if number > 30 {
			return 30
		}
		return number
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if _, err := fmt.Sscanf(strings.TrimSpace(text), "%d", &number); err == nil && number > 0 {
			if number > 30 {
				return 30
			}
			return number
		}
	}
	return 5
}

func providerOAuthJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}
