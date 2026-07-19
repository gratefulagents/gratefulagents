package dashboard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// soulFakeStore is a stateful auth.Store fake for SOUL handler tests. It embeds
// collaborationAuthStore for the rest of the interface and overrides only the
// SOUL methods.
type soulFakeStore struct {
	collaborationAuthStore
	soul      *auth.UserSoul
	getErr    error
	upsertErr error
}

func (s *soulFakeStore) GetUserSoul(_ context.Context, userID string) (*auth.UserSoul, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.soul == nil || s.soul.UserID != userID {
		return nil, nil
	}
	return s.soul, nil
}

func (s *soulFakeStore) UpsertUserSoul(_ context.Context, soul *auth.UserSoul) (*auth.UserSoul, error) {
	if s.upsertErr != nil {
		return nil, s.upsertErr
	}
	stored := *soul
	s.soul = &stored
	return &stored, nil
}

func soulActorContext(subject string) context.Context {
	return context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: subject})
}

func TestGetMySoulRequiresAuth(t *testing.T) {
	srv := &Server{authStore: &soulFakeStore{}}
	_, err := srv.GetMySoul(context.Background(), &platform.GetMySoulRequest{})
	if err == nil {
		t.Fatal("expected unauthenticated error for missing actor")
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestGetMySoulEmptyWhenNoneSaved(t *testing.T) {
	srv := &Server{authStore: &soulFakeStore{}}
	resp, err := srv.GetMySoul(soulActorContext("user-1"), &platform.GetMySoulRequest{})
	if err != nil {
		t.Fatalf("GetMySoul() error = %v", err)
	}
	if resp.GetContent() != "" {
		t.Fatalf("content = %q, want empty", resp.GetContent())
	}
	if resp.GetUpdatedAt() != nil {
		t.Fatalf("updatedAt = %v, want nil for never-saved", resp.GetUpdatedAt())
	}
}

func TestUpdateMySoulRoundTrip(t *testing.T) {
	store := &soulFakeStore{}
	srv := &Server{authStore: store}
	ctx := soulActorContext("user-42")

	_, err := srv.UpdateMySoul(ctx, &platform.UpdateMySoulRequest{Content: "  # I review for tests  "})
	if err != nil {
		t.Fatalf("UpdateMySoul() error = %v", err)
	}
	if store.soul == nil || store.soul.UserID != "user-42" {
		t.Fatalf("soul not stored for actor: %+v", store.soul)
	}
	if store.soul.Content != "# I review for tests" {
		t.Fatalf("content = %q, want trimmed value", store.soul.Content)
	}

	got, err := srv.GetMySoul(ctx, &platform.GetMySoulRequest{})
	if err != nil {
		t.Fatalf("GetMySoul() error = %v", err)
	}
	if got.GetContent() != "# I review for tests" {
		t.Fatalf("round-trip content = %q", got.GetContent())
	}
}

func TestUpdateMySoulRejectsOversizeContent(t *testing.T) {
	srv := &Server{authStore: &soulFakeStore{}}
	big := strings.Repeat("x", maxSoulContentLen+1)
	_, err := srv.UpdateMySoul(soulActorContext("user-1"), &platform.UpdateMySoulRequest{Content: big})
	if err == nil {
		t.Fatal("expected invalid-argument error for oversize content")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

func TestUpdateMySoulRequiresAuth(t *testing.T) {
	srv := &Server{authStore: &soulFakeStore{}}
	_, err := srv.UpdateMySoul(context.Background(), &platform.UpdateMySoulRequest{Content: "x"})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestSoulHandlersUnavailableWithoutStore(t *testing.T) {
	srv := &Server{}
	if _, err := srv.GetMySoul(soulActorContext("u"), &platform.GetMySoulRequest{}); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("GetMySoul code = %v, want Unavailable", connect.CodeOf(err))
	}
	if _, err := srv.UpdateMySoul(soulActorContext("u"), &platform.UpdateMySoulRequest{}); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("UpdateMySoul code = %v, want Unavailable", connect.CodeOf(err))
	}
}

func TestUpdateMySoulPropagatesStoreError(t *testing.T) {
	srv := &Server{authStore: &soulFakeStore{upsertErr: errors.New("boom")}}
	_, err := srv.UpdateMySoul(soulActorContext("u"), &platform.UpdateMySoulRequest{Content: "x"})
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code = %v, want Internal", connect.CodeOf(err))
	}
}
