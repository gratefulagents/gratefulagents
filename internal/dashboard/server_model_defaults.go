package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const maxUserModelDefaultsLength = 512

// validReasoningLevels mirrors REASONING_LEVELS in
// platform-app/frontend/src/lib/reasoning.ts.
var validReasoningLevels = map[string]bool{
	"": true, "none": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

func (s *Server) userModelDefaultsStore() (auth.UserModelDefaultsStore, error) {
	if s.authStore == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("auth store not configured"))
	}
	store, ok := s.authStore.(auth.UserModelDefaultsStore)
	if !ok {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("model defaults store not configured"))
	}
	return store, nil
}

// GetMyModelDefaults returns the calling user's personal model defaults
// (empty when none are saved).
func (s *Server) GetMyModelDefaults(ctx context.Context, _ *platform.GetMyModelDefaultsRequest) (*platform.ModelDefaults, error) {
	store, err := s.userModelDefaultsStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	defaults, err := store.GetUserModelDefaults(ctx, actor.Subject)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get model defaults: %w", err))
	}
	return modelDefaultsToProto(defaults), nil
}

// UpdateMyModelDefaults saves the calling user's personal model defaults.
// Sending every field empty with disabled=false clears the record.
func (s *Server) UpdateMyModelDefaults(ctx context.Context, req *platform.UpdateMyModelDefaultsRequest) (*platform.ModelDefaults, error) {
	store, err := s.userModelDefaultsStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	provider := strings.ToLower(strings.TrimSpace(req.GetProvider()))
	authMode := strings.ToLower(strings.TrimSpace(req.GetAuthMode()))
	model := strings.TrimSpace(req.GetModel())
	reasoningLevel := strings.ToLower(strings.TrimSpace(req.GetReasoningLevel()))
	if err := validateModelDefaults(provider, authMode, model, reasoningLevel); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if provider == "" && authMode == "" && model == "" && reasoningLevel == "" && !req.GetDisabled() {
		if err := store.DeleteUserModelDefaults(ctx, actor.Subject); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("clear model defaults: %w", err))
		}
		return &platform.ModelDefaults{}, nil
	}
	// Older clients predate auth_mode. Preserve an existing selection when such
	// a client replaces the other fields during a rolling upgrade.
	if req.AuthMode == nil {
		current, getErr := store.GetUserModelDefaults(ctx, actor.Subject)
		if getErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get model defaults: %w", getErr))
		}
		if current != nil && current.Provider == provider {
			authMode = current.AuthMode
		}
	}
	defaults, err := store.UpsertUserModelDefaults(ctx, &auth.UserModelDefaults{
		UserID:         actor.Subject,
		Provider:       provider,
		AuthMode:       authMode,
		Model:          model,
		ReasoningLevel: reasoningLevel,
		Disabled:       req.GetDisabled(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update model defaults: %w", err))
	}
	return modelDefaultsToProto(defaults), nil
}

// validateModelDefaults checks trimmed values. A provider without a model is
// allowed (default provider only), as is a reasoning level alone; a model
// without a provider is ambiguous and rejected.
func validateModelDefaults(provider, authMode, model, reasoningLevel string) error {
	if provider != "" {
		if _, err := resolveProvider(provider, ""); err != nil {
			return err
		}
	}
	if len(model) > maxUserModelDefaultsLength {
		return fmt.Errorf("model exceeds %d characters", maxUserModelDefaultsLength)
	}
	if model != "" && provider == "" {
		return fmt.Errorf("model requires a provider")
	}
	if authMode != "" && provider == "" {
		return fmt.Errorf("auth mode requires a provider")
	}
	if authMode != "" && authMode != "api-key" && authMode != "oauth" {
		return fmt.Errorf("unsupported auth mode %q (expected api-key or oauth)", authMode)
	}
	if provider == "copilot" && authMode != "" && authMode != "oauth" {
		return fmt.Errorf("copilot requires oauth auth mode")
	}
	if provider != "" && authMode != "" {
		if err := triggersv1alpha1.ValidateProviderAuthMode(provider, triggersv1alpha1.NormalizeAuthMode(authMode)); err != nil {
			return err
		}
	}
	if !validReasoningLevels[reasoningLevel] {
		return fmt.Errorf("unsupported reasoning level %q (expected none, low, medium, high, xhigh, or max)", reasoningLevel)
	}
	return nil
}

// modelDefaultsToProto converts stored UserModelDefaults into their wire
// representation. Nil (never saved or cleared) maps to an empty message.
func modelDefaultsToProto(defaults *auth.UserModelDefaults) *platform.ModelDefaults {
	out := &platform.ModelDefaults{}
	if defaults == nil {
		return out
	}
	out.Provider = defaults.Provider
	out.AuthMode = defaults.AuthMode
	out.Model = defaults.Model
	out.ReasoningLevel = defaults.ReasoningLevel
	out.Disabled = defaults.Disabled
	if !defaults.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(defaults.UpdatedAt)
	}
	return out
}
