package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// currentClaims replaces identity fields embedded in an access token with the
// current stored user values. Access tokens live for several minutes, while
// role promotions and demotions are expected to take effect immediately.
func currentClaims(ctx context.Context, claims *Claims, store Store) (*Claims, error) {
	if claims == nil || store == nil {
		return nil, errors.New("auth store unavailable")
	}
	user, err := store.GetUserByID(ctx, claims.Sub)
	if err != nil {
		return nil, err
	}
	resolved := *claims
	resolved.Username = user.Username
	resolved.Email = user.Email
	resolved.Name = user.Name
	resolved.Picture = user.Picture
	resolved.Role = user.Role
	return &resolved, nil
}

// JWTMiddleware returns a chi middleware that validates JWT Bearer tokens.
// The current stored role overrides the role embedded in the token so
// promotions and demotions apply without waiting for expiry. Requests fail
// closed when the store cannot resolve the user.
func JWTMiddleware(jwks *JWKSCache, store Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "missing authorization header", http.StatusUnauthorized)
				return
			}

			if !strings.HasPrefix(authHeader, "Bearer ") {
				http.Error(w, "invalid authorization header", http.StatusUnauthorized)
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			claims, err := jwks.VerifyToken(token)
			if err != nil {
				http.Error(w, "invalid token: "+err.Error(), http.StatusUnauthorized)
				return
			}
			claims, err = currentClaims(r.Context(), claims, store)
			if err != nil {
				http.Error(w, "unable to resolve current user", http.StatusServiceUnavailable)
				return
			}

			ctx := WithClaims(r.Context(), claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// OptionalJWTMiddleware validates a Bearer token when one is provided and
// injects the claims, but never rejects the request. It lets mixed routes
// (e.g. the auth service, where Login must stay public) serve handlers that
// need the caller identity, such as GetCurrentUser and SearchUsers.
func OptionalJWTMiddleware(jwks *JWKSCache, store Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				token := strings.TrimPrefix(authHeader, "Bearer ")
				if claims, err := jwks.VerifyToken(token); err == nil {
					if claims, err = currentClaims(r.Context(), claims, store); err == nil {
						r = r.WithContext(WithClaims(r.Context(), claims))
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
