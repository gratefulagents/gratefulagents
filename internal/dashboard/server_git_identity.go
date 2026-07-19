package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// Bounds for a git identity. Git itself accepts almost anything, so the caps
// only guard against runaway input; the character checks keep the values safe
// to embed in commit headers and pod env vars.
const (
	maxGitIdentityNameLen  = 200
	maxGitIdentityEmailLen = 254
)

// GetMyGitIdentity returns the calling user's git commit identity (empty when
// none is saved).
func (s *Server) GetMyGitIdentity(ctx context.Context, _ *platform.GetMyGitIdentityRequest) (*platform.GitIdentity, error) {
	if s.authStore == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("auth store not configured"))
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	identity, err := s.authStore.GetUserGitIdentity(ctx, actor.Subject)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get git identity: %w", err))
	}
	return gitIdentityToProto(identity), nil
}

// UpdateMyGitIdentity saves the calling user's git commit identity. Name and
// email must be provided together; sending both empty clears the identity so
// the user's runs fall back to the default agent identity.
func (s *Server) UpdateMyGitIdentity(ctx context.Context, req *platform.UpdateMyGitIdentityRequest) (*platform.GitIdentity, error) {
	if s.authStore == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("auth store not configured"))
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	name := strings.TrimSpace(req.GetName())
	email := strings.TrimSpace(req.GetEmail())
	if err := validateGitIdentity(name, email); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	identity, err := s.authStore.UpsertUserGitIdentity(ctx, &auth.UserGitIdentity{
		UserID: actor.Subject,
		Name:   name,
		Email:  email,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update git identity: %w", err))
	}
	return gitIdentityToProto(identity), nil
}

// validateGitIdentity checks a trimmed name/email pair. Both must be set
// together (or both empty, which clears the identity). The values end up in
// git commit headers and pod environment variables, so control characters and
// angle brackets are rejected.
func validateGitIdentity(name, email string) error {
	if name == "" && email == "" {
		return nil
	}
	if name == "" || email == "" {
		return fmt.Errorf("git identity requires both name and email (send both empty to clear)")
	}
	if len(name) > maxGitIdentityNameLen {
		return fmt.Errorf("git identity name exceeds %d characters", maxGitIdentityNameLen)
	}
	if len(email) > maxGitIdentityEmailLen {
		return fmt.Errorf("git identity email exceeds %d characters", maxGitIdentityEmailLen)
	}
	if strings.ContainsAny(name, "<>\n\r") || hasControlChars(name) {
		return fmt.Errorf("git identity name must not contain control characters or angle brackets")
	}
	if strings.ContainsAny(email, "<>\n\r ") || hasControlChars(email) {
		return fmt.Errorf("git identity email must not contain spaces, control characters, or angle brackets")
	}
	at := strings.Index(email, "@")
	if at <= 0 || at == len(email)-1 {
		return fmt.Errorf("git identity email %q is not a valid address", email)
	}
	return nil
}

func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// stampGitIdentityAnnotations copies the calling user's saved author identity
// onto a run being created.
func (s *Server) stampGitIdentityAnnotations(ctx context.Context, run *platformv1alpha1.AgentRun) error {
	if s.authStore == nil || run == nil {
		return nil
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil
	}
	identity, err := s.authStore.GetUserGitIdentity(ctx, actor.Subject)
	if err != nil {
		return fmt.Errorf("load git settings for user %s: %w", actor.Subject, err)
	}
	if identity == nil {
		return nil
	}
	name := strings.TrimSpace(identity.Name)
	email := strings.TrimSpace(identity.Email)
	if name == "" && email == "" {
		return nil
	}
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	if name != "" && email != "" {
		run.Annotations[platformv1alpha1.GitAuthorNameAnnotation] = name
		run.Annotations[platformv1alpha1.GitAuthorEmailAnnotation] = email
	}
	return nil
}

// gitIdentityToProto converts a stored UserGitIdentity into its wire
// representation. A nil identity (never saved) maps to an empty message.
func gitIdentityToProto(identity *auth.UserGitIdentity) *platform.GitIdentity {
	out := &platform.GitIdentity{}
	if identity == nil {
		return out
	}
	out.Name = identity.Name
	out.Email = identity.Email
	if !identity.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(identity.UpdatedAt)
	}
	return out
}
