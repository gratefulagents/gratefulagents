package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type middlewareRoleStore struct {
	Store
	user *User
}

func (s *middlewareRoleStore) GetUserByID(context.Context, string) (*User, error) {
	return s.user, nil
}

func testTokenVerifier(t *testing.T) (*JWTIssuer, *JWKSCache) {
	t.Helper()
	issuer, err := NewJWTIssuer("")
	if err != nil {
		t.Fatalf("NewJWTIssuer() error = %v", err)
	}
	cache, err := NewJWKSCacheFromIssuer(issuer)
	if err != nil {
		t.Fatalf("NewJWKSCacheFromIssuer() error = %v", err)
	}
	return issuer, cache
}

func TestJWTMiddlewareUsesCurrentStoredRole(t *testing.T) {
	issuer, cache := testTokenVerifier(t)

	for _, test := range []struct {
		name       string
		tokenRole  string
		storedRole string
	}{
		{name: "promotion", tokenRole: RoleMember, storedRole: RoleAdmin},
		{name: "demotion", tokenRole: RoleAdmin, storedRole: RoleMember},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, _, err := issuer.IssueAccessToken(AccessTokenClaims{Sub: "user-1", Role: test.tokenRole}, time.Minute)
			if err != nil {
				t.Fatalf("IssueAccessToken() error = %v", err)
			}
			store := &middlewareRoleStore{user: &User{ID: "user-1", Role: test.storedRole}}
			var gotRole string
			handler := JWTMiddleware(cache, store)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotRole = ClaimsFromContext(r.Context()).Role
			}))
			request := httptest.NewRequest(http.MethodPost, "/rpc", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if gotRole != test.storedRole {
				t.Fatalf("role = %q, want current stored role %q", gotRole, test.storedRole)
			}
		})
	}
}

func TestJWTMiddlewareFailsClosedWithoutStore(t *testing.T) {
	issuer, cache := testTokenVerifier(t)
	token, _, err := issuer.IssueAccessToken(AccessTokenClaims{Sub: "user-1", Role: RoleAdmin}, time.Minute)
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}
	handler := JWTMiddleware(cache, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler ran without an authorization store")
	}))
	request := httptest.NewRequest(http.MethodPost, "/rpc", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}
