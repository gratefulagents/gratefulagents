package oauthrefresh

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// newTestRefresher builds a Refresher backed by a fake K8s client and a stubbed
// HTTP transport so token-exchange requests are intercepted in-process.
func newTestRefresher(t *testing.T, now time.Time, transport http.RoundTripper, objs ...*corev1.Secret) *Refresher {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(refresherTestScheme(t))
	for _, o := range objs {
		builder = builder.WithObjects(o)
	}
	return &Refresher{
		client:       builder.Build(),
		httpClient:   &http.Client{Transport: transport},
		now:          nowFunc(now),
		staleSecrets: map[oauthSecretRef]string{},
	}
}

const testSecretNamespace = "ns"

func secretWithAuthJSON(name, authJSON string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: testSecretNamespace, Name: name},
		Data:       map[string][]byte{oauth.AuthJSONKey: []byte(authJSON)},
	}
}

func TestRefreshSecretRotatesCopilotToken(t *testing.T) {
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour).Unix()
	secret := secretWithAuthJSON("copilot-secret", `{"oauth_token":"github-oauth","type":"copilot"}`)

	var calls int
	r := newTestRefresher(t, now, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", req.Method)
		}
		if got := req.URL.String(); got != oauth.CopilotTokenURL {
			t.Fatalf("url = %s, want %s", got, oauth.CopilotTokenURL)
		}
		if got := req.Header.Get("Authorization"); got != "token github-oauth" {
			t.Fatalf("Authorization = %q, want token github-oauth", got)
		}
		if got := req.Header.Get("Editor-Plugin-Version"); got != copilotEditorVersion {
			t.Fatalf("Editor-Plugin-Version = %q, want %q", got, copilotEditorVersion)
		}
		if got := req.Header.Get("User-Agent"); got != copilotUserAgent {
			t.Fatalf("User-Agent = %q, want %q", got, copilotUserAgent)
		}
		return jsonResponse(http.StatusOK, `{"token":"copilot-api-token","expires_at":`+strconv.FormatInt(expiresAt, 10)+`}`), nil
	}), secret)

	ref := oauthSecretRef{NamespacedName: types.NamespacedName{Namespace: testSecretNamespace, Name: "copilot-secret"}, Provider: triggersv1alpha1.ProviderCopilot}
	if err := r.refreshSecret(context.Background(), ref); err != nil {
		t.Fatalf("refreshSecret() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", calls)
	}

	auth := reloadCopilotAuth(t, r, ref.NamespacedName)
	if auth.OAuthToken != "github-oauth" || auth.Token != "copilot-api-token" {
		t.Fatalf("rotated auth = %#v", auth)
	}
	if got := auth.ExpiresAt.Unix(); got != expiresAt {
		t.Fatalf("ExpiresAt = %d, want %d", got, expiresAt)
	}
}

func TestRefreshSecretRotatesAnthropicToken(t *testing.T) {
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	secret := secretWithAuthJSON("anthropic-secret",
		`{"access_token":"old-access","refresh_token":"old-refresh","expired":"`+expired+`","type":"claude"}`)

	r := newTestRefresher(t, now, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", req.Method)
		}
		if got := req.URL.String(); got != oauth.AnthropicOAuthTokenURL {
			t.Fatalf("url = %s, want %s", got, oauth.AnthropicOAuthTokenURL)
		}
		var body map[string]string
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["client_id"] != oauth.AnthropicOAuthClientID {
			t.Fatalf("client_id = %q, want %q", body["client_id"], oauth.AnthropicOAuthClientID)
		}
		if body["scope"] != oauth.AnthropicOAuthScope {
			t.Fatalf("scope = %q, want %q", body["scope"], oauth.AnthropicOAuthScope)
		}
		if body["refresh_token"] != "old-refresh" {
			t.Fatalf("refresh_token = %q, want old-refresh", body["refresh_token"])
		}
		return jsonResponse(http.StatusOK, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`), nil
	}), secret)

	ref := oauthSecretRef{NamespacedName: types.NamespacedName{Namespace: testSecretNamespace, Name: "anthropic-secret"}, Provider: triggersv1alpha1.ProviderAnthropic}
	if err := r.refreshSecret(context.Background(), ref); err != nil {
		t.Fatalf("refreshSecret() error = %v", err)
	}

	auth := reloadAnthropicAuth(t, r, ref.NamespacedName)
	if auth.AccessToken != "new-access" || auth.RefreshToken != "new-refresh" {
		t.Fatalf("rotated auth = %#v", auth)
	}
	if want := now.Add(time.Hour); !auth.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", auth.ExpiresAt, want)
	}
}

func TestRefreshSecretMarksStaleOnTerminalErrorAndSkips(t *testing.T) {
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	secret := secretWithAuthJSON("copilot-secret", `{"oauth_token":"github-oauth","type":"copilot"}`)

	var calls int
	r := newTestRefresher(t, now, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusUnauthorized, `{"message":"bad credentials","token":"leaked"}`), nil
	}), secret)

	ref := oauthSecretRef{NamespacedName: types.NamespacedName{Namespace: testSecretNamespace, Name: "copilot-secret"}, Provider: triggersv1alpha1.ProviderCopilot}

	err := r.refreshSecret(context.Background(), ref)
	if err == nil {
		t.Fatal("refreshSecret() error = nil, want error")
	}
	if strings.Contains(err.Error(), "leaked") {
		t.Fatalf("error leaked token material: %s", err)
	}
	r.mu.Lock()
	_, marked := r.staleSecrets[ref]
	r.mu.Unlock()
	if !marked {
		t.Fatal("secret was not marked stale after terminal refresh error")
	}

	// A second pass over the unchanged secret must be skipped (no retry).
	if err := r.refreshSecret(context.Background(), ref); err != nil {
		t.Fatalf("second refreshSecret() error = %v, want nil (skipped)", err)
	}
	if calls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1 (stale secret retried)", calls)
	}
}

func TestRefreshSecretSkipsWhenNotStale(t *testing.T) {
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	// Copilot token present and far from expiry → no refresh needed.
	expiresAt := now.Add(time.Hour).Unix()
	secret := secretWithAuthJSON("copilot-secret",
		`{"oauth_token":"github-oauth","token":"copilot-api-token","expires_at":`+strconv.FormatInt(expiresAt, 10)+`,"type":"copilot"}`)

	var calls int
	r := newTestRefresher(t, now, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusOK, `{}`), nil
	}), secret)

	ref := oauthSecretRef{NamespacedName: types.NamespacedName{Namespace: testSecretNamespace, Name: "copilot-secret"}, Provider: triggersv1alpha1.ProviderCopilot}
	if err := r.refreshSecret(context.Background(), ref); err != nil {
		t.Fatalf("refreshSecret() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("token endpoint calls = %d, want 0 (fresh token)", calls)
	}
}

func reloadCopilotAuth(t *testing.T, r *Refresher, nn types.NamespacedName) oauth.CopilotAuth {
	t.Helper()
	secret := &corev1.Secret{}
	if err := r.client.Get(context.Background(), nn, secret); err != nil {
		t.Fatalf("reload secret: %v", err)
	}
	auth, err := oauth.ParseCopilotAuthJSON(secret.Data[oauth.AuthJSONKey])
	if err != nil {
		t.Fatalf("ParseCopilotAuthJSON() error = %v", err)
	}
	return auth
}

func reloadAnthropicAuth(t *testing.T, r *Refresher, nn types.NamespacedName) oauth.AnthropicAuth {
	t.Helper()
	secret := &corev1.Secret{}
	if err := r.client.Get(context.Background(), nn, secret); err != nil {
		t.Fatalf("reload secret: %v", err)
	}
	auth, err := oauth.ParseAnthropicAuthJSON(secret.Data[oauth.AuthJSONKey])
	if err != nil {
		t.Fatalf("ParseAnthropicAuthJSON() error = %v", err)
	}
	return auth
}

func nowFunc(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
