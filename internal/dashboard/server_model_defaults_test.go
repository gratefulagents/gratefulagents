package dashboard

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// modelDefaultsFakeStore is a stateful auth.Store fake for model defaults
// handler tests. It embeds collaborationAuthStore for the rest of the
// interface and adds the optional UserModelDefaultsStore methods.
type modelDefaultsFakeStore struct {
	collaborationAuthStore
	defaults *auth.UserModelDefaults
	getErr   error
}

func (s *modelDefaultsFakeStore) GetUserModelDefaults(_ context.Context, userID string) (*auth.UserModelDefaults, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.defaults == nil || s.defaults.UserID != userID {
		return nil, nil
	}
	return s.defaults, nil
}

func (s *modelDefaultsFakeStore) UpsertUserModelDefaults(_ context.Context, defaults *auth.UserModelDefaults) (*auth.UserModelDefaults, error) {
	stored := *defaults
	s.defaults = &stored
	return &stored, nil
}

func (s *modelDefaultsFakeStore) DeleteUserModelDefaults(_ context.Context, userID string) error {
	if s.defaults != nil && s.defaults.UserID == userID {
		s.defaults = nil
	}
	return nil
}

func modelDefaultsActorContext(subject string) context.Context {
	return context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: subject})
}

func TestGetMyModelDefaultsRequiresAuth(t *testing.T) {
	srv := &Server{authStore: &modelDefaultsFakeStore{}}
	_, err := srv.GetMyModelDefaults(context.Background(), &platform.GetMyModelDefaultsRequest{})
	if err == nil {
		t.Fatal("expected unauthenticated error for missing actor")
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}

	_, err = srv.UpdateMyModelDefaults(context.Background(), &platform.UpdateMyModelDefaultsRequest{})
	if err == nil {
		t.Fatal("expected unauthenticated error for missing actor on update")
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("update code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestGetMyModelDefaultsUnavailableWithoutStore(t *testing.T) {
	srv := &Server{}
	_, err := srv.GetMyModelDefaults(modelDefaultsActorContext("user-1"), &platform.GetMyModelDefaultsRequest{})
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("code = %v, want Unavailable", connect.CodeOf(err))
	}

	// An auth store without the optional interface is also unavailable.
	srv = &Server{authStore: &collaborationAuthStore{}}
	_, err = srv.GetMyModelDefaults(modelDefaultsActorContext("user-1"), &platform.GetMyModelDefaultsRequest{})
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("code without optional interface = %v, want Unavailable", connect.CodeOf(err))
	}
}

func TestGetMyModelDefaultsEmptyWhenNoneSaved(t *testing.T) {
	srv := &Server{authStore: &modelDefaultsFakeStore{}}
	resp, err := srv.GetMyModelDefaults(modelDefaultsActorContext("user-1"), &platform.GetMyModelDefaultsRequest{})
	if err != nil {
		t.Fatalf("GetMyModelDefaults() error = %v", err)
	}
	if resp.GetProvider() != "" || resp.GetModel() != "" || resp.GetReasoningLevel() != "" || resp.GetDisabled() {
		t.Fatalf("defaults = %+v, want empty", resp)
	}
	if resp.GetUpdatedAt() != nil {
		t.Fatalf("updatedAt = %v, want nil for never-saved", resp.GetUpdatedAt())
	}
}

func TestUpdateMyModelDefaultsRoundTrip(t *testing.T) {
	store := &modelDefaultsFakeStore{}
	srv := &Server{authStore: store}
	ctx := modelDefaultsActorContext("user-42")

	resp, err := srv.UpdateMyModelDefaults(ctx, &platform.UpdateMyModelDefaultsRequest{
		Provider:       " Anthropic ",
		AuthMode:       new(" OAUTH "),
		Model:          "  claude-sonnet-4-6  ",
		ReasoningLevel: " HIGH ",
	})
	if err != nil {
		t.Fatalf("UpdateMyModelDefaults() error = %v", err)
	}
	if resp.GetProvider() != "anthropic" || resp.GetAuthMode() != "oauth" || resp.GetModel() != "claude-sonnet-4-6" || resp.GetReasoningLevel() != "high" {
		t.Fatalf("defaults = %+v, want trimmed/normalized values", resp)
	}
	got, err := srv.GetMyModelDefaults(ctx, &platform.GetMyModelDefaultsRequest{})
	if err != nil {
		t.Fatalf("GetMyModelDefaults() error = %v", err)
	}
	if got.GetProvider() != "anthropic" || got.GetAuthMode() != "oauth" || got.GetModel() != "claude-sonnet-4-6" || got.GetReasoningLevel() != "high" || got.GetDisabled() {
		t.Fatalf("round-trip defaults = %+v", got)
	}
}

func TestUpdateMyModelDefaultsProviderOnlyAndReasoningOnly(t *testing.T) {
	srv := &Server{authStore: &modelDefaultsFakeStore{}}
	ctx := modelDefaultsActorContext("user-1")

	if _, err := srv.UpdateMyModelDefaults(ctx, &platform.UpdateMyModelDefaultsRequest{Provider: "openai"}); err != nil {
		t.Fatalf("provider-only update error = %v", err)
	}
	if _, err := srv.UpdateMyModelDefaults(ctx, &platform.UpdateMyModelDefaultsRequest{ReasoningLevel: "low"}); err != nil {
		t.Fatalf("reasoning-only update error = %v", err)
	}
}

func TestUpdateMyModelDefaultsLegacyClientPreservesAuthMode(t *testing.T) {
	store := &modelDefaultsFakeStore{defaults: &auth.UserModelDefaults{
		UserID: "user-1", Provider: "openai", AuthMode: "oauth", Model: "gpt-5",
	}}
	srv := &Server{authStore: store}

	// AuthMode is deliberately absent, as it is for clients generated before
	// the field was added.
	got, err := srv.UpdateMyModelDefaults(modelDefaultsActorContext("user-1"), &platform.UpdateMyModelDefaultsRequest{
		Provider: "openai", Model: "gpt-5-mini", ReasoningLevel: "low",
	})
	if err != nil {
		t.Fatalf("UpdateMyModelDefaults() error = %v", err)
	}
	if got.GetAuthMode() != "oauth" {
		t.Fatalf("auth mode = %q, want preserved oauth", got.GetAuthMode())
	}
}

func TestUpdateMyModelDefaultsValidation(t *testing.T) {
	srv := &Server{authStore: &modelDefaultsFakeStore{}}
	ctx := modelDefaultsActorContext("user-1")

	cases := []struct {
		name string
		req  *platform.UpdateMyModelDefaultsRequest
	}{
		{"invalid provider", &platform.UpdateMyModelDefaultsRequest{Provider: "totallyfake", Model: "m"}},
		{"invalid auth mode", &platform.UpdateMyModelDefaultsRequest{Provider: "openai", AuthMode: new("password")}},
		{"auth mode without provider", &platform.UpdateMyModelDefaultsRequest{AuthMode: new("oauth")}},
		{"copilot api key", &platform.UpdateMyModelDefaultsRequest{Provider: "copilot", AuthMode: new("api-key")}},
		{"openrouter oauth", &platform.UpdateMyModelDefaultsRequest{Provider: "openrouter", AuthMode: new("oauth")}},
		{"invalid reasoning level", &platform.UpdateMyModelDefaultsRequest{Provider: "openai", ReasoningLevel: "ultra"}},
		{"model without provider", &platform.UpdateMyModelDefaultsRequest{Model: "gpt-5.2"}},
	}
	for _, tc := range cases {
		_, err := srv.UpdateMyModelDefaults(ctx, tc.req)
		if err == nil {
			t.Errorf("%s: expected validation error", tc.name)
			continue
		}
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: code = %v, want InvalidArgument", tc.name, connect.CodeOf(err))
		}
	}
}

func TestUpdateMyModelDefaultsClears(t *testing.T) {
	store := &modelDefaultsFakeStore{defaults: &auth.UserModelDefaults{
		UserID: "user-42", Provider: "openai", Model: "gpt-5.2", ReasoningLevel: "high",
	}}
	srv := &Server{authStore: store}

	resp, err := srv.UpdateMyModelDefaults(modelDefaultsActorContext("user-42"), &platform.UpdateMyModelDefaultsRequest{})
	if err != nil {
		t.Fatalf("UpdateMyModelDefaults(clear) error = %v", err)
	}
	if resp.GetProvider() != "" || resp.GetModel() != "" || resp.GetReasoningLevel() != "" || resp.GetDisabled() {
		t.Fatalf("defaults after clear = %+v, want empty", resp)
	}
	if store.defaults != nil {
		t.Fatalf("stored defaults = %+v, want deleted record", store.defaults)
	}
}

func TestUpdateMyModelDefaultsDisabledPersists(t *testing.T) {
	store := &modelDefaultsFakeStore{}
	srv := &Server{authStore: store}
	ctx := modelDefaultsActorContext("user-42")

	if _, err := srv.UpdateMyModelDefaults(ctx, &platform.UpdateMyModelDefaultsRequest{
		Provider: "openai", Model: "gpt-5.2", ReasoningLevel: "medium", Disabled: true,
	}); err != nil {
		t.Fatalf("UpdateMyModelDefaults(disabled) error = %v", err)
	}
	got, err := srv.GetMyModelDefaults(ctx, &platform.GetMyModelDefaultsRequest{})
	if err != nil {
		t.Fatalf("GetMyModelDefaults() error = %v", err)
	}
	if !got.GetDisabled() || got.GetProvider() != "openai" || got.GetModel() != "gpt-5.2" || got.GetReasoningLevel() != "medium" {
		t.Fatalf("disabled defaults = %+v, want values kept with disabled=true", got)
	}

	// Disabled with all values empty is still a saved record, not a clear.
	if _, err := srv.UpdateMyModelDefaults(ctx, &platform.UpdateMyModelDefaultsRequest{Disabled: true}); err != nil {
		t.Fatalf("UpdateMyModelDefaults(disabled empty) error = %v", err)
	}
	if store.defaults == nil || !store.defaults.Disabled {
		t.Fatalf("stored defaults = %+v, want persisted disabled record", store.defaults)
	}
}
