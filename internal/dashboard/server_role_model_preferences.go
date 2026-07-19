package dashboard

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	maxUserRoleModelPreferences = 100
	maxUserRoleModelLength      = 512
)

func (s *Server) userRoleModelStore() (auth.UserRoleModelStore, error) {
	if s.authStore == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("auth store not configured"))
	}
	store, ok := s.authStore.(auth.UserRoleModelStore)
	if !ok {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("personal role model store not configured"))
	}
	return store, nil
}

// GetMyRoleModelPreferences returns only the calling user's personal role model
// overrides. Platform defaults remain available through ListRoleInstructions.
func (s *Server) GetMyRoleModelPreferences(ctx context.Context, _ *platform.GetMyRoleModelPreferencesRequest) (*platform.RoleModelPreferences, error) {
	store, err := s.userRoleModelStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	preferences, err := store.ListUserRoleModelPreferences(ctx, actor.Subject)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get personal role models: %w", err))
	}
	return roleModelPreferencesToProto(preferences), nil
}

// UpdateMyRoleModelPreferences atomically replaces the calling user's personal
// role model overrides. No user identifier is accepted from the request.
func (s *Server) UpdateMyRoleModelPreferences(ctx context.Context, req *platform.UpdateMyRoleModelPreferencesRequest) (*platform.RoleModelPreferences, error) {
	store, err := s.userRoleModelStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	preferences, err := s.normalizeUserRoleModelPreferences(ctx, actor.Subject, req.GetPreferences())
	if err != nil {
		return nil, err
	}
	stored, err := store.ReplaceUserRoleModelPreferences(ctx, actor.Subject, preferences)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update personal role models: %w", err))
	}
	return roleModelPreferencesToProto(stored), nil
}

func (s *Server) normalizeUserRoleModelPreferences(ctx context.Context, userID string, values []*platform.RoleModelPreference) ([]*auth.UserRoleModelPreference, error) {
	if len(values) > maxUserRoleModelPreferences {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most %d role model preferences are allowed", maxUserRoleModelPreferences))
	}
	var catalog platformv1alpha1.RoleInstructionList
	if err := s.k8sClient.List(ctx, &catalog); err != nil {
		return nil, mapK8sError("list RoleInstructions", err)
	}
	knownRoles := make(map[string]struct{}, len(catalog.Items))
	for i := range catalog.Items {
		knownRoles[catalog.Items[i].Name] = struct{}{}
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]*auth.UserRoleModelPreference, 0, len(values))
	for _, value := range values {
		if value == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("role model preference must not be null"))
		}
		roleName := strings.ToLower(strings.TrimSpace(value.GetRoleName()))
		if err := validateResourceName(roleName); err != nil {
			return nil, err
		}
		if _, ok := knownRoles[roleName]; !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown role %q", roleName))
		}
		if strings.TrimSpace(value.GetProvider()) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider for role %q must not be empty", roleName))
		}
		provider, err := resolveProvider(value.GetProvider(), "")
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		model := strings.TrimSpace(value.GetModel())
		if model == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("model for role %q provider %q must not be empty", roleName, provider))
		}
		if len(model) > maxUserRoleModelLength {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("model for role %q provider %q exceeds %d bytes", roleName, provider, maxUserRoleModelLength))
		}
		key := roleName + "\x00" + provider
		if _, ok := seen[key]; ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("role %q provider %q is duplicated", roleName, provider))
		}
		seen[key] = struct{}{}
		out = append(out, &auth.UserRoleModelPreference{UserID: userID, RoleName: roleName, Provider: provider, Model: model})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RoleName == out[j].RoleName {
			return out[i].Provider < out[j].Provider
		}
		return out[i].RoleName < out[j].RoleName
	})
	return out, nil
}

func roleModelPreferencesToProto(preferences []*auth.UserRoleModelPreference) *platform.RoleModelPreferences {
	out := &platform.RoleModelPreferences{}
	var updatedAt time.Time
	for _, preference := range preferences {
		if preference == nil {
			continue
		}
		out.Preferences = append(out.Preferences, &platform.RoleModelPreference{
			RoleName: preference.RoleName,
			Provider: preference.Provider,
			Model:    preference.Model,
		})
		if preference.UpdatedAt.After(updatedAt) {
			updatedAt = preference.UpdatedAt
		}
	}
	if !updatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(updatedAt)
	}
	return out
}

// stampRoleModelOverrides snapshots personal preferences onto a run so workers
// never need database access and shared-namespace runs still use their creator's
// choices. Authenticated creation fails closed when the snapshot cannot be read.
func (s *Server) stampRoleModelOverrides(ctx context.Context, run *platformv1alpha1.AgentRun) error {
	if run == nil {
		return nil
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil
	}
	store, ok := s.authStore.(auth.UserRoleModelStore)
	if !ok {
		return nil // alternate/legacy auth stores have no personal preferences
	}
	preferences, err := store.ListUserRoleModelPreferences(ctx, actor.Subject)
	if err != nil {
		log.Printf("WARN: failed to load personal role models for user %s: %v", actor.Subject, err)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("snapshot personal role models: %w", err))
	}
	byRole := make(map[string]map[string]string)
	for _, preference := range preferences {
		if preference == nil || strings.TrimSpace(preference.RoleName) == "" || strings.TrimSpace(preference.Provider) == "" || strings.TrimSpace(preference.Model) == "" {
			continue
		}
		models := byRole[preference.RoleName]
		if models == nil {
			models = map[string]string{}
			byRole[preference.RoleName] = models
		}
		models[preference.Provider] = preference.Model
	}
	roles := make([]string, 0, len(byRole))
	for role := range byRole {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		run.Spec.RoleModelOverrides = append(run.Spec.RoleModelOverrides, platformv1alpha1.AgentRunRoleModelOverride{
			Role: role, ModelsByProvider: byRole[role],
		})
	}
	return nil
}
