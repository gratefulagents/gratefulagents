package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// --- Sharing handlers ---

func (s *Server) ShareResource(ctx context.Context, req *platform.ShareResourceRequest) (*platform.ShareResourceResponse, error) {
	if s.stateStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("state store not configured"))
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}

	// Only owner or admin can share.
	access := s.checkResourceAccess(ctx, req.ResourceType, req.ResourceId, req.ResourceNamespace)
	if access < AccessOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only the resource owner or admin can share"))
	}

	// Look up the target user by email via integrated auth store.
	if s.authStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("user search not available"))
	}
	users, err := s.authStore.SearchUsers(ctx, req.SharedWithEmail, 10)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("searching users: %w", err))
	}
	// Find exact email match.
	var targetUserID, targetEmail, targetName, targetPicture string
	for _, u := range users {
		if u.Email == req.SharedWithEmail {
			targetUserID = u.ID
			targetEmail = u.Email
			targetName = u.Name
			targetPicture = u.Picture
			break
		}
	}
	if targetUserID == "" {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user %q not found", req.SharedWithEmail))
	}

	// Validate permission value.
	if req.Permission != "viewer" && req.Permission != "collaborator" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("permission must be 'viewer' or 'collaborator'"))
	}

	share, err := s.stateStore.ShareResource(ctx, &store.ResourceShare{
		ResourceType:      req.ResourceType,
		ResourceID:        req.ResourceId,
		ResourceNamespace: req.ResourceNamespace,
		SharedWithUserID:  targetUserID,
		SharedByUserID:    actor.Subject,
		Permission:        req.Permission,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("creating share: %w", err))
	}

	// Fire notification for the target user.
	if err := s.stateStore.CreateNotification(ctx, &store.Notification{
		UserID:            targetUserID,
		Type:              "resource_shared",
		Title:             fmt.Sprintf("Resource shared with you"),
		Body:              fmt.Sprintf("%s shared a %s with you as %s", actor.Subject, req.ResourceType, req.Permission),
		ResourceType:      req.ResourceType,
		ResourceID:        req.ResourceId,
		ResourceNamespace: req.ResourceNamespace,
		ActorID:           actor.Subject,
	}); err != nil {
		log.Printf("WARN: failed to create share notification: %v", err)
	}

	return &platform.ShareResourceResponse{
		Share: &platform.ResourceShareInfo{
			Id:                share.ID,
			ResourceType:      share.ResourceType,
			ResourceId:        share.ResourceID,
			ResourceNamespace: share.ResourceNamespace,
			SharedWith: &platform.ResourceOwner{
				UserId:  targetUserID,
				Email:   targetEmail,
				Name:    targetName,
				Picture: targetPicture,
			},
			SharedBy: &platform.ResourceOwner{
				UserId: actor.Subject,
			},
			Permission: share.Permission,
			CreatedAt:  timestamppb.New(share.CreatedAt),
		},
	}, nil
}

func (s *Server) RevokeShare(ctx context.Context, req *platform.RevokeShareRequest) error {
	if s.stateStore == nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("state store not configured"))
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	if err := s.requireShareManagementAccess(ctx, req.ShareId, "revoke this share"); err != nil {
		return err
	}
	if err := s.stateStore.RevokeShare(ctx, req.ShareId); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("revoking share: %w", err))
	}
	return nil
}

func (s *Server) UpdateSharePermission(ctx context.Context, req *platform.UpdateSharePermissionRequest) (*platform.ResourceShareInfo, error) {
	if s.stateStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("state store not configured"))
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	if req.Permission != "viewer" && req.Permission != "collaborator" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("permission must be 'viewer' or 'collaborator'"))
	}
	if err := s.requireShareManagementAccess(ctx, req.ShareId, "update this share"); err != nil {
		return nil, err
	}
	if err := s.stateStore.UpdateSharePermission(ctx, req.ShareId, req.Permission); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("updating share: %w", err))
	}
	return &platform.ResourceShareInfo{
		Id:         req.ShareId,
		Permission: req.Permission,
	}, nil
}

func (s *Server) ListShares(ctx context.Context, req *platform.ListSharesRequest) (*platform.ListSharesResponse, error) {
	if s.stateStore == nil {
		return &platform.ListSharesResponse{}, nil
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" && actor.Role == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	// Only someone with at least viewer access to the resource may enumerate
	// its shares; share IDs gate revoke/update operations.
	if access := s.checkResourceAccess(ctx, req.ResourceType, req.ResourceId, req.ResourceNamespace); access < AccessViewer {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("you do not have access to this resource"))
	}
	shares, err := s.stateStore.ListSharesForResource(ctx, req.ResourceType, req.ResourceId, req.ResourceNamespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing shares: %w", err))
	}
	enrich := s.ownerEnricher(ctx)
	var pbShares []*platform.ResourceShareInfo
	for _, sh := range shares {
		pbShares = append(pbShares, shareToProto(&sh, enrich))
	}
	return &platform.ListSharesResponse{Shares: pbShares}, nil
}

func (s *Server) ListSharedWithMe(ctx context.Context, req *platform.ListSharedWithMeRequest) (*platform.ListSharedWithMeResponse, error) {
	if s.stateStore == nil {
		return &platform.ListSharedWithMeResponse{}, nil
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	shares, err := s.stateStore.ListSharedWithMe(ctx, actor.Subject, req.ResourceType)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing shared resources: %w", err))
	}
	enrich := s.ownerEnricher(ctx)
	var resources []*platform.SharedResource
	for _, sh := range shares {
		resources = append(resources, &platform.SharedResource{
			Share:       shareToProto(&sh, enrich),
			DisplayName: sh.ResourceID,
		})
	}
	return &platform.ListSharedWithMeResponse{Resources: resources}, nil
}

func shareToProto(sh *store.ResourceShare, enrichOwner func(string) *platform.ResourceOwner) *platform.ResourceShareInfo {
	return &platform.ResourceShareInfo{
		Id:                sh.ID,
		ResourceType:      sh.ResourceType,
		ResourceId:        sh.ResourceID,
		ResourceNamespace: sh.ResourceNamespace,
		SharedWith:        enrichOwner(sh.SharedWithUserID),
		SharedBy:          enrichOwner(sh.SharedByUserID),
		Permission:        sh.Permission,
		CreatedAt:         timestamppb.New(sh.CreatedAt),
	}
}

// --- Notification handlers ---

func (s *Server) ListNotifications(ctx context.Context, req *platform.ListNotificationsRequest) (*platform.ListNotificationsResponse, error) {
	if s.stateStore == nil {
		return &platform.ListNotificationsResponse{}, nil
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	notifications, err := s.stateStore.ListNotifications(ctx, actor.Subject, req.UnreadOnly, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing notifications: %w", err))
	}
	unreadCount, err := s.stateStore.GetUnreadNotificationCount(ctx, actor.Subject)
	if err != nil {
		log.Printf("WARN: failed to get unread notification count: %v", err)
	}
	var pbNotifications []*platform.NotificationInfo
	for _, n := range notifications {
		pbNotifications = append(pbNotifications, &platform.NotificationInfo{
			Id:                n.ID,
			Type:              n.Type,
			Title:             n.Title,
			Body:              n.Body,
			ResourceType:      n.ResourceType,
			ResourceId:        n.ResourceID,
			ResourceNamespace: n.ResourceNamespace,
			Actor: &platform.ResourceOwner{
				UserId: n.ActorID,
				Name:   n.ActorName,
			},
			Read:      n.Read,
			CreatedAt: timestamppb.New(n.CreatedAt),
		})
	}
	return &platform.ListNotificationsResponse{
		Notifications: pbNotifications,
		UnreadCount:   unreadCount,
	}, nil
}

// notificationUserScopedStore is implemented by state stores that can mark a
// notification read only when it belongs to the given user.
type notificationUserScopedStore interface {
	MarkNotificationReadForUser(ctx context.Context, notificationID, userID string) error
}

func (s *Server) MarkNotificationRead(ctx context.Context, req *platform.MarkNotificationReadRequest) error {
	if s.stateStore == nil {
		return nil
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	if req.NotificationId == "" {
		// Mark all read.
		return s.stateStore.MarkAllNotificationsRead(ctx, actor.Subject)
	}
	if scoped, ok := s.stateStore.(notificationUserScopedStore); ok {
		return scoped.MarkNotificationReadForUser(ctx, req.NotificationId, actor.Subject)
	}
	return s.stateStore.MarkNotificationRead(ctx, req.NotificationId)
}

// --- Presence handlers ---

func (s *Server) SendPresenceHeartbeat(ctx context.Context, req *platform.PresenceHeartbeatRequest) error {
	if s.presence == nil {
		return nil
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	if err := s.requirePresenceResourceAccess(ctx, req.ResourceType, req.ResourceId, req.ResourceNamespace, "record presence on this resource"); err != nil {
		return err
	}

	vi := ViewerInfo{UserID: actor.Subject}
	// Enrich with name/email/picture from JWT claims.
	if claims := auth.ClaimsFromContext(ctx); claims != nil {
		vi.Name = claims.Name
		if vi.Name == "" {
			vi.Name = claims.Username
		}
		vi.Email = claims.Email
		vi.Picture = claims.Picture
	}

	s.presence.Heartbeat(req.ResourceType, req.ResourceId, req.ResourceNamespace, vi)
	return nil
}

func (s *Server) GetPresence(ctx context.Context, req *platform.GetPresenceRequest) (*platform.GetPresenceResponse, error) {
	if s.presence == nil {
		return &platform.GetPresenceResponse{}, nil
	}
	// Presence reveals who is viewing a resource; only callers who can view
	// the resource itself may see its viewers.
	if err := s.requirePresenceResourceAccess(ctx, req.ResourceType, req.ResourceId, req.ResourceNamespace, "view presence on this resource"); err != nil {
		return nil, err
	}
	viewers := s.presence.GetViewers(req.ResourceType, req.ResourceId, req.ResourceNamespace)
	var pbViewers []*platform.ResourceOwner
	for _, v := range viewers {
		pbViewers = append(pbViewers, &platform.ResourceOwner{
			UserId:  v.UserID,
			Name:    v.Name,
			Email:   v.Email,
			Picture: v.Picture,
		})
	}
	return &platform.GetPresenceResponse{Viewers: pbViewers}, nil
}

func (s *Server) requirePresenceResourceAccess(ctx context.Context, resourceType, resourceID, resourceNS, action string) error {
	if resourceType == "agent_run" {
		return s.requireAgentRunAccess(ctx, resourceNS, resourceID, AccessViewer, action)
	}
	return s.requireResourceAccess(ctx, resourceType, resourceID, resourceNS, AccessViewer, action)
}

// enrichOwner looks up a user by ID and returns a populated ResourceOwner proto.
func (s *Server) enrichOwner(ctx context.Context, userID string) *platform.ResourceOwner {
	owner := &platform.ResourceOwner{UserId: userID}
	if s.authStore != nil {
		if u, err := s.authStore.GetUserByID(ctx, userID); err == nil && u != nil {
			owner.Name = u.Name
			owner.Email = u.Email
			owner.Picture = u.Picture
		}
	}
	return owner
}

// ownerEnricher returns a memoized enrichOwner for enriching many owners in a
// single request (e.g. share lists where the same user appears repeatedly).
func (s *Server) ownerEnricher(ctx context.Context) func(userID string) *platform.ResourceOwner {
	cache := make(map[string]*platform.ResourceOwner)
	return func(userID string) *platform.ResourceOwner {
		if owner, ok := cache[userID]; ok {
			return owner
		}
		owner := s.enrichOwner(ctx, userID)
		cache[userID] = owner
		return owner
	}
}

// --- Authorization helpers ---

// ResourceAccessLevel defines the access a user has to a resource.
type ResourceAccessLevel int

const (
	AccessNone         ResourceAccessLevel = iota
	AccessViewer                           // read-only via explicit share
	AccessCollaborator                     // read-write via explicit share
	AccessOwner                            // resource owner
	AccessAdmin                            // workspace admin or org owner — full visibility
)

// checkResourceAccess determines the caller's access level for a given resource.
func (s *Server) checkResourceAccess(ctx context.Context, resourceType, resourceID, resourceNS string) ResourceAccessLevel {
	actor := requestActorFromContext(ctx)

	// 1. Admin/owner role → full access.
	if actor.Role == "admin" || actor.Role == "owner" {
		return AccessAdmin
	}

	if s.stateStore == nil {
		return AccessNone
	}

	// 2. Resource owner.
	ownership, err := s.stateStore.GetResourceOwner(ctx, resourceType, resourceID, resourceNS)
	if err == nil && ownership != nil && ownership.OwnerID == actor.Subject {
		return AccessOwner
	}

	// 3. Explicit share.
	if actor.Subject != "" {
		share, err := s.stateStore.GetSharePermission(ctx, resourceType, resourceID, resourceNS, actor.Subject)
		if err == nil && share != nil {
			if share.Permission == "collaborator" {
				return AccessCollaborator
			}
			return AccessViewer
		}
	}

	return AccessNone
}

// requireResourceAccess enforces a minimum access level on a resource.
//
// Posture:
//   - Internal calls and tests that never went through the RPC interceptor
//     (no actor recorded on the context) are allowed: the only external
//     entrypoint attaches RequestActorInterceptor, so every real request
//     carries an actor.
//   - Admin/owner roles always pass.
//   - Without a state store there is no ownership system; authenticated
//     callers are allowed (single-tenant/dev deployments).
//   - Resources without an ownership record (trigger/system-created) are
//     visible to any authenticated user, preserving team workflows.
//   - Owned resources require ownership, an explicit share at or above the
//     requested level, or an admin role.
func (s *Server) requireResourceAccess(ctx context.Context, resourceType, resourceID, resourceNS string, min ResourceAccessLevel, action string) error {
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded {
		return nil // internal invocation — no RPC interceptor ran
	}
	if actor.Role == "admin" || actor.Role == "owner" {
		return nil
	}
	if actor.Subject == "" && actor.Role == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required to %s", action))
	}
	if s.stateStore == nil {
		return nil // no ownership system configured
	}

	ownership, err := s.stateStore.GetResourceOwner(ctx, resourceType, resourceID, resourceNS)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("checking resource ownership: %w", err))
	}
	if ownership == nil || ownership.OwnerID == "" {
		return nil // unowned (trigger/system-created) resource
	}
	if ownership.OwnerID == actor.Subject {
		return nil
	}
	share, err := s.stateStore.GetSharePermission(ctx, resourceType, resourceID, resourceNS, actor.Subject)
	if err == nil && share != nil {
		level := AccessViewer
		if share.Permission == "collaborator" {
			level = AccessCollaborator
		}
		if level >= min {
			return nil
		}
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("you do not have permission to %s", action))
}

// requireAgentRunViewer enforces read access to an AgentRun. Runs without a
// direct ownership record inherit the owner of the trigger that created them;
// otherwise an owned private trigger's conversations would be exposed as
// apparently-unowned runs.
func (s *Server) requireAgentRunViewer(ctx context.Context, namespace, name string) error {
	return s.requireAgentRunAccess(ctx, namespace, name, AccessViewer, "view this run")
}

func (s *Server) requireAgentRunAccess(ctx context.Context, namespace, name string, min ResourceAccessLevel, action string) error {
	// Preserve the cheap/direct path and support callers whose tests or
	// transitional resources have an ownership row before the CR is visible.
	if s.stateStore == nil {
		return s.requireResourceAccess(ctx, "agent_run", name, namespace, min, action)
	}
	ownership, err := s.stateStore.GetResourceOwner(ctx, "agent_run", name, namespace)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("checking resource ownership: %w", err))
	}
	if ownership != nil && ownership.OwnerID != "" {
		return s.requireResourceAccess(ctx, "agent_run", name, namespace, min, action)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, run); err != nil {
		return mapK8sError(fmt.Sprintf("get AgentRun %s/%s", namespace, name), err)
	}
	return s.requireAgentRunAccessForRun(ctx, run, min, action)
}

func (s *Server) requireAgentRunViewerForRun(ctx context.Context, run *platformv1alpha1.AgentRun) error {
	return s.requireAgentRunAccessForRun(ctx, run, AccessViewer, "view this run")
}

func agentRunProjectName(run *platformv1alpha1.AgentRun) string {
	if run == nil || run.Spec.Context == nil || run.Spec.Context.ProjectRef == nil || run.Spec.Context.ProjectRef.Kind != "Project" {
		return ""
	}
	return strings.TrimSpace(run.Spec.Context.ProjectRef.Name)
}

func (s *Server) requireAgentRunAccessForRun(ctx context.Context, run *platformv1alpha1.AgentRun, min ResourceAccessLevel, action string) error {
	if projectName := agentRunProjectName(run); projectName != "" {
		return s.requireResourceAccess(ctx, projectResourceType, projectName, run.Namespace, min, action)
	}
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded || actor.Role == "admin" || actor.Role == "owner" {
		return nil
	}
	if actor.Subject == "" && actor.Role == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required to %s", action))
	}
	if s.stateStore == nil {
		return nil
	}

	ownership, err := s.stateStore.GetResourceOwner(ctx, "agent_run", run.Name, run.Namespace)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("checking resource ownership: %w", err))
	}
	if ownership == nil || ownership.OwnerID == "" {
		resourceType := agentRunTriggerResourceTypes[run.Spec.Trigger.Kind]
		triggerName := strings.TrimSpace(run.Spec.Trigger.Name)
		if resourceType != "" && triggerName != "" {
			ownership, err = s.stateStore.GetResourceOwner(ctx, resourceType, triggerName, run.Namespace)
			if err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("checking trigger ownership: %w", err))
			}
		}
	}
	if ownership == nil || ownership.OwnerID == "" || ownership.OwnerID == actor.Subject {
		return nil
	}
	share, err := s.stateStore.GetSharePermission(ctx, "agent_run", run.Name, run.Namespace, actor.Subject)
	if err == nil && share != nil {
		level := AccessViewer
		if share.Permission == "collaborator" {
			level = AccessCollaborator
		}
		if level >= min {
			return nil
		}
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("you do not have permission to %s", action))
}

// shareByIDStore is implemented by state stores that can resolve a share by ID.
type shareByIDStore interface {
	GetShareByID(ctx context.Context, shareID string) (*store.ResourceShare, error)
}

// resourceOwnersByTypeStore is implemented by stores that can bulk-list ownership.
type resourceOwnersByTypeStore interface {
	ListResourceOwnersByType(ctx context.Context, resourceType string) ([]store.ResourceOwnership, error)
}

// cachedResourceOwnersByType bulk-loads ownership for one resource type.
// When cached is true the result is coalesced across concurrent list/watch
// ticks for probeACLTTL and the returned slice is shared: callers must treat
// it as read-only. User-triggered (unary) paths pass cached=false so a
// mutation is visible in the very next request.
func (s *Server) cachedResourceOwnersByType(ctx context.Context, bulk resourceOwnersByTypeStore, resourceType string, cached bool) ([]store.ResourceOwnership, error) {
	if !cached {
		return bulk.ListResourceOwnersByType(ctx, resourceType)
	}
	return probeCacheDo(ctx, &s.probes, "aclown|"+resourceType, probeACLTTL,
		func(ctx context.Context) ([]store.ResourceOwnership, error) {
			return bulk.ListResourceOwnersByType(ctx, resourceType)
		})
}

// cachedSharedWithMe loads one actor's shares for a resource type. When
// cached is true the result is coalesced across that actor's concurrent
// list/watch ticks for probeACLTTL and the returned slice is shared: callers
// must treat it as read-only.
func (s *Server) cachedSharedWithMe(ctx context.Context, subject, resourceType string, cached bool) ([]store.ResourceShare, error) {
	if !cached {
		return s.stateStore.ListSharedWithMe(ctx, subject, resourceType)
	}
	return probeCacheDo(ctx, &s.probes, "aclsh|"+subject+"|"+resourceType, probeACLTTL,
		func(ctx context.Context) ([]store.ResourceShare, error) {
			return s.stateStore.ListSharedWithMe(ctx, subject, resourceType)
		})
}

// resourceACLView returns per-request visibility and ACL-fingerprint
// functions for one resource type, sharing a single bulk load of ownership
// and share state. visible decides whether the caller may see a resource:
// admins (and internal calls without an RPC actor) see everything; other
// callers see unowned (system-created) resources, their own resources, and
// resources shared with them. aclKey fingerprints the caller's access to a
// resource (owner id + permission level) so watch streams can re-emit items
// whose ownership or share level changed even though the underlying
// Kubernetes resource did not.
// cached selects the coalesced (watch-tick) ACL loads; user-triggered unary
// paths pass cached=false so mutations are visible in the next request.
func (s *Server) resourceACLView(ctx context.Context, resourceType string, cached bool) (visible func(namespace, name string) bool, aclKey func(namespace, name string) string) {
	allowAll := func(string, string) bool { return true }
	noACL := func(string, string) string { return "" }
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded || actor.Role == "admin" || actor.Role == "owner" || s.stateStore == nil {
		return allowAll, noACL
	}
	if bulk, ok := s.stateStore.(resourceOwnersByTypeStore); ok {
		owners, err := s.cachedResourceOwnersByType(ctx, bulk, resourceType, cached)
		if err == nil {
			ownerOf := make(map[string]string, len(owners))
			for _, o := range owners {
				ownerOf[o.ResourceNamespace+"/"+o.ResourceID] = o.OwnerID
			}
			shareOf := map[string]string{}
			shares, _ := s.cachedSharedWithMe(ctx, actor.Subject, resourceType, cached)
			for _, sh := range shares {
				shareOf[sh.ResourceNamespace+"/"+sh.ResourceID] = sh.Permission
			}
			visible = func(namespace, name string) bool {
				key := namespace + "/" + name
				owner, owned := ownerOf[key]
				if !owned || owner == "" || owner == actor.Subject {
					return true
				}
				_, shared := shareOf[key]
				return shared
			}
			aclKey = func(namespace, name string) string {
				key := namespace + "/" + name
				owner := ownerOf[key]
				permission := shareOf[key]
				if owner != "" && owner == actor.Subject {
					permission = "owner"
				}
				return owner + "\x1f" + permission
			}
			return visible, aclKey
		}
		log.Printf("WARN: listing resource owners for visibility filter: %v", err)
	}
	// Fallback: per-resource ownership lookups.
	visible = func(namespace, name string) bool {
		return s.requireResourceAccess(ctx, resourceType, name, namespace, AccessViewer, "view this resource") == nil
	}
	aclKey = func(namespace, name string) string {
		owner, permission := s.resourceACL(ctx, resourceType, name, namespace)
		return owner.GetUserId() + "\x1f" + permission
	}
	return visible, aclKey
}

// resourceVisibilityFilter returns a predicate deciding whether the caller may
// see a given resource of resourceType in list/watch responses.
func (s *Server) resourceVisibilityFilter(ctx context.Context, resourceType string, cached bool) func(namespace, name string) bool {
	visible, _ := s.resourceACLView(ctx, resourceType, cached)
	return visible
}

// agentRunVisibilityFilter returns a predicate deciding whether the caller may
// see a run in list/watch responses. A run with no direct owner inherits its
// trigger's owner, matching detail authorization and preventing private
// trigger conversations from being treated as public, unowned runs.
func (s *Server) agentRunVisibilityFilter(ctx context.Context, cached bool) func(*platformv1alpha1.AgentRun) bool {
	allowAll := func(*platformv1alpha1.AgentRun) bool { return true }
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded || actor.Role == "admin" || actor.Role == "owner" || s.stateStore == nil {
		return allowAll
	}
	bulk, ok := s.stateStore.(resourceOwnersByTypeStore)
	if !ok {
		return func(run *platformv1alpha1.AgentRun) bool {
			return s.requireAgentRunViewerForRun(ctx, run) == nil
		}
	}

	owners, err := s.cachedResourceOwnersByType(ctx, bulk, "agent_run", cached)
	if err != nil {
		log.Printf("WARN: listing AgentRun owners for visibility filter: %v", err)
		return func(run *platformv1alpha1.AgentRun) bool {
			return s.requireAgentRunViewerForRun(ctx, run) == nil
		}
	}
	ownerOf := make(map[string]string, len(owners))
	for _, ownership := range owners {
		ownerOf[ownership.ResourceNamespace+"/"+ownership.ResourceID] = ownership.OwnerID
	}
	triggerOwners := make(map[string]map[string]string, len(agentRunTriggerResourceTypes))
	for _, resourceType := range agentRunTriggerResourceTypes {
		if _, loaded := triggerOwners[resourceType]; loaded {
			continue
		}
		items, loadErr := s.cachedResourceOwnersByType(ctx, bulk, resourceType, cached)
		if loadErr != nil {
			log.Printf("WARN: listing %s owners for AgentRun visibility filter: %v", resourceType, loadErr)
			return func(run *platformv1alpha1.AgentRun) bool {
				return s.requireAgentRunViewerForRun(ctx, run) == nil
			}
		}
		byName := make(map[string]string, len(items))
		for _, ownership := range items {
			byName[ownership.ResourceNamespace+"/"+ownership.ResourceID] = ownership.OwnerID
		}
		triggerOwners[resourceType] = byName
	}
	shareOf := map[string]struct{}{}
	shares, err := s.cachedSharedWithMe(ctx, actor.Subject, "agent_run", cached)
	if err != nil {
		log.Printf("WARN: listing AgentRun shares for visibility filter: %v", err)
		return func(run *platformv1alpha1.AgentRun) bool {
			return s.requireAgentRunViewerForRun(ctx, run) == nil
		}
	}
	for _, share := range shares {
		shareOf[share.ResourceNamespace+"/"+share.ResourceID] = struct{}{}
	}

	return func(run *platformv1alpha1.AgentRun) bool {
		if projectName := agentRunProjectName(run); projectName != "" {
			return s.requireResourceAccess(ctx, projectResourceType, projectName, run.Namespace, AccessViewer, "view this run") == nil
		}
		key := run.Namespace + "/" + run.Name
		ownerID := ownerOf[key]
		if ownerID == "" {
			resourceType := agentRunTriggerResourceTypes[run.Spec.Trigger.Kind]
			triggerName := strings.TrimSpace(run.Spec.Trigger.Name)
			if resourceType != "" && triggerName != "" {
				ownerID = triggerOwners[resourceType][run.Namespace+"/"+triggerName]
			}
		}
		if ownerID == "" || ownerID == actor.Subject {
			return true
		}
		_, shared := shareOf[key]
		return shared
	}
}

// resourceACL returns the display owner and the caller's permission level for
// a resource, for embedding in read responses ("owner", "collaborator",
// "viewer", or "admin"). Returns (nil, "") when no ownership record exists or
// no state store is configured.
func (s *Server) resourceACL(ctx context.Context, resourceType, resourceID, resourceNS string) (*platform.ResourceOwner, string) {
	if s.stateStore == nil {
		return nil, ""
	}
	ownership, err := s.stateStore.GetResourceOwner(ctx, resourceType, resourceID, resourceNS)
	if err != nil || ownership == nil {
		return nil, ""
	}
	owner := s.enrichOwner(ctx, ownership.OwnerID)
	actor := requestActorFromContext(ctx)
	switch {
	case actor.Role == "admin" || actor.Role == "owner":
		return owner, "admin"
	case ownership.OwnerID == actor.Subject:
		return owner, "owner"
	case actor.Subject != "":
		if share, err := s.stateStore.GetSharePermission(ctx, resourceType, resourceID, resourceNS, actor.Subject); err == nil && share != nil {
			return owner, share.Permission
		}
	}
	return owner, ""
}

// requireShareManagementAccess resolves a share by ID and verifies the caller
// owns (or administers) the underlying resource.
func (s *Server) requireShareManagementAccess(ctx context.Context, shareID, action string) error {
	actor := requestActorFromContext(ctx)
	if actor.Role == "admin" || actor.Role == "owner" {
		return nil
	}
	byID, ok := s.stateStore.(shareByIDStore)
	if !ok {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only an admin can %s", action))
	}
	share, err := byID.GetShareByID(ctx, shareID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("share not found"))
		}
		// Don't mask backend failures as missing data.
		return connect.NewError(connect.CodeInternal, fmt.Errorf("looking up share: %w", err))
	}
	access := s.checkResourceAccess(ctx, share.ResourceType, share.ResourceID, share.ResourceNamespace)
	if access < AccessOwner {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only the resource owner or admin can %s", action))
	}
	return nil
}
