package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// maxSoulContentLen bounds a user's SOUL document so a runaway paste cannot
// bloat the database or the persona prompt injected into ask_teammate runs.
const maxSoulContentLen = 16 * 1024

// GetMySoul returns the calling user's personal SOUL (empty when none is saved).
func (s *Server) GetMySoul(ctx context.Context, _ *platform.GetMySoulRequest) (*platform.Soul, error) {
	if s.authStore == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("auth store not configured"))
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	soul, err := s.authStore.GetUserSoul(ctx, actor.Subject)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get soul: %w", err))
	}
	return soulToProto(soul), nil
}

// UpdateMySoul saves the calling user's personal SOUL. The content is trimmed
// and bounded; an empty body clears the SOUL.
func (s *Server) UpdateMySoul(ctx context.Context, req *platform.UpdateMySoulRequest) (*platform.Soul, error) {
	if s.authStore == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("auth store not configured"))
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	content := strings.TrimSpace(req.GetContent())
	if len(content) > maxSoulContentLen {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("soul content exceeds %d bytes", maxSoulContentLen))
	}
	soul, err := s.authStore.UpsertUserSoul(ctx, &auth.UserSoul{
		UserID:  actor.Subject,
		Content: content,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update soul: %w", err))
	}
	return soulToProto(soul), nil
}

// soulToProto converts a stored UserSoul into its wire representation. A nil
// soul (never saved) maps to an empty message.
func soulToProto(soul *auth.UserSoul) *platform.Soul {
	out := &platform.Soul{}
	if soul == nil {
		return out
	}
	out.Content = soul.Content
	if !soul.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(soul.UpdatedAt)
	}
	return out
}
