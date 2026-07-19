package auth

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	authpb "github.com/gratefulagents/gratefulagents/rpc/auth"
)

// adminFakeStore is a minimal in-memory Store for exercising the admin
// user-management handlers.
type adminFakeStore struct {
	Store // panics on unimplemented methods

	users   map[string]*User
	order   []string
	deleted []string
}

func newAdminFakeStore(users ...*User) *adminFakeStore {
	s := &adminFakeStore{users: map[string]*User{}}
	for _, u := range users {
		s.users[u.ID] = u
		s.order = append(s.order, u.ID)
	}
	return s
}

func (s *adminFakeStore) ListUsers(context.Context) ([]*User, error) {
	var out []*User
	for _, id := range s.order {
		if u, ok := s.users[id]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *adminFakeStore) GetUserByID(_ context.Context, id string) (*User, error) {
	u, ok := s.users[id]
	if !ok {
		return nil, context.Canceled
	}
	return u, nil
}

func (s *adminFakeStore) SetUserRole(_ context.Context, userID, role string) error {
	if u, ok := s.users[userID]; ok {
		u.Role = role
	}
	return nil
}

func (s *adminFakeStore) DeleteUser(_ context.Context, userID string) error {
	if _, ok := s.users[userID]; !ok {
		return context.Canceled
	}
	delete(s.users, userID)
	s.deleted = append(s.deleted, userID)
	return nil
}

func adminCtx(sub string) context.Context {
	return WithClaims(context.Background(), &Claims{Sub: sub, Role: RoleAdmin})
}

func memberCtx(sub string) context.Context {
	return WithClaims(context.Background(), &Claims{Sub: sub, Role: RoleMember})
}

func newAdminTestServer(store Store) *Server {
	return NewServer(store, nil, nil, nil)
}

func TestListUsersRequiresAdmin(t *testing.T) {
	srv := newAdminTestServer(newAdminFakeStore())

	if _, err := srv.ListUsers(context.Background(), connect.NewRequest(&authpb.ListUsersRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated: got %v, want CodeUnauthenticated", err)
	}
	if _, err := srv.ListUsers(memberCtx("u1"), connect.NewRequest(&authpb.ListUsersRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("member: got %v, want CodePermissionDenied", err)
	}
}

func TestListUsersReturnsAllUsersWithLastLogin(t *testing.T) {
	lastLogin := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	created := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newAdminFakeStore(
		&User{ID: "u1", Username: "alice", Role: RoleAdmin, CreatedAt: created, LastLoginAt: &lastLogin},
		&User{ID: "u2", Username: "bob", Role: RoleMember, CreatedAt: created},
	)
	srv := newAdminTestServer(store)

	resp, err := srv.ListUsers(adminCtx("u1"), connect.NewRequest(&authpb.ListUsersRequest{}))
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(resp.Msg.Users) != 2 {
		t.Fatalf("got %d users, want 2", len(resp.Msg.Users))
	}
	if got := resp.Msg.Users[0].LastLoginAt; got != lastLogin.Unix() {
		t.Errorf("alice last_login_at = %d, want %d", got, lastLogin.Unix())
	}
	if got := resp.Msg.Users[1].LastLoginAt; got != 0 {
		t.Errorf("bob last_login_at = %d, want 0 (never logged in)", got)
	}
	if got := resp.Msg.Users[0].CreatedAt; got != created.Unix() {
		t.Errorf("created_at = %d, want %d", got, created.Unix())
	}
}

func TestUpdateUserRolePromotesToAdmin(t *testing.T) {
	store := newAdminFakeStore(
		&User{ID: "u1", Username: "alice", Role: RoleAdmin},
		&User{ID: "u2", Username: "bob", Role: RoleMember},
	)
	srv := newAdminTestServer(store)

	resp, err := srv.UpdateUserRole(adminCtx("u1"), connect.NewRequest(&authpb.UpdateUserRoleRequest{UserId: "u2", Role: RoleAdmin}))
	if err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}
	if resp.Msg.Role != RoleAdmin {
		t.Errorf("role = %q, want admin", resp.Msg.Role)
	}
	if store.users["u2"].Role != RoleAdmin {
		t.Errorf("stored role = %q, want admin", store.users["u2"].Role)
	}
}

func TestUpdateUserRoleGuards(t *testing.T) {
	store := newAdminFakeStore(&User{ID: "u1", Username: "alice", Role: RoleAdmin})
	srv := newAdminTestServer(store)

	// Invalid role rejected.
	if _, err := srv.UpdateUserRole(adminCtx("u1"), connect.NewRequest(&authpb.UpdateUserRoleRequest{UserId: "u1", Role: "root"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("invalid role: got %v, want CodeInvalidArgument", err)
	}
	// Self-demotion rejected.
	if _, err := srv.UpdateUserRole(adminCtx("u1"), connect.NewRequest(&authpb.UpdateUserRoleRequest{UserId: "u1", Role: RoleMember})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("self demotion: got %v, want CodeFailedPrecondition", err)
	}
	// Non-admin rejected.
	if _, err := srv.UpdateUserRole(memberCtx("u1"), connect.NewRequest(&authpb.UpdateUserRoleRequest{UserId: "u1", Role: RoleAdmin})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("member caller: got %v, want CodePermissionDenied", err)
	}
}

func TestDeleteUser(t *testing.T) {
	store := newAdminFakeStore(
		&User{ID: "u1", Username: "alice", Role: RoleAdmin},
		&User{ID: "u2", Username: "bob", Role: RoleMember},
	)
	srv := newAdminTestServer(store)

	// Self-deletion rejected.
	if _, err := srv.DeleteUser(adminCtx("u1"), connect.NewRequest(&authpb.DeleteUserRequest{UserId: "u1"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("self delete: got %v, want CodeFailedPrecondition", err)
	}
	// Non-admin rejected.
	if _, err := srv.DeleteUser(memberCtx("u2"), connect.NewRequest(&authpb.DeleteUserRequest{UserId: "u1"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("member caller: got %v, want CodePermissionDenied", err)
	}
	// Happy path.
	if _, err := srv.DeleteUser(adminCtx("u1"), connect.NewRequest(&authpb.DeleteUserRequest{UserId: "u2"})); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "u2" {
		t.Errorf("deleted = %v, want [u2]", store.deleted)
	}
	// Deleting again → not found.
	if _, err := srv.DeleteUser(adminCtx("u1"), connect.NewRequest(&authpb.DeleteUserRequest{UserId: "u2"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("missing user: got %v, want CodeNotFound", err)
	}
}
