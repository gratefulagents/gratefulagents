package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/crypto/bcrypt"

	authpb "github.com/gratefulagents/gratefulagents/rpc/auth"
)

const (
	accessTokenTTL  = 15 * time.Minute
	refreshTokenTTL = 30 * 24 * time.Hour
)

// Server implements the AuthService RPC handlers.
type Server struct {
	store    Store
	google   *GoogleVerifier // nil if Google OAuth is not configured
	jwt      *JWTIssuer
	resolver *RoleResolver
}

// NewServer creates a new integrated auth server.
func NewServer(store Store, google *GoogleVerifier, jwt *JWTIssuer, resolver *RoleResolver) *Server {
	return &Server{
		store:    store,
		google:   google,
		jwt:      jwt,
		resolver: resolver,
	}
}

func (s *Server) Login(ctx context.Context, req *connect.Request[authpb.LoginRequest]) (*connect.Response[authpb.LoginResponse], error) {
	var user *User

	if req.Msg.GoogleIdToken != "" {
		// Google OAuth login
		u, err := s.loginGoogle(ctx, req.Msg.GoogleIdToken)
		if err != nil {
			return nil, err
		}
		user = u
	} else if req.Msg.Username != "" && req.Msg.Password != "" {
		// Username/password login
		u, err := s.loginPassword(ctx, req.Msg.Username, req.Msg.Password)
		if err != nil {
			return nil, err
		}
		user = u
	} else {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provide google_id_token or username+password"))
	}

	// Record the login time for the admin user view; best-effort.
	if err := s.store.TouchUserLastLogin(ctx, user.ID); err == nil {
		now := time.Now()
		user.LastLoginAt = &now
	}

	// Issue tokens
	accessToken, expiresAt, err := s.jwt.IssueAccessToken(AccessTokenClaims{
		Sub:      user.ID,
		Username: user.Username,
		Email:    user.Email,
		Name:     user.Name,
		Picture:  user.Picture,
		Role:     user.Role,
	}, accessTokenTTL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("issue access token: %w", err))
	}

	refreshToken, refreshExp, err := s.jwt.IssueRefreshToken(user.ID, refreshTokenTTL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("issue refresh token: %w", err))
	}

	if err := s.store.CreateSession(ctx, &Session{
		UserID:           user.ID,
		RefreshTokenHash: hashToken(refreshToken),
		ExpiresAt:        time.Unix(refreshExp, 0),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
	}

	return connect.NewResponse(&authpb.LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		User:         userToProto(user),
	}), nil
}

func (s *Server) loginGoogle(ctx context.Context, idToken string) (*User, error) {
	if s.google == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("Google OAuth is not configured"))
	}

	claims, err := s.google.Verify(ctx, idToken)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("google token: %w", err))
	}
	if !claims.EmailVerified {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("email not verified"))
	}

	role := s.resolver.ResolveRole(claims.Email)

	// Use email prefix as username for Google users
	username := claims.Email
	user, err := s.store.UpsertUser(ctx, &User{
		Username: username,
		Email:    claims.Email,
		Name:     claims.Name,
		Picture:  claims.Picture,
		GoogleID: claims.Sub,
		Role:     role,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("upsert user: %w", err))
	}

	return user, nil
}

func (s *Server) loginPassword(ctx context.Context, username, password string) (*User, error) {
	user, err := s.store.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
	}
	if user.PasswordHash == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
	}

	return user, nil
}

func (s *Server) RefreshToken(ctx context.Context, req *connect.Request[authpb.RefreshTokenRequest]) (*connect.Response[authpb.RefreshTokenResponse], error) {
	tokenHash := hashToken(req.Msg.RefreshToken)
	session, err := s.store.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid refresh token"))
	}
	if time.Now().After(session.ExpiresAt) {
		_ = s.store.DeleteSession(ctx, session.ID)
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("refresh token expired"))
	}

	user, err := s.store.GetUserByID(ctx, session.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get user: %w", err))
	}

	accessToken, expiresAt, err := s.jwt.IssueAccessToken(AccessTokenClaims{
		Sub:      user.ID,
		Username: user.Username,
		Email:    user.Email,
		Name:     user.Name,
		Picture:  user.Picture,
		Role:     user.Role,
	}, accessTokenTTL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("issue access token: %w", err))
	}

	newRefreshToken, refreshExp, err := s.jwt.IssueRefreshToken(user.ID, refreshTokenTTL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("issue refresh token: %w", err))
	}
	// Atomically consume the old session and create the replacement so a
	// concurrently replayed refresh token cannot mint a second session.
	if err := s.store.RotateSession(ctx, session.ID, &Session{
		UserID:           user.ID,
		RefreshTokenHash: hashToken(newRefreshToken),
		ExpiresAt:        time.Unix(refreshExp, 0),
	}); err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("refresh token no longer valid"))
	}

	return connect.NewResponse(&authpb.RefreshTokenResponse{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
		ExpiresAt:    expiresAt,
	}), nil
}

func (s *Server) Logout(ctx context.Context, req *connect.Request[authpb.LogoutRequest]) (*connect.Response[authpb.LogoutResponse], error) {
	tokenHash := hashToken(req.Msg.RefreshToken)
	session, err := s.store.GetSessionByTokenHash(ctx, tokenHash)
	if err == nil {
		_ = s.store.DeleteSession(ctx, session.ID)
	}
	return connect.NewResponse(&authpb.LogoutResponse{}), nil
}

func (s *Server) GetCurrentUser(ctx context.Context, _ *connect.Request[authpb.GetCurrentUserRequest]) (*connect.Response[authpb.User], error) {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("not authenticated"))
	}

	user, err := s.store.GetUserByID(ctx, claims.Sub)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
	}

	return connect.NewResponse(userToProto(user)), nil
}

func (s *Server) SearchUsers(ctx context.Context, req *connect.Request[authpb.SearchUsersRequest]) (*connect.Response[authpb.SearchUsersResponse], error) {
	if ClaimsFromContext(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	if req.Msg.Query == "" {
		return connect.NewResponse(&authpb.SearchUsersResponse{}), nil
	}

	limit := req.Msg.Limit
	if limit <= 0 {
		limit = 10
	}

	users, err := s.store.SearchUsers(ctx, req.Msg.Query, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("search users: %w", err))
	}

	var pbUsers []*authpb.UserSummary
	for _, u := range users {
		pbUsers = append(pbUsers, &authpb.UserSummary{
			Id:       u.ID,
			Email:    u.Email,
			Name:     u.Name,
			Picture:  u.Picture,
			Username: u.Username,
		})
	}

	return connect.NewResponse(&authpb.SearchUsersResponse{
		Users: pbUsers,
	}), nil
}

// --- Admin user management ---

// requireAdmin returns the caller's claims when they are an authenticated
// admin, or a connect error otherwise.
func requireAdmin(ctx context.Context) (*Claims, error) {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	if claims.Role != RoleAdmin {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("admin role required"))
	}
	return claims, nil
}

func (s *Server) ListUsers(ctx context.Context, _ *connect.Request[authpb.ListUsersRequest]) (*connect.Response[authpb.ListUsersResponse], error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list users: %w", err))
	}
	resp := &authpb.ListUsersResponse{}
	for _, u := range users {
		resp.Users = append(resp.Users, userToProto(u))
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) UpdateUserRole(ctx context.Context, req *connect.Request[authpb.UpdateUserRoleRequest]) (*connect.Response[authpb.User], error) {
	claims, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	role := req.Msg.Role
	if role != RoleAdmin && role != RoleMember && role != RoleViewer {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid role %q: must be admin, member, or viewer", role))
	}
	if req.Msg.UserId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("user_id is required"))
	}
	if req.Msg.UserId == claims.Sub && role != RoleAdmin {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("you cannot demote your own admin account"))
	}
	if _, err := s.store.GetUserByID(ctx, req.Msg.UserId); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
	}
	if err := s.store.SetUserRole(ctx, req.Msg.UserId, role); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("set user role: %w", err))
	}
	user, err := s.store.GetUserByID(ctx, req.Msg.UserId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get user: %w", err))
	}
	return connect.NewResponse(userToProto(user)), nil
}

func (s *Server) DeleteUser(ctx context.Context, req *connect.Request[authpb.DeleteUserRequest]) (*connect.Response[authpb.DeleteUserResponse], error) {
	claims, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.UserId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("user_id is required"))
	}
	if req.Msg.UserId == claims.Sub {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("you cannot delete your own account"))
	}
	if err := s.store.DeleteUser(ctx, req.Msg.UserId); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user not found"))
	}
	return connect.NewResponse(&authpb.DeleteUserResponse{}), nil
}

func (s *Server) GetJWKS(_ context.Context, _ *connect.Request[authpb.GetJWKSRequest]) (*connect.Response[authpb.GetJWKSResponse], error) {
	jwks, err := s.jwt.JWKSJSON()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generate JWKS: %w", err))
	}
	return connect.NewResponse(&authpb.GetJWKSResponse{
		KeysJson: jwks,
	}), nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// userToProto converts a stored user into its RPC representation.
func userToProto(u *User) *authpb.User {
	pb := &authpb.User{
		Id:        u.ID,
		Email:     u.Email,
		Name:      u.Name,
		Picture:   u.Picture,
		Username:  u.Username,
		Role:      u.Role,
		CreatedAt: u.CreatedAt.Unix(),
	}
	if u.LastLoginAt != nil {
		pb.LastLoginAt = u.LastLoginAt.Unix()
	}
	return pb
}
