package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// GoogleClaims holds the verified claims from a Google ID token.
type GoogleClaims struct {
	Sub           string // Google user ID
	Email         string
	Name          string
	Picture       string
	HD            string // Hosted domain (Google Workspace)
	EmailVerified bool
}

// GoogleVerifier verifies Google OAuth ID tokens.
type GoogleVerifier struct {
	clientID   string
	httpClient *http.Client
}

// NewGoogleVerifier creates a verifier that checks tokens against the given client ID.
func NewGoogleVerifier(clientID string) *GoogleVerifier {
	return &GoogleVerifier{
		clientID:   clientID,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Verify validates a Google ID token and returns the claims.
func (g *GoogleVerifier) Verify(ctx context.Context, idToken string) (*GoogleClaims, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://oauth2.googleapis.com/tokeninfo?id_token="+idToken, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("invalid token: status %d", resp.StatusCode)
	}

	var payload struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified string `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
		HD            string `json:"hd"`
		Aud           string `json:"aud"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}

	if payload.Aud != g.clientID {
		return nil, fmt.Errorf("token audience %q does not match client ID %q", payload.Aud, g.clientID)
	}

	return &GoogleClaims{
		Sub:           payload.Sub,
		Email:         payload.Email,
		Name:          payload.Name,
		Picture:       payload.Picture,
		HD:            payload.HD,
		EmailVerified: payload.EmailVerified == "true",
	}, nil
}
