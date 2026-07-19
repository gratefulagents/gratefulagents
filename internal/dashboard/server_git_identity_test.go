package dashboard

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// gitIdentityFakeStore is a stateful auth.Store fake for git identity handler
// tests. It embeds collaborationAuthStore for the rest of the interface and
// overrides only the git identity methods.
type gitIdentityFakeStore struct {
	collaborationAuthStore
	identity *auth.UserGitIdentity
	getErr   error
}

func (s *gitIdentityFakeStore) GetUserGitIdentity(_ context.Context, userID string) (*auth.UserGitIdentity, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.identity == nil || s.identity.UserID != userID {
		return nil, nil
	}
	return s.identity, nil
}

func (s *gitIdentityFakeStore) UpsertUserGitIdentity(_ context.Context, identity *auth.UserGitIdentity) (*auth.UserGitIdentity, error) {
	stored := *identity
	s.identity = &stored
	return &stored, nil
}

func gitIdentityActorContext(subject string) context.Context {
	return context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: subject})
}

func TestGetMyGitIdentityRequiresAuth(t *testing.T) {
	srv := &Server{authStore: &gitIdentityFakeStore{}}
	_, err := srv.GetMyGitIdentity(context.Background(), &platform.GetMyGitIdentityRequest{})
	if err == nil {
		t.Fatal("expected unauthenticated error for missing actor")
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestGetMyGitIdentityEmptyWhenNoneSaved(t *testing.T) {
	srv := &Server{authStore: &gitIdentityFakeStore{}}
	resp, err := srv.GetMyGitIdentity(gitIdentityActorContext("user-1"), &platform.GetMyGitIdentityRequest{})
	if err != nil {
		t.Fatalf("GetMyGitIdentity() error = %v", err)
	}
	if resp.GetName() != "" || resp.GetEmail() != "" {
		t.Fatalf("identity = %q <%q>, want empty", resp.GetName(), resp.GetEmail())
	}
	if resp.GetUpdatedAt() != nil {
		t.Fatalf("updatedAt = %v, want nil for never-saved", resp.GetUpdatedAt())
	}
}

func TestUpdateMyGitIdentityRoundTrip(t *testing.T) {
	store := &gitIdentityFakeStore{}
	srv := &Server{authStore: store}
	ctx := gitIdentityActorContext("user-42")

	resp, err := srv.UpdateMyGitIdentity(ctx, &platform.UpdateMyGitIdentityRequest{
		Name:  "  Alice Doe  ",
		Email: " alice@example.com ",
	})
	if err != nil {
		t.Fatalf("UpdateMyGitIdentity() error = %v", err)
	}
	if resp.GetName() != "Alice Doe" || resp.GetEmail() != "alice@example.com" {
		t.Fatalf("identity = %q <%q>, want trimmed values", resp.GetName(), resp.GetEmail())
	}
	got, err := srv.GetMyGitIdentity(ctx, &platform.GetMyGitIdentityRequest{})
	if err != nil {
		t.Fatalf("GetMyGitIdentity() error = %v", err)
	}
	if got.GetName() != "Alice Doe" || got.GetEmail() != "alice@example.com" {
		t.Fatalf("round-trip git settings = %+v", got)
	}
}

func TestUpdateMyGitIdentityClears(t *testing.T) {
	store := &gitIdentityFakeStore{identity: &auth.UserGitIdentity{
		UserID: "user-42", Name: "Alice", Email: "alice@example.com",
	}}
	srv := &Server{authStore: store}

	resp, err := srv.UpdateMyGitIdentity(gitIdentityActorContext("user-42"), &platform.UpdateMyGitIdentityRequest{})
	if err != nil {
		t.Fatalf("UpdateMyGitIdentity(clear) error = %v", err)
	}
	if resp.GetName() != "" || resp.GetEmail() != "" {
		t.Fatalf("identity after clear = %q <%q>, want empty", resp.GetName(), resp.GetEmail())
	}
}

func TestUpdateMyGitIdentityValidation(t *testing.T) {
	srv := &Server{authStore: &gitIdentityFakeStore{}}
	ctx := gitIdentityActorContext("user-1")

	cases := []struct {
		name  string
		req   *platform.UpdateMyGitIdentityRequest
		valid bool
	}{
		{"name without email", &platform.UpdateMyGitIdentityRequest{Name: "Alice"}, false},
		{"email without name", &platform.UpdateMyGitIdentityRequest{Email: "a@b.com"}, false},
		{"missing at sign", &platform.UpdateMyGitIdentityRequest{Name: "Alice", Email: "nope"}, false},
		{"trailing at sign", &platform.UpdateMyGitIdentityRequest{Name: "Alice", Email: "nope@"}, false},
		{"angle bracket in name", &platform.UpdateMyGitIdentityRequest{Name: "Alice <x>", Email: "a@b.com"}, false},
		{"space in email", &platform.UpdateMyGitIdentityRequest{Name: "Alice", Email: "a @b.com"}, false},
		{"newline in name", &platform.UpdateMyGitIdentityRequest{Name: "Alice\nBob", Email: "a@b.com"}, false},
		{"valid", &platform.UpdateMyGitIdentityRequest{Name: "Alice Doe", Email: "alice@example.com"}, true},
		{"clear", &platform.UpdateMyGitIdentityRequest{}, true},
	}
	for _, tc := range cases {
		_, err := srv.UpdateMyGitIdentity(ctx, tc.req)
		if tc.valid && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.valid {
			if err == nil {
				t.Errorf("%s: expected validation error", tc.name)
			} else if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("%s: code = %v, want InvalidArgument", tc.name, connect.CodeOf(err))
			}
		}
	}
}

func TestStampGitIdentityAnnotations(t *testing.T) {
	store := &gitIdentityFakeStore{identity: &auth.UserGitIdentity{
		UserID: "user-42", Name: "Alice Doe", Email: "alice@example.com",
	}}
	srv := &Server{authStore: store}

	run := &platformv1alpha1.AgentRun{}
	if err := srv.stampGitIdentityAnnotations(gitIdentityActorContext("user-42"), run); err != nil {
		t.Fatalf("stampGitIdentityAnnotations() error = %v", err)
	}
	if got := run.Annotations[platformv1alpha1.GitAuthorNameAnnotation]; got != "Alice Doe" {
		t.Fatalf("git author name annotation = %q, want Alice Doe", got)
	}
	if got := run.Annotations[platformv1alpha1.GitAuthorEmailAnnotation]; got != "alice@example.com" {
		t.Fatalf("git author email annotation = %q, want alice@example.com", got)
	}

	// Anonymous actor: untouched.
	run = &platformv1alpha1.AgentRun{}
	if err := srv.stampGitIdentityAnnotations(context.Background(), run); err != nil {
		t.Fatalf("anonymous stamp error = %v", err)
	}
	if len(run.Annotations) != 0 {
		t.Fatalf("annotations = %v, want none for anonymous actor", run.Annotations)
	}

	// No saved identity: untouched.
	run = &platformv1alpha1.AgentRun{}
	if err := srv.stampGitIdentityAnnotations(gitIdentityActorContext("user-other"), run); err != nil {
		t.Fatalf("unset settings stamp error = %v", err)
	}
	if len(run.Annotations) != 0 {
		t.Fatalf("annotations = %v, want none without saved identity", run.Annotations)
	}

	// Store errors prevent creation with incomplete settings.
	store.getErr = context.DeadlineExceeded
	run = &platformv1alpha1.AgentRun{}
	if err := srv.stampGitIdentityAnnotations(gitIdentityActorContext("user-42"), run); err == nil {
		t.Fatal("expected settings lookup error")
	}
	if len(run.Annotations) != 0 {
		t.Fatalf("annotations = %v, want none on store error", run.Annotations)
	}
}
