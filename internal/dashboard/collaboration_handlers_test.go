package dashboard

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

type collaborationStateStore struct {
	*mockStateStore

	getResourceOwnerErr   error
	shares                map[string]*store.ResourceShare
	getShareByIDErr       error
	revokeShareErr        error
	updateShareErr        error
	createNotificationErr error
	notifications         []*store.Notification
}

func newCollaborationStateStore() *collaborationStateStore {
	return &collaborationStateStore{
		mockStateStore: newMockStateStore(),
		shares:         make(map[string]*store.ResourceShare),
	}
}

func (m *collaborationStateStore) GetResourceOwner(ctx context.Context, resourceType, resourceID, resourceNamespace string) (*store.ResourceOwnership, error) {
	if m.getResourceOwnerErr != nil {
		return nil, m.getResourceOwnerErr
	}
	return m.mockStateStore.GetResourceOwner(ctx, resourceType, resourceID, resourceNamespace)
}

func (m *collaborationStateStore) ShareResource(_ context.Context, sh *store.ResourceShare) (*store.ResourceShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *sh
	if cp.ID == "" {
		cp.ID = "share-" + sh.SharedWithUserID
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	if m.shares == nil {
		m.shares = make(map[string]*store.ResourceShare)
	}
	m.shares[cp.ID] = &cp
	return &cp, nil
}

func (m *collaborationStateStore) RevokeShare(_ context.Context, shareID string) error {
	if m.revokeShareErr != nil {
		return m.revokeShareErr
	}
	delete(m.shares, shareID)
	return nil
}

func (m *collaborationStateStore) UpdateSharePermission(_ context.Context, shareID, permission string) error {
	if m.updateShareErr != nil {
		return m.updateShareErr
	}
	if sh := m.shares[shareID]; sh != nil {
		sh.Permission = permission
		return nil
	}
	return pgx.ErrNoRows
}

func (m *collaborationStateStore) ListSharedWithMe(_ context.Context, userID, resourceType string) ([]store.ResourceShare, error) {
	var out []store.ResourceShare
	for _, sh := range m.shares {
		if sh.SharedWithUserID == userID && (resourceType == "" || sh.ResourceType == resourceType) {
			out = append(out, *sh)
		}
	}
	return out, nil
}

func (m *collaborationStateStore) ListSharesForResource(_ context.Context, resourceType, resourceID, resourceNamespace string) ([]store.ResourceShare, error) {
	var out []store.ResourceShare
	for _, sh := range m.shares {
		if sh.ResourceType == resourceType && sh.ResourceID == resourceID && sh.ResourceNamespace == resourceNamespace {
			out = append(out, *sh)
		}
	}
	return out, nil
}

func (m *collaborationStateStore) GetSharePermission(_ context.Context, resourceType, resourceID, resourceNamespace, userID string) (*store.ResourceShare, error) {
	for _, sh := range m.shares {
		if sh.ResourceType == resourceType && sh.ResourceID == resourceID && sh.ResourceNamespace == resourceNamespace && sh.SharedWithUserID == userID {
			cp := *sh
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *collaborationStateStore) GetShareByID(_ context.Context, shareID string) (*store.ResourceShare, error) {
	if m.getShareByIDErr != nil {
		return nil, m.getShareByIDErr
	}
	sh := m.shares[shareID]
	if sh == nil {
		return nil, pgx.ErrNoRows
	}
	cp := *sh
	return &cp, nil
}

func (m *collaborationStateStore) ListResourceOwnersByType(ctx context.Context, resourceType string) ([]store.ResourceOwnership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.ResourceOwnership
	for _, owner := range m.owners {
		if owner.ResourceType == resourceType {
			out = append(out, *owner)
		}
	}
	return out, nil
}

func (m *collaborationStateStore) GetLatestActivityBySessions(_ context.Context, sessionIDs []uuid.UUID) (map[uuid.UUID]store.ActivityEvent, error) {
	out := make(map[uuid.UUID]store.ActivityEvent)
	for _, sessionID := range sessionIDs {
		events := m.getRecentActivityBySession[sessionID]
		if len(events) > 0 {
			out[sessionID] = events[0]
		}
	}
	return out, nil
}

func (m *collaborationStateStore) CreateNotification(_ context.Context, n *store.Notification) error {
	if m.createNotificationErr != nil {
		return m.createNotificationErr
	}
	cp := *n
	m.notifications = append(m.notifications, &cp)
	return nil
}

type collaborationAuthStore struct {
	users []*auth.User
}

func (s *collaborationAuthStore) UpsertUser(context.Context, *auth.User) (*auth.User, error) {
	return nil, nil
}
func (s *collaborationAuthStore) GetUserByID(_ context.Context, id string) (*auth.User, error) {
	for _, u := range s.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, pgx.ErrNoRows
}
func (s *collaborationAuthStore) GetUserByUsername(context.Context, string) (*auth.User, error) {
	return nil, nil
}
func (s *collaborationAuthStore) GetUserByGoogleID(context.Context, string) (*auth.User, error) {
	return nil, nil
}
func (s *collaborationAuthStore) SearchUsers(_ context.Context, query string, _ int32) ([]*auth.User, error) {
	var out []*auth.User
	for _, u := range s.users {
		if u.Email == query {
			out = append(out, u)
		}
	}
	return out, nil
}
func (s *collaborationAuthStore) SetUserRole(context.Context, string, string) error { return nil }
func (s *collaborationAuthStore) ListUsers(context.Context) ([]*auth.User, error) {
	return s.users, nil
}
func (s *collaborationAuthStore) DeleteUser(context.Context, string) error         { return nil }
func (s *collaborationAuthStore) TouchUserLastLogin(context.Context, string) error { return nil }
func (s *collaborationAuthStore) GetUserNamespace(context.Context, string) (string, error) {
	return "", nil
}
func (s *collaborationAuthStore) SetUserNamespace(context.Context, string, string) error {
	return nil
}
func (s *collaborationAuthStore) GetUserSoul(context.Context, string) (*auth.UserSoul, error) {
	return nil, nil
}
func (s *collaborationAuthStore) UpsertUserSoul(_ context.Context, soul *auth.UserSoul) (*auth.UserSoul, error) {
	return soul, nil
}
func (s *collaborationAuthStore) GetUserGitIdentity(context.Context, string) (*auth.UserGitIdentity, error) {
	return nil, nil
}
func (s *collaborationAuthStore) UpsertUserGitIdentity(_ context.Context, identity *auth.UserGitIdentity) (*auth.UserGitIdentity, error) {
	return identity, nil
}
func (s *collaborationAuthStore) CreateSession(context.Context, *auth.Session) error { return nil }
func (s *collaborationAuthStore) GetSessionByTokenHash(context.Context, string) (*auth.Session, error) {
	return nil, nil
}
func (s *collaborationAuthStore) DeleteSession(context.Context, string) error { return nil }
func (s *collaborationAuthStore) DeleteExpiredSessions(context.Context) error { return nil }
func (s *collaborationAuthStore) RotateSession(context.Context, string, *auth.Session) error {
	return nil
}

func addCollaborationOwner(t *testing.T, ms *collaborationStateStore, resourceType, namespace, id, owner string) {
	t.Helper()
	if err := ms.SetResourceOwner(context.Background(), resourceType, id, namespace, owner); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}
}

func addCollaborationShare(ms *collaborationStateStore, id, resourceType, namespace, resourceID, withUser, byUser, permission string) {
	ms.shares[id] = &store.ResourceShare{
		ID:                id,
		ResourceType:      resourceType,
		ResourceID:        resourceID,
		ResourceNamespace: namespace,
		SharedWithUserID:  withUser,
		SharedByUserID:    byUser,
		Permission:        permission,
		CreatedAt:         time.Now(),
	}
}

func requireConnectCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Fatalf("want nil error, got %v", err)
		}
		return
	}
	if got := connect.CodeOf(err); got != want {
		t.Fatalf("code = %v, want %v (err %v)", got, want, err)
	}
}

func TestRequireResourceAccessMatrix(t *testing.T) {
	errBoom := errors.New("boom")
	cases := []struct {
		name       string
		ctx        context.Context
		stateStore *collaborationStateStore
		min        ResourceAccessLevel
		setup      func(*testing.T, *collaborationStateStore)
		wantCode   connect.Code
	}{
		{
			name:       "no actor recorded allowed as internal call",
			ctx:        context.Background(),
			stateStore: newCollaborationStateStore(),
			min:        AccessCollaborator,
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
			},
		},
		{
			name:       "admin role allowed",
			ctx:        actorContext("root", "admin", "", ""),
			stateStore: newCollaborationStateStore(),
			min:        AccessCollaborator,
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
			},
		},
		{
			name:     "authenticated with nil state store allowed",
			ctx:      actorContext("bob", "member", "", ""),
			min:      AccessCollaborator,
			wantCode: 0,
		},
		{
			name:       "unowned resource allowed",
			ctx:        actorContext("bob", "member", "", ""),
			stateStore: newCollaborationStateStore(),
			min:        AccessCollaborator,
		},
		{
			name:       "owned by actor allowed",
			ctx:        actorContext("alice", "member", "", ""),
			stateStore: newCollaborationStateStore(),
			min:        AccessCollaborator,
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
			},
		},
		{
			name:       "owned by other without share denied",
			ctx:        actorContext("bob", "member", "", ""),
			stateStore: newCollaborationStateStore(),
			min:        AccessCollaborator,
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
			},
			wantCode: connect.CodePermissionDenied,
		},
		{
			name:       "viewer share cannot satisfy collaborator",
			ctx:        actorContext("bob", "member", "", ""),
			stateStore: newCollaborationStateStore(),
			min:        AccessCollaborator,
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
				addCollaborationShare(ms, "share-viewer", "agent_run", "default", "run", "bob", "alice", "viewer")
			},
			wantCode: connect.CodePermissionDenied,
		},
		{
			name:       "collaborator share satisfies collaborator",
			ctx:        actorContext("bob", "member", "", ""),
			stateStore: newCollaborationStateStore(),
			min:        AccessCollaborator,
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
				addCollaborationShare(ms, "share-collab", "agent_run", "default", "run", "bob", "alice", "collaborator")
			},
		},
		{
			name:       "get owner store error maps internal",
			ctx:        actorContext("bob", "member", "", ""),
			stateStore: &collaborationStateStore{mockStateStore: newMockStateStore(), getResourceOwnerErr: errBoom},
			min:        AccessCollaborator,
			wantCode:   connect.CodeInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{}
			if tc.stateStore != nil {
				if tc.stateStore.shares == nil {
					tc.stateStore.shares = make(map[string]*store.ResourceShare)
				}
				if tc.setup != nil {
					tc.setup(t, tc.stateStore)
				}
				srv.stateStore = tc.stateStore
			}
			requireConnectCode(t, srv.requireResourceAccess(tc.ctx, "agent_run", "run", "default", tc.min, "edit this run"), tc.wantCode)
		})
	}
}

func TestShareResource(t *testing.T) {
	target := &auth.User{ID: "bob", Email: "bob@example.com", Name: "Bob", Picture: "bob.png"}

	t.Run("owner can share", func(t *testing.T) {
		ms := newCollaborationStateStore()
		addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
		srv := &Server{stateStore: ms, authStore: &collaborationAuthStore{users: []*auth.User{target}}}

		resp, err := srv.ShareResource(actorContext("alice", "member", "", ""), &platform.ShareResourceRequest{
			ResourceType: "agent_run", ResourceId: "run", ResourceNamespace: "default", SharedWithEmail: "bob@example.com", Permission: "viewer",
		})
		if err != nil {
			t.Fatalf("ShareResource owner: %v", err)
		}
		if resp.Share.GetSharedWith().GetUserId() != "bob" || resp.Share.GetSharedWith().GetEmail() != "bob@example.com" {
			t.Fatalf("share target = %#v", resp.Share.GetSharedWith())
		}
		if len(ms.notifications) != 1 {
			t.Fatalf("notifications written = %d, want 1", len(ms.notifications))
		}
	})

	t.Run("non owner non admin denied", func(t *testing.T) {
		ms := newCollaborationStateStore()
		addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
		srv := &Server{stateStore: ms, authStore: &collaborationAuthStore{users: []*auth.User{target}}}
		_, err := srv.ShareResource(actorContext("mallory", "member", "", ""), &platform.ShareResourceRequest{
			ResourceType: "agent_run", ResourceId: "run", ResourceNamespace: "default", SharedWithEmail: "bob@example.com", Permission: "viewer",
		})
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("want PermissionDenied, got %v", err)
		}
	})

	t.Run("invalid permission rejected", func(t *testing.T) {
		ms := newCollaborationStateStore()
		addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
		srv := &Server{stateStore: ms, authStore: &collaborationAuthStore{users: []*auth.User{target}}}
		_, err := srv.ShareResource(actorContext("alice", "member", "", ""), &platform.ShareResourceRequest{
			ResourceType: "agent_run", ResourceId: "run", ResourceNamespace: "default", SharedWithEmail: "bob@example.com", Permission: "editor",
		})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("want InvalidArgument, got %v", err)
		}
	})

	t.Run("notification failure does not fail share", func(t *testing.T) {
		ms := newCollaborationStateStore()
		ms.createNotificationErr = errors.New("notification down")
		addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
		srv := &Server{stateStore: ms, authStore: &collaborationAuthStore{users: []*auth.User{target}}}
		resp, err := srv.ShareResource(actorContext("alice", "member", "", ""), &platform.ShareResourceRequest{
			ResourceType: "agent_run", ResourceId: "run", ResourceNamespace: "default", SharedWithEmail: "bob@example.com", Permission: "collaborator",
		})
		if err != nil {
			t.Fatalf("ShareResource with notification failure: %v", err)
		}
		if resp.Share.GetPermission() != "collaborator" {
			t.Fatalf("permission = %q", resp.Share.GetPermission())
		}
	})
}

func TestRevokeShare(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		setup    func(*testing.T, *collaborationStateStore)
		wantCode connect.Code
	}{
		{
			name: "resource owner can revoke",
			ctx:  actorContext("alice", "member", "", ""),
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
				addCollaborationShare(ms, "share", "agent_run", "default", "run", "bob", "alice", "viewer")
			},
		},
		{
			name: "admin can revoke",
			ctx:  actorContext("root", "admin", "", ""),
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationShare(ms, "share", "agent_run", "default", "run", "bob", "alice", "viewer")
			},
		},
		{
			name: "non owner non admin denied",
			ctx:  actorContext("mallory", "member", "", ""),
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
				addCollaborationShare(ms, "share", "agent_run", "default", "run", "bob", "alice", "viewer")
			},
			wantCode: connect.CodePermissionDenied,
		},
		{
			name:     "share by id not found",
			ctx:      actorContext("alice", "member", "", ""),
			wantCode: connect.CodeNotFound,
		},
		{
			name: "share by id store error maps internal",
			ctx:  actorContext("alice", "member", "", ""),
			setup: func(t *testing.T, ms *collaborationStateStore) {
				ms.getShareByIDErr = errors.New("lookup failed")
			},
			wantCode: connect.CodeInternal,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ms := newCollaborationStateStore()
			if tc.setup != nil {
				tc.setup(t, ms)
			}
			srv := &Server{stateStore: ms}
			err := srv.RevokeShare(tc.ctx, &platform.RevokeShareRequest{ShareId: "share"})
			requireConnectCode(t, err, tc.wantCode)
		})
	}
}

func TestUpdateSharePermission(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		setup    func(*testing.T, *collaborationStateStore)
		wantCode connect.Code
	}{
		{
			name: "resource owner can update",
			ctx:  actorContext("alice", "member", "", ""),
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
				addCollaborationShare(ms, "share", "agent_run", "default", "run", "bob", "alice", "viewer")
			},
		},
		{
			name: "admin can update",
			ctx:  actorContext("root", "admin", "", ""),
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationShare(ms, "share", "agent_run", "default", "run", "bob", "alice", "viewer")
			},
		},
		{
			name: "non owner non admin denied",
			ctx:  actorContext("mallory", "member", "", ""),
			setup: func(t *testing.T, ms *collaborationStateStore) {
				addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
				addCollaborationShare(ms, "share", "agent_run", "default", "run", "bob", "alice", "viewer")
			},
			wantCode: connect.CodePermissionDenied,
		},
		{
			name:     "share by id not found",
			ctx:      actorContext("alice", "member", "", ""),
			wantCode: connect.CodeNotFound,
		},
		{
			name: "share by id store error maps internal",
			ctx:  actorContext("alice", "member", "", ""),
			setup: func(t *testing.T, ms *collaborationStateStore) {
				ms.getShareByIDErr = errors.New("lookup failed")
			},
			wantCode: connect.CodeInternal,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ms := newCollaborationStateStore()
			if tc.setup != nil {
				tc.setup(t, ms)
			}
			srv := &Server{stateStore: ms}
			resp, err := srv.UpdateSharePermission(tc.ctx, &platform.UpdateSharePermissionRequest{ShareId: "share", Permission: "collaborator"})
			requireConnectCode(t, err, tc.wantCode)
			if tc.wantCode == 0 && resp.GetPermission() != "collaborator" {
				t.Fatalf("permission = %q, want collaborator", resp.GetPermission())
			}
		})
	}
}

func TestVisibilityAgentRunVisibilityFilterUsesBulkResourceOwnersByTypeStore(t *testing.T) {
	ms := newCollaborationStateStore()
	addCollaborationOwner(t, ms, "agent_run", "default", "own", "alice")
	addCollaborationOwner(t, ms, "agent_run", "default", "other", "mallory")
	addCollaborationShare(ms, "share", "agent_run", "default", "shared", "alice", "mallory", "viewer")
	addCollaborationOwner(t, ms, "agent_run", "default", "shared", "mallory")
	srv := &Server{stateStore: ms}

	adminFilter := srv.agentRunVisibilityFilter(actorContext("root", "admin", "", ""), false)
	for _, name := range []string{"unowned", "own", "shared", "other"} {
		run := ownedRun(name)
		if !adminFilter(run) {
			t.Fatalf("admin should see %s", name)
		}
	}

	memberFilter := srv.agentRunVisibilityFilter(actorContext("alice", "member", "", ""), false)
	want := map[string]bool{
		"unowned": true,
		"own":     true,
		"shared":  true,
		"other":   false,
	}
	for name, allowed := range want {
		run := ownedRun(name)
		if got := memberFilter(run); got != allowed {
			t.Fatalf("memberFilter(%s) = %v, want %v", name, got, allowed)
		}
	}
}

func TestListSharedWithMeRequiresAuthenticatedActor(t *testing.T) {
	srv := &Server{stateStore: newCollaborationStateStore()}
	_, err := srv.ListSharedWithMe(withRequestActor(context.Background(), http.Header{}), &platform.ListSharedWithMeRequest{})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

func TestListSharesEnrichesUserInfo(t *testing.T) {
	ms := newCollaborationStateStore()
	addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
	addCollaborationShare(ms, "share", "agent_run", "default", "run", "bob", "alice", "collaborator")
	srv := &Server{stateStore: ms, authStore: &collaborationAuthStore{users: []*auth.User{
		{ID: "alice", Email: "alice@example.com", Name: "Alice", Picture: "alice.png"},
		{ID: "bob", Email: "bob@example.com", Name: "Bob", Picture: "bob.png"},
	}}}

	resp, err := srv.ListShares(actorContext("alice", "member", "", ""), &platform.ListSharesRequest{
		ResourceType: "agent_run", ResourceId: "run", ResourceNamespace: "default",
	})
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	if len(resp.Shares) != 1 {
		t.Fatalf("shares = %d, want 1", len(resp.Shares))
	}
	with := resp.Shares[0].GetSharedWith()
	if with.GetUserId() != "bob" || with.GetName() != "Bob" || with.GetEmail() != "bob@example.com" || with.GetPicture() != "bob.png" {
		t.Fatalf("sharedWith not enriched: %#v", with)
	}
	by := resp.Shares[0].GetSharedBy()
	if by.GetUserId() != "alice" || by.GetName() != "Alice" || by.GetEmail() != "alice@example.com" {
		t.Fatalf("sharedBy not enriched: %#v", by)
	}
}

func TestListSharesUnknownUserFallsBackToUserID(t *testing.T) {
	ms := newCollaborationStateStore()
	addCollaborationOwner(t, ms, "agent_run", "default", "run", "alice")
	addCollaborationShare(ms, "share", "agent_run", "default", "run", "ghost", "alice", "viewer")
	srv := &Server{stateStore: ms, authStore: &collaborationAuthStore{}}

	resp, err := srv.ListShares(actorContext("alice", "member", "", ""), &platform.ListSharesRequest{
		ResourceType: "agent_run", ResourceId: "run", ResourceNamespace: "default",
	})
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	if len(resp.Shares) != 1 {
		t.Fatalf("shares = %d, want 1", len(resp.Shares))
	}
	with := resp.Shares[0].GetSharedWith()
	if with.GetUserId() != "ghost" || with.GetName() != "" || with.GetEmail() != "" {
		t.Fatalf("sharedWith = %#v, want bare user id", with)
	}
}

func TestListSharedWithMeEnrichesUserInfo(t *testing.T) {
	ms := newCollaborationStateStore()
	addCollaborationShare(ms, "share", "agent_run", "default", "run", "bob", "alice", "viewer")
	srv := &Server{stateStore: ms, authStore: &collaborationAuthStore{users: []*auth.User{
		{ID: "alice", Email: "alice@example.com", Name: "Alice"},
		{ID: "bob", Email: "bob@example.com", Name: "Bob"},
	}}}

	resp, err := srv.ListSharedWithMe(actorContext("bob", "member", "", ""), &platform.ListSharedWithMeRequest{})
	if err != nil {
		t.Fatalf("ListSharedWithMe: %v", err)
	}
	if len(resp.Resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(resp.Resources))
	}
	by := resp.Resources[0].GetShare().GetSharedBy()
	if by.GetName() != "Alice" || by.GetEmail() != "alice@example.com" {
		t.Fatalf("sharedBy not enriched: %#v", by)
	}
}

var _ store.StateStore = (*collaborationStateStore)(nil)
var _ auth.Store = (*collaborationAuthStore)(nil)
