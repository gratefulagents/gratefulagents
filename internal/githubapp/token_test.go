package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMinterMintsAndCachesInstallationToken(t *testing.T) {
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/456/access_tokens" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Fatalf("Authorization = %q, want Bearer JWT", auth)
		}
		claims := decodeClaims(t, strings.TrimPrefix(auth, "Bearer "))
		if claims["iss"] != "123" {
			t.Fatalf("iss = %#v, want 123", claims["iss"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"installation-token","expires_at":"2026-06-10T11:00:00Z"}`))
	}))
	defer server.Close()

	m, err := NewMinter(testPrivateKeyPEM(t), WithHTTPClient(server.Client()), WithBaseURL(server.URL+"/"), WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("NewMinter() error = %v", err)
	}
	first, err := m.Mint(context.Background(), 123, 456)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	second, err := m.Mint(context.Background(), 123, 456)
	if err != nil {
		t.Fatalf("Mint() second error = %v", err)
	}
	if first != "installation-token" || second != first {
		t.Fatalf("tokens = %q, %q", first, second)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want cached single call", calls)
	}
}

func TestMinterRefreshesNearExpiry(t *testing.T) {
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"token-` + string(rune('0'+calls)) + `","expires_at":"2026-06-10T11:00:00Z"}`))
	}))
	defer server.Close()

	m, err := NewMinter(testPrivateKeyPEM(t), WithHTTPClient(server.Client()), WithBaseURL(server.URL+"/"), WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("NewMinter() error = %v", err)
	}
	first, err := m.Mint(context.Background(), 123, 456)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	now = now.Add(51 * time.Minute)
	second, err := m.Mint(context.Background(), 123, 456)
	if err != nil {
		t.Fatalf("Mint() second error = %v", err)
	}
	if first == second {
		t.Fatalf("token was not refreshed near expiry: %q", second)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func decodeClaims(t *testing.T, token string) map[string]interface{} {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d parts, want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return claims
}

func testPrivateKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestMinterScopedMintSendsPermissionsAndCachesSeparately(t *testing.T) {
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"token-` + strings.Repeat("x", len(bodies)) + `","expires_at":"2026-06-10T11:00:00Z"}`))
	}))
	defer server.Close()

	m, err := NewMinter(testPrivateKeyPEM(t), WithHTTPClient(server.Client()), WithBaseURL(server.URL+"/"), WithNow(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("NewMinter() error = %v", err)
	}

	full, err := m.Mint(context.Background(), 123, 456)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	scoped, err := m.MintScoped(context.Background(), 123, 456, ReviewerInstallationPermissions())
	if err != nil {
		t.Fatalf("MintScoped() error = %v", err)
	}
	if full == scoped {
		t.Fatal("scoped and full tokens must not share a cache entry")
	}
	if len(bodies) != 2 {
		t.Fatalf("API calls = %d, want 2 (no cache crosstalk)", len(bodies))
	}
	perms, ok := bodies[1]["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("scoped request body missing permissions: %#v", bodies[1])
	}
	if perms["contents"] != "read" || perms["pull_requests"] != "write" {
		t.Fatalf("scoped permissions = %#v, want contents:read pull_requests:write", perms)
	}
	if _, hasPerms := bodies[0]["permissions"]; hasPerms && bodies[0]["permissions"] != nil {
		t.Fatalf("full-scope request unexpectedly carried permissions: %#v", bodies[0])
	}

	// Second scoped mint must hit the scoped cache entry.
	again, err := m.MintScoped(context.Background(), 123, 456, ReviewerInstallationPermissions())
	if err != nil {
		t.Fatalf("MintScoped() second error = %v", err)
	}
	if again != scoped || len(bodies) != 2 {
		t.Fatalf("scoped mint not cached: token %q vs %q, calls %d", again, scoped, len(bodies))
	}
}
