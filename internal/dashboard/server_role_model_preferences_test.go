package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

type roleModelPreferenceFakeStore struct {
	collaborationAuthStore
	byUser map[string][]*auth.UserRoleModelPreference
}

func (s *roleModelPreferenceFakeStore) ListUserRoleModelPreferences(_ context.Context, userID string) ([]*auth.UserRoleModelPreference, error) {
	return cloneRoleModelPreferences(s.byUser[userID]), nil
}

func (s *roleModelPreferenceFakeStore) ReplaceUserRoleModelPreferences(_ context.Context, userID string, preferences []*auth.UserRoleModelPreference) ([]*auth.UserRoleModelPreference, error) {
	if s.byUser == nil {
		s.byUser = map[string][]*auth.UserRoleModelPreference{}
	}
	now := time.Now()
	stored := cloneRoleModelPreferences(preferences)
	for _, preference := range stored {
		preference.UserID = userID
		preference.UpdatedAt = now
	}
	s.byUser[userID] = stored
	return cloneRoleModelPreferences(stored), nil
}

func cloneRoleModelPreferences(in []*auth.UserRoleModelPreference) []*auth.UserRoleModelPreference {
	out := make([]*auth.UserRoleModelPreference, 0, len(in))
	for _, preference := range in {
		if preference != nil {
			clone := *preference
			out = append(out, &clone)
		}
	}
	return out
}

func roleModelPreferenceServer(t *testing.T, store *roleModelPreferenceFakeStore) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&platformv1alpha1.RoleInstruction{ObjectMeta: metav1.ObjectMeta{Name: "explore"}, Spec: platformv1alpha1.RoleInstructionSpec{Instructions: "explore"}},
		&platformv1alpha1.RoleInstruction{ObjectMeta: metav1.ObjectMeta{Name: "analyst"}, Spec: platformv1alpha1.RoleInstructionSpec{Instructions: "analyze"}},
	).Build()
	return &Server{k8sClient: client, scheme: scheme, authStore: store}
}

func roleModelActor(subject string) context.Context {
	return context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: subject, Role: "member"})
}

func TestMyRoleModelPreferencesAreNormalizedAndIsolated(t *testing.T) {
	store := &roleModelPreferenceFakeStore{}
	srv := roleModelPreferenceServer(t, store)

	updated, err := srv.UpdateMyRoleModelPreferences(roleModelActor("user-a"), &platform.UpdateMyRoleModelPreferencesRequest{Preferences: []*platform.RoleModelPreference{
		{RoleName: " explore ", Provider: " OpenAI ", Model: " gpt-5.4-mini "},
		{RoleName: "analyst", Provider: "anthropic", Model: "claude-opus-4-6"},
	}})
	if err != nil {
		t.Fatalf("UpdateMyRoleModelPreferences: %v", err)
	}
	if len(updated.Preferences) != 2 || updated.Preferences[0].RoleName != "analyst" || updated.Preferences[1].Model != "gpt-5.4-mini" {
		t.Fatalf("normalized preferences = %#v", updated.Preferences)
	}
	other, err := srv.GetMyRoleModelPreferences(roleModelActor("user-b"), &platform.GetMyRoleModelPreferencesRequest{})
	if err != nil {
		t.Fatalf("GetMyRoleModelPreferences(other): %v", err)
	}
	if len(other.Preferences) != 0 {
		t.Fatalf("other user saw preferences: %#v", other.Preferences)
	}
}

func TestMyRoleModelPreferencesRejectUnknownAndDuplicateEntries(t *testing.T) {
	srv := roleModelPreferenceServer(t, &roleModelPreferenceFakeStore{})
	ctx := roleModelActor("user-a")
	for _, values := range [][]*platform.RoleModelPreference{
		{{RoleName: "missing", Provider: "openai", Model: "model"}},
		{{RoleName: "explore", Provider: "openai", Model: "a"}, {RoleName: "explore", Provider: "OpenAI", Model: "b"}},
		{{RoleName: "explore", Provider: "openai", Model: " "}},
		{{RoleName: "explore", Provider: " ", Model: "model"}},
		{{RoleName: "explore", Provider: "openai", Model: strings.Repeat("m", maxUserRoleModelLength+1)}},
	} {
		_, err := srv.UpdateMyRoleModelPreferences(ctx, &platform.UpdateMyRoleModelPreferencesRequest{Preferences: values})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("UpdateMyRoleModelPreferences(%#v) error = %v, want InvalidArgument", values, err)
		}
	}
}

func TestMyRoleModelPreferencesRequireAuthentication(t *testing.T) {
	srv := roleModelPreferenceServer(t, &roleModelPreferenceFakeStore{})
	if _, err := srv.GetMyRoleModelPreferences(context.Background(), &platform.GetMyRoleModelPreferencesRequest{}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("GetMyRoleModelPreferences error = %v, want Unauthenticated", err)
	}
}

func TestStampRoleModelOverridesSnapshotsCreatorPreferences(t *testing.T) {
	store := &roleModelPreferenceFakeStore{byUser: map[string][]*auth.UserRoleModelPreference{
		"user-a": {{UserID: "user-a", RoleName: "explore", Provider: "openai", Model: "gpt-5.6-terra"}},
	}}
	srv := roleModelPreferenceServer(t, store)
	run := &platformv1alpha1.AgentRun{}
	if err := srv.stampRoleModelOverrides(roleModelActor("user-a"), run); err != nil {
		t.Fatalf("stampRoleModelOverrides: %v", err)
	}
	if len(run.Spec.RoleModelOverrides) != 1 || run.Spec.RoleModelOverrides[0].Role != "explore" || run.Spec.RoleModelOverrides[0].ModelsByProvider["openai"] != "gpt-5.6-terra" {
		t.Fatalf("role model snapshot = %#v", run.Spec.RoleModelOverrides)
	}
}
