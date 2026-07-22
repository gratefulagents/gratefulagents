package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/google/go-github/v68/github"
)

const refreshSkew = 10 * time.Minute

const (
	// PrivateKeySecretKey is the Secret data key for GitHub App private keys.
	PrivateKeySecretKey = "private-key.pem"
	// TokenSecretKey is the Secret data key for installation tokens consumed by workers.
	TokenSecretKey = "token"
)

// InstallationTokenMinter mints GitHub App installation access tokens.
type InstallationTokenMinter interface {
	MintInstallationToken(ctx context.Context, appID, installationID int64, privateKeyPEM []byte) (string, error)
}

// ScopedInstallationTokenMinter is an optional capability for minting tokens
// downscoped to a specific permission set. Callers type-assert and fall back
// to full-scope minting when unimplemented.
type ScopedInstallationTokenMinter interface {
	MintScopedInstallationToken(ctx context.Context, appID, installationID int64, privateKeyPEM []byte, permissions *github.InstallationPermissions) (string, error)
}

// ReviewerInstallationPermissions returns the reduced permission set for PR
// reviewer runs: read code, write reviews — never push. This boundary holds
// even if the agent pod is fully compromised.
func ReviewerInstallationPermissions() *github.InstallationPermissions {
	return &github.InstallationPermissions{
		Contents:     github.Ptr("read"),
		Metadata:     github.Ptr("read"),
		Issues:       github.Ptr("read"),
		PullRequests: github.Ptr("write"),
	}
}

// PullRequestPollingInstallationPermissions returns the read-only permission
// set needed by the control-plane PR monitor. Polling credentials never need
// contents write or branch-push access.
func PullRequestPollingInstallationPermissions() *github.InstallationPermissions {
	return &github.InstallationPermissions{
		Metadata:     github.Ptr("read"),
		Issues:       github.Ptr("read"),
		PullRequests: github.Ptr("read"),
		Checks:       new("read"),
		Statuses:     new("read"),
	}
}

type tokenCacheKey struct {
	AppID          int64
	InstallationID int64
	// PermissionsKey fingerprints the requested permission set; empty means
	// the installation's full default permissions.
	PermissionsKey string
}

type cachedToken struct {
	Token     string
	ExpiresAt time.Time
}

// Minter mints and caches installation tokens for a single GitHub App private key.
type Minter struct {
	privateKey *rsa.PrivateKey
	httpClient *http.Client
	now        func() time.Time
	baseURL    *url.URL

	mu    sync.Mutex
	cache map[tokenCacheKey]cachedToken
}

// NewMinter creates a GitHub App installation token minter from a private key PEM.
func NewMinter(privateKeyPEM []byte, opts ...Option) (*Minter, error) {
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	m := &Minter{
		privateKey: key,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		now:        time.Now,
		cache:      make(map[tokenCacheKey]cachedToken),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

// Option customizes a Minter.
type Option func(*Minter)

// WithHTTPClient sets the HTTP client used for GitHub API calls.
func WithHTTPClient(client *http.Client) Option {
	return func(m *Minter) {
		if client != nil {
			m.httpClient = client
		}
	}
}

// WithNow sets the clock used for JWT claims and token cache checks.
func WithNow(now func() time.Time) Option {
	return func(m *Minter) {
		if now != nil {
			m.now = now
		}
	}
}

// WithBaseURL sets an alternate GitHub API base URL, primarily for tests.
func WithBaseURL(raw string) Option {
	return func(m *Minter) {
		if raw == "" {
			return
		}
		u, err := url.Parse(raw)
		if err == nil {
			m.baseURL = u
		}
	}
}

// Mint mints or returns a cached installation access token.
func (m *Minter) Mint(ctx context.Context, appID, installationID int64) (string, error) {
	return m.MintScoped(ctx, appID, installationID, nil)
}

// MintScoped mints or returns a cached installation token, optionally
// downscoped to the given permission set. A nil permissions value requests
// the installation's full default permissions.
func (m *Minter) MintScoped(ctx context.Context, appID, installationID int64, permissions *github.InstallationPermissions) (string, error) {
	if appID <= 0 {
		return "", fmt.Errorf("appID must be greater than zero")
	}
	if installationID <= 0 {
		return "", fmt.Errorf("installationID must be greater than zero")
	}
	key := tokenCacheKey{AppID: appID, InstallationID: installationID, PermissionsKey: permissionsFingerprint(permissions)}
	now := m.now()

	m.mu.Lock()
	if cached := m.cache[key]; cached.Token != "" && now.Before(cached.ExpiresAt.Add(-refreshSkew)) {
		m.mu.Unlock()
		return cached.Token, nil
	}
	m.mu.Unlock()

	appJWT, err := m.signAppJWT(appID, now)
	if err != nil {
		return "", err
	}
	client := github.NewClient(m.httpClient).WithAuthToken(appJWT)
	if m.baseURL != nil {
		client.BaseURL = m.baseURL
	}
	var opts *github.InstallationTokenOptions
	if permissions != nil {
		opts = &github.InstallationTokenOptions{Permissions: permissions}
	}
	installationToken, _, err := client.Apps.CreateInstallationToken(ctx, installationID, opts)
	if err != nil {
		return "", fmt.Errorf("create GitHub App installation token: %w", err)
	}
	token := installationToken.GetToken()
	if token == "" {
		return "", fmt.Errorf("GitHub App installation token response missing token")
	}
	expiresAt := now.Add(time.Hour)
	if installationToken.ExpiresAt != nil && !installationToken.ExpiresAt.IsZero() {
		expiresAt = installationToken.ExpiresAt.Time
	}

	m.mu.Lock()
	m.cache[key] = cachedToken{Token: token, ExpiresAt: expiresAt}
	m.mu.Unlock()
	return token, nil
}

// permissionsFingerprint produces a stable cache-key fragment for a
// permission set so scoped and full tokens never share cache entries.
func permissionsFingerprint(p *github.InstallationPermissions) string {
	if p == nil {
		return ""
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "unmarshalable"
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

// AppJWT signs a short-lived JWT for GitHub App API calls.
func (m *Minter) AppJWT(appID int64) (string, error) {
	if appID <= 0 {
		return "", fmt.Errorf("appID must be greater than zero")
	}
	return m.signAppJWT(appID, m.now())
}

func (m *Minter) signAppJWT(appID int64, now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	payload := map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": strconv.FormatInt(appID, 10),
	}
	return signJWT(header, payload, m.privateKey)
}

// KeyedMinter caches per-private-key minters and implements InstallationTokenMinter.
type KeyedMinter struct {
	opts  []Option
	mu    sync.Mutex
	byPEM map[[32]byte]*Minter
}

// NewKeyedMinter creates a multi-key installation token minter.
func NewKeyedMinter(opts ...Option) *KeyedMinter {
	return &KeyedMinter{opts: opts, byPEM: make(map[[32]byte]*Minter)}
}

// MintInstallationToken mints an installation token using privateKeyPEM.
func (k *KeyedMinter) MintInstallationToken(ctx context.Context, appID, installationID int64, privateKeyPEM []byte) (string, error) {
	m, err := k.minterForKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	return m.Mint(ctx, appID, installationID)
}

// MintScopedInstallationToken mints a permission-downscoped installation
// token using privateKeyPEM. Implements ScopedInstallationTokenMinter.
func (k *KeyedMinter) MintScopedInstallationToken(ctx context.Context, appID, installationID int64, privateKeyPEM []byte, permissions *github.InstallationPermissions) (string, error) {
	m, err := k.minterForKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	return m.MintScoped(ctx, appID, installationID, permissions)
}

// AppJWT signs a short-lived JWT using privateKeyPEM.
func (k *KeyedMinter) AppJWT(appID int64, privateKeyPEM []byte) (string, error) {
	m, err := k.minterForKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	return m.AppJWT(appID)
}

func (k *KeyedMinter) minterForKey(privateKeyPEM []byte) (*Minter, error) {
	hash := sha256.Sum256(privateKeyPEM)
	k.mu.Lock()
	m := k.byPEM[hash]
	k.mu.Unlock()
	if m == nil {
		var err error
		m, err = NewMinter(privateKeyPEM, k.opts...)
		if err != nil {
			return nil, err
		}
		k.mu.Lock()
		if existing := k.byPEM[hash]; existing != nil {
			m = existing
		} else {
			k.byPEM[hash] = m
		}
		k.mu.Unlock()
	}
	return m, nil
}

func parsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in GitHub App private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		pkcs1, pkcs1Err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if pkcs1Err != nil {
			return nil, fmt.Errorf("parse GitHub App private key: %w", err)
		}
		return pkcs1, nil
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("GitHub App private key is not RSA")
	}
	return rsaKey, nil
}

func signJWT(header map[string]string, payload map[string]any, key *rsa.PrivateKey) (string, error) {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(payloadJSON)
	hash := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
