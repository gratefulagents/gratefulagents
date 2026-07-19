package dashboard

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/auth"
)

type requestActor struct {
	Subject string
	Role    string
	Name    string
}

type requestActorContextKey struct{}

// insecureHeaderAuthEnabled reports whether header-based identity fallback is
// explicitly enabled. This is for local development only: trusting identity
// headers without JWT verification allows trivial actor/role spoofing.
func insecureHeaderAuthEnabled() bool {
	return os.Getenv("DASHBOARD_INSECURE_HEADER_AUTH") == "true"
}

// withRequestActor populates a requestActor from JWT claims. When claims are
// absent (which cannot happen behind the JWT middleware), an empty actor is
// recorded so that downstream authorization checks fail closed instead of
// trusting spoofable identity headers.
func withRequestActor(ctx context.Context, header http.Header) context.Context {
	if claims := auth.ClaimsFromContext(ctx); claims != nil {
		actor := requestActor{
			Subject: claims.Sub,
			Role:    claims.Role,
			Name:    claims.Name,
		}
		return context.WithValue(ctx, requestActorContextKey{}, actor)
	}

	if insecureHeaderAuthEnabled() {
		return withRequestActorFromHeader(ctx, header)
	}

	// Record an explicit empty actor: the request went through the RPC
	// surface without verified claims, so authorization must deny it.
	return context.WithValue(ctx, requestActorContextKey{}, requestActor{})
}

func withRequestActorFromHeader(ctx context.Context, header http.Header) context.Context {
	if header == nil {
		return ctx
	}
	actor := requestActor{
		Role: strings.TrimSpace(firstHeaderValue(header,
			"x-gratefulagents-role", "x-engg-role", "x-role")),
		Subject: strings.TrimSpace(firstHeaderValue(header,
			"x-gratefulagents-actor", "x-engg-actor", "x-user", "x-user-id")),
	}
	return context.WithValue(ctx, requestActorContextKey{}, actor)
}

func requestActorFromContext(ctx context.Context) requestActor {
	actor, _ := requestActorFromContextOK(ctx)
	return actor
}

func actorIsAdmin(actor requestActor) bool {
	return strings.EqualFold(strings.TrimSpace(actor.Role), auth.RoleAdmin)
}

// requestActorFromContextOK returns the actor and whether one was recorded on
// the context at all. Requests arriving through the RPC surface always have an
// actor recorded (possibly empty when unauthenticated); internal calls and
// tests that construct bare contexts do not.
func requestActorFromContextOK(ctx context.Context) (requestActor, bool) {
	if ctx == nil {
		return requestActor{}, false
	}
	actor, ok := ctx.Value(requestActorContextKey{}).(requestActor)
	if !ok {
		return requestActor{}, false
	}
	return actor, ok
}

func firstHeaderValue(header http.Header, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}
