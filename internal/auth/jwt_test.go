package auth

import (
	"strings"
	"testing"
	"time"
)

func newTestIssuer(t *testing.T) (*JWTIssuer, *JWKSCache) {
	t.Helper()
	issuer, err := NewJWTIssuer("")
	if err != nil {
		t.Fatalf("NewJWTIssuer: %v", err)
	}
	cache, err := NewJWKSCacheFromIssuer(issuer)
	if err != nil {
		t.Fatalf("NewJWKSCacheFromIssuer: %v", err)
	}
	return issuer, cache
}

func TestVerifyTokenAcceptsValidAccessToken(t *testing.T) {
	issuer, cache := newTestIssuer(t)
	token, _, err := issuer.IssueAccessToken(AccessTokenClaims{Sub: "user-1", Username: "u1", Role: "member"}, time.Minute)
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	claims, err := cache.VerifyToken(token)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if claims.Sub != "user-1" || claims.Role != "member" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestVerifyTokenRejectsRefreshToken(t *testing.T) {
	issuer, cache := newTestIssuer(t)
	refresh, _, err := issuer.IssueRefreshToken("user-1", time.Hour)
	if err != nil {
		t.Fatalf("IssueRefreshToken: %v", err)
	}
	if _, err := cache.VerifyToken(refresh); err == nil {
		t.Fatal("refresh token must not be accepted as an access token")
	}
}

func TestVerifyTokenRejectsExpiredToken(t *testing.T) {
	issuer, cache := newTestIssuer(t)
	token, _, err := issuer.IssueAccessToken(AccessTokenClaims{Sub: "user-1"}, -time.Minute)
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	if _, err := cache.VerifyToken(token); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("want expiry error, got %v", err)
	}
}

func TestVerifyTokenRejectsForeignIssuerKey(t *testing.T) {
	// A token signed by a different issuer (different key) must fail
	// signature verification against our JWKS.
	foreign, err := NewJWTIssuer("")
	if err != nil {
		t.Fatalf("NewJWTIssuer: %v", err)
	}
	_, cache := newTestIssuer(t)
	token, _, err := foreign.IssueAccessToken(AccessTokenClaims{Sub: "user-1"}, time.Minute)
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	if _, err := cache.VerifyToken(token); err == nil {
		t.Fatal("token signed with a foreign key must be rejected")
	}
}

func TestEscapeLikePattern(t *testing.T) {
	cases := map[string]string{
		`alice`:    `alice`,
		`%`:        `\%`,
		`_x_`:      `\_x\_`,
		`a\b`:      `a\\b`,
		`50%_done`: `50\%\_done`,
	}
	for in, want := range cases {
		if got := escapeLikePattern(in); got != want {
			t.Errorf("escapeLikePattern(%q) = %q, want %q", in, got, want)
		}
	}
}
