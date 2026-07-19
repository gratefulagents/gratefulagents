package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Claims represents the JWT claims for an authenticated user.
type Claims struct {
	Sub      string `json:"sub"`
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Name     string `json:"name"`
	Picture  string `json:"picture,omitempty"`
	Role     string `json:"role"`
}

type claimsContextKey struct{}

// WithClaims stores claims in the context.
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, c)
}

// ClaimsFromContext retrieves claims from the context.
func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsContextKey{}).(*Claims)
	return c
}

// ParseUnverifiedClaims extracts claims from a JWT without verifying the signature.
// Use only after the signature has been verified by the middleware.
func ParseUnverifiedClaims(token string) (*Claims, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT: expected 3 parts, got %d", len(parts))
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}

	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("unmarshal claims: %w", err)
	}

	return &c, nil
}
