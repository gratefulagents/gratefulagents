package dashboard

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type providerOAuthRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn providerOAuthRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func providerOAuthResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newProviderOAuthTestServer(t *testing.T, transport http.RoundTripper) *Server {
	t.Helper()
	scheme := testProjectScheme(t)
	return &Server{
		k8sClient:             fake.NewClientBuilder().WithScheme(scheme).Build(),
		scheme:                scheme,
		providerOAuthHTTP:     &http.Client{Transport: transport, Timeout: time.Second},
		providerOAuthSessions: make(map[string]providerOAuthSession),
	}
}

func TestAnthropicWebOAuthStoresCredentialsServerSide(t *testing.T) {
	var tokenRequest map[string]string
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://platform.claude.com/v1/oauth/token" {
			t.Fatalf("unexpected request URL %s", req.URL)
		}
		if err := json.NewDecoder(req.Body).Decode(&tokenRequest); err != nil {
			t.Fatalf("decode token request: %v", err)
		}
		return providerOAuthResponse(http.StatusOK, `{"access_token":"claude-access","refresh_token":"claude-refresh","expires_in":3600,"account":{"uuid":"acct-1","email_address":"claude@example.com"}}`), nil
	})
	srv := newProviderOAuthTestServer(t, transport)
	ctx := credActorCtx("oauth-user", "OAuth User")

	start, err := srv.StartProviderOAuth(ctx, &platform.StartProviderOAuthRequest{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("StartProviderOAuth() error = %v", err)
	}
	if start.Mode != "manual-code" || !strings.HasPrefix(start.AuthorizeUrl, "https://claude.ai/oauth/authorize?") {
		t.Fatalf("start = %#v", start)
	}
	session := srv.providerOAuthSessions["oauth-user"]
	result, err := srv.CompleteProviderOAuth(ctx, &platform.CompleteProviderOAuthRequest{
		Provider:  "anthropic",
		Code:      "authorization-code#" + session.state,
		SessionId: start.SessionId,
	})
	if err != nil {
		t.Fatalf("CompleteProviderOAuth() error = %v", err)
	}
	if result.Status != "completed" || result.Email != "claude@example.com" || result.Credentials == nil || !result.Credentials.AnthropicOauthPresent {
		t.Fatalf("result = %#v", result)
	}
	if tokenRequest["code"] != "authorization-code" || tokenRequest["code_verifier"] != session.verifier {
		t.Fatalf("token request = %#v", tokenRequest)
	}
	if _, ok := srv.providerOAuthSessions["oauth-user"]; ok {
		t.Fatal("completed session was not removed")
	}
}

func TestOpenAIWebOAuthDeviceFlowStoresCredentialsServerSide(t *testing.T) {
	claims, _ := json.Marshal(map[string]any{
		"email":                       "chatgpt@example.com",
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "chatgpt-account"},
	})
	idToken := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	polls := 0
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			return providerOAuthResponse(http.StatusOK, `{"device_auth_id":"device-1","user_code":"ABCD-EFGH","interval":"1"}`), nil
		case "/api/accounts/deviceauth/token":
			polls++
			if polls == 1 {
				return providerOAuthResponse(http.StatusForbidden, `{}`), nil
			}
			return providerOAuthResponse(http.StatusOK, `{"authorization_code":"approved-code","code_verifier":"provider-verifier"}`), nil
		case "/oauth/token":
			return providerOAuthResponse(http.StatusOK, `{"id_token":"`+idToken+`","access_token":"openai-access","refresh_token":"openai-refresh"}`), nil
		default:
			t.Fatalf("unexpected request path %s", req.URL.Path)
			return nil, nil
		}
	})
	srv := newProviderOAuthTestServer(t, transport)
	ctx := credActorCtx("oauth-user", "OAuth User")

	start, err := srv.StartProviderOAuth(ctx, &platform.StartProviderOAuthRequest{Provider: "openai"})
	if err != nil {
		t.Fatalf("StartProviderOAuth() error = %v", err)
	}
	if start.Mode != "device" || start.UserCode != "ABCD-EFGH" || start.IntervalSeconds != 1 || start.AuthorizeUrl != "https://auth.openai.com/codex/device" {
		t.Fatalf("start = %#v", start)
	}
	pollRequest := &platform.PollProviderOAuthRequest{Provider: "openai", SessionId: start.SessionId}
	pending, err := srv.PollProviderOAuth(ctx, pollRequest)
	if err != nil || pending.Status != "pending" {
		t.Fatalf("pending poll = %#v, err = %v", pending, err)
	}
	throttled, err := srv.PollProviderOAuth(ctx, pollRequest)
	if err != nil || throttled.Status != "pending" || polls != 1 {
		t.Fatalf("throttled poll = %#v, calls = %d, err = %v", throttled, polls, err)
	}
	// Advance the server-side cadence without sleeping.
	srv.providerOAuthMu.Lock()
	session := srv.providerOAuthSessions["oauth-user"]
	session.nextPollAt = time.Time{}
	srv.providerOAuthSessions["oauth-user"] = session
	srv.providerOAuthMu.Unlock()
	result, err := srv.PollProviderOAuth(ctx, pollRequest)
	if err != nil {
		t.Fatalf("completed poll error = %v", err)
	}
	if result.Status != "completed" || result.Email != "chatgpt@example.com" || result.Credentials == nil || !result.Credentials.OpenaiOauthPresent {
		t.Fatalf("result = %#v", result)
	}
	if _, ok := srv.providerOAuthSessions["oauth-user"]; ok {
		t.Fatal("completed session was not removed")
	}
}

func TestProviderOAuthSerializesConcurrentStarts(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/accounts/deviceauth/usercode" {
			t.Fatalf("unexpected request path %s", req.URL.Path)
		}
		calls.Add(1)
		close(entered)
		<-release
		return providerOAuthResponse(http.StatusOK, `{"device_auth_id":"device-1","user_code":"ABCD-EFGH","interval":"1"}`), nil
	})
	srv := newProviderOAuthTestServer(t, transport)
	ctx := credActorCtx("oauth-user", "OAuth User")
	firstDone := make(chan error, 1)
	go func() {
		_, err := srv.StartProviderOAuth(ctx, &platform.StartProviderOAuthRequest{Provider: "openai"})
		firstDone <- err
	}()
	<-entered
	if _, err := srv.StartProviderOAuth(ctx, &platform.StartProviderOAuthRequest{Provider: "openai"}); err == nil {
		t.Fatal("concurrent start was accepted")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider start calls = %d, want 1", got)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first start error = %v", err)
	}
}

func TestProviderOAuthRejectsMismatchedActorAndState(t *testing.T) {
	srv := newProviderOAuthTestServer(t, providerOAuthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("provider should not be called")
		return nil, nil
	}))
	ctx := credActorCtx("oauth-user", "OAuth User")
	start, err := srv.StartProviderOAuth(ctx, &platform.StartProviderOAuthRequest{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("start error = %v", err)
	}
	if _, err := srv.CompleteProviderOAuth(credActorCtx("other-user", "Other User"), &platform.CompleteProviderOAuthRequest{Provider: "anthropic", Code: "code#state", SessionId: start.SessionId}); err == nil {
		t.Fatal("other actor completed caller session")
	}
	if _, err := srv.CompleteProviderOAuth(ctx, &platform.CompleteProviderOAuthRequest{Provider: "anthropic", Code: "code#wrong-state", SessionId: start.SessionId}); err == nil {
		t.Fatal("mismatched state was accepted")
	}
	if session, ok := srv.providerOAuthSessions["oauth-user"]; !ok || session.inFlight {
		t.Fatalf("mismatched state should preserve a retryable session: %#v", session)
	}
}
