package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User represents a registered user with a flat role model.
type User struct {
	ID           string
	Username     string
	Email        string // populated from Google OAuth; empty for local accounts
	Name         string
	Picture      string
	PasswordHash string // bcrypt hash; empty for OAuth-only accounts
	GoogleID     string // empty for local accounts
	Role         string // "admin", "member", or "viewer"
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time // nil if the user never logged in
}

// Session represents a refresh token session.
type Session struct {
	ID               string
	UserID           string
	RefreshTokenHash string
	ExpiresAt        time.Time
	CreatedAt        time.Time
}

// UserSummary is a lightweight user representation for search results.
type UserSummary struct {
	ID       string
	Username string
	Email    string
	Name     string
	Picture  string
}

// UserNamespace maps a user to their personal Kubernetes namespace, where their
// saved credential secrets and projects live. It is persisted so the namespace
// stays stable across display-name changes.
type UserNamespace struct {
	UserID    string
	Namespace string
	CreatedAt time.Time
}

// UserSoul is a user's personal SOUL: a role/persona definition they edit for
// their own agent. Other users' agents can consult it (via the ask_teammate
// tool) to get that teammate's likely perspective.
type UserSoul struct {
	UserID    string
	Content   string
	UpdatedAt time.Time
}

// UserGitIdentity contains a user's git commit settings. When name and email
// are set, AgentRuns the user creates author commits with that identity.
type UserGitIdentity struct {
	UserID    string
	Name      string
	Email     string
	UpdatedAt time.Time
}

// UserRoleModelPreference is one personal provider-specific model override for
// a specialist role. Missing rows inherit the platform RoleInstruction.
type UserRoleModelPreference struct {
	UserID    string
	RoleName  string
	Provider  string
	Model     string
	UpdatedAt time.Time
}

// UserRoleModelStore is an optional extension implemented by auth stores that
// persist personal role-model preferences. Keeping it separate from Store lets
// lightweight auth fakes and alternate auth backends remain source-compatible.
type UserRoleModelStore interface {
	ListUserRoleModelPreferences(ctx context.Context, userID string) ([]*UserRoleModelPreference, error)
	ReplaceUserRoleModelPreferences(ctx context.Context, userID string, preferences []*UserRoleModelPreference) ([]*UserRoleModelPreference, error)
}

// Store defines the persistence interface for integrated auth.
type Store interface {
	UpsertUser(ctx context.Context, u *User) (*User, error)
	GetUserByID(ctx context.Context, id string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByGoogleID(ctx context.Context, googleID string) (*User, error)
	SearchUsers(ctx context.Context, query string, limit int32) ([]*User, error)
	SetUserRole(ctx context.Context, userID, role string) error
	// ListUsers returns all registered users ordered by creation time.
	ListUsers(ctx context.Context) ([]*User, error)
	// DeleteUser permanently removes a user and their sessions.
	DeleteUser(ctx context.Context, userID string) error
	// TouchUserLastLogin records that the user just logged in.
	TouchUserLastLogin(ctx context.Context, userID string) error

	// GetUserNamespace returns the user's persisted personal namespace, or empty
	// string when none has been assigned yet.
	GetUserNamespace(ctx context.Context, userID string) (string, error)
	// SetUserNamespace persists the user's personal namespace. It is a no-op if a
	// namespace is already assigned (the first assignment wins).
	SetUserNamespace(ctx context.Context, userID, namespace string) error

	// GetUserSoul returns the user's personal SOUL, or nil if none is saved.
	GetUserSoul(ctx context.Context, userID string) (*UserSoul, error)
	// UpsertUserSoul creates or updates the user's personal SOUL.
	UpsertUserSoul(ctx context.Context, soul *UserSoul) (*UserSoul, error)

	// GetUserGitIdentity returns the user's git commit identity, or nil if none
	// is saved.
	GetUserGitIdentity(ctx context.Context, userID string) (*UserGitIdentity, error)
	// UpsertUserGitIdentity creates or updates the user's git commit identity.
	UpsertUserGitIdentity(ctx context.Context, identity *UserGitIdentity) (*UserGitIdentity, error)

	CreateSession(ctx context.Context, s *Session) error
	GetSessionByTokenHash(ctx context.Context, hash string) (*Session, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteExpiredSessions(ctx context.Context) error
	// RotateSession atomically consumes the old session and creates its
	// replacement. It fails when the old session was already consumed,
	// enforcing one-time use of refresh tokens under concurrency.
	RotateSession(ctx context.Context, oldSessionID string, replacement *Session) error
}

// PGStore implements Store backed by Postgres via pgx.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore creates a new Postgres-backed auth store from an existing pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) UpsertUser(ctx context.Context, u *User) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO auth_users (username, email, name, picture, password_hash, google_id, role)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (username) DO UPDATE SET
			email = COALESCE(NULLIF(EXCLUDED.email, ''), auth_users.email),
			name = COALESCE(NULLIF(EXCLUDED.name, ''), auth_users.name),
			picture = COALESCE(NULLIF(EXCLUDED.picture, ''), auth_users.picture),
			password_hash = COALESCE(NULLIF(EXCLUDED.password_hash, ''), auth_users.password_hash),
			google_id = COALESCE(NULLIF(EXCLUDED.google_id, ''), auth_users.google_id),
			-- Existing roles are managed explicitly by SetUserRole. Preserving the
			-- stored value prevents an SSO profile refresh from undoing a manual
			-- promotion or demotion with the resolver's default role.
			role = auth_users.role,
			updated_at = now()
		RETURNING id, username, COALESCE(email, ''), name, picture,
			COALESCE(password_hash, ''), COALESCE(google_id, ''), role,
			created_at, updated_at, last_login_at`,
		u.Username, nullableString(u.Email), u.Name, u.Picture,
		nullableString(u.PasswordHash), nullableString(u.GoogleID), u.Role)
	return scanUser(row)
}

func (s *PGStore) GetUserByID(ctx context.Context, id string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, username, COALESCE(email, ''), name, picture,
			COALESCE(password_hash, ''), COALESCE(google_id, ''), role,
			created_at, updated_at, last_login_at
		FROM auth_users WHERE id = $1`, id)
	return scanUser(row)
}

func (s *PGStore) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, username, COALESCE(email, ''), name, picture,
			COALESCE(password_hash, ''), COALESCE(google_id, ''), role,
			created_at, updated_at, last_login_at
		FROM auth_users WHERE username = $1`, username)
	return scanUser(row)
}

func (s *PGStore) GetUserByGoogleID(ctx context.Context, googleID string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, username, COALESCE(email, ''), name, picture,
			COALESCE(password_hash, ''), COALESCE(google_id, ''), role,
			created_at, updated_at, last_login_at
		FROM auth_users WHERE google_id = $1`, googleID)
	return scanUser(row)
}

func (s *PGStore) SearchUsers(ctx context.Context, query string, limit int32) ([]*User, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	pattern := "%" + escapeLikePattern(query) + "%"
	rows, err := s.pool.Query(ctx, `
		SELECT id, username, COALESCE(email, ''), name, picture,
			COALESCE(password_hash, ''), COALESCE(google_id, ''), role,
			created_at, updated_at, last_login_at
		FROM auth_users
		WHERE username ILIKE $1 OR name ILIKE $1 OR email ILIKE $1
		ORDER BY name
		LIMIT $2`, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("searching users: %w", err)
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *PGStore) SetUserRole(ctx context.Context, userID, role string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE auth_users SET role = $1, updated_at = now() WHERE id = $2`,
		role, userID)
	if err != nil {
		return fmt.Errorf("setting user role: %w", err)
	}
	return nil
}

func (s *PGStore) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, username, COALESCE(email, ''), name, picture,
			COALESCE(password_hash, ''), COALESCE(google_id, ''), role,
			created_at, updated_at, last_login_at
		FROM auth_users
		ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *PGStore) DeleteUser(ctx context.Context, userID string) error {
	// Sessions (and other per-user auth rows) cascade via foreign keys.
	tag, err := s.pool.Exec(ctx, `DELETE FROM auth_users WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("deleting user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PGStore) TouchUserLastLogin(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE auth_users SET last_login_at = now() WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("touching user last login: %w", err)
	}
	return nil
}

// --- User namespaces ---

func (s *PGStore) GetUserNamespace(ctx context.Context, userID string) (string, error) {
	var namespace string
	err := s.pool.QueryRow(ctx, `
		SELECT namespace FROM auth_user_namespaces WHERE user_id = $1`, userID).
		Scan(&namespace)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("getting user namespace: %w", err)
	}
	return namespace, nil
}

func (s *PGStore) SetUserNamespace(ctx context.Context, userID, namespace string) error {
	// First assignment wins: a user's namespace must remain stable once chosen.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO auth_user_namespaces (user_id, namespace)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO NOTHING`, userID, namespace); err != nil {
		return fmt.Errorf("setting user namespace: %w", err)
	}
	return nil
}

// --- SOUL (personal persona) ---

func (s *PGStore) GetUserSoul(ctx context.Context, userID string) (*UserSoul, error) {
	var soul UserSoul
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, content, updated_at
		FROM auth_user_souls WHERE user_id = $1`, userID).
		Scan(&soul.UserID, &soul.Content, &soul.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting user soul: %w", err)
	}
	return &soul, nil
}

func (s *PGStore) UpsertUserSoul(ctx context.Context, soul *UserSoul) (*UserSoul, error) {
	var out UserSoul
	err := s.pool.QueryRow(ctx, `
		INSERT INTO auth_user_souls (user_id, content)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET
			content = EXCLUDED.content,
			updated_at = now()
		RETURNING user_id, content, updated_at`,
		soul.UserID, soul.Content).
		Scan(&out.UserID, &out.Content, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upserting user soul: %w", err)
	}
	return &out, nil
}

// --- Git identity (commit author) ---

func (s *PGStore) GetUserGitIdentity(ctx context.Context, userID string) (*UserGitIdentity, error) {
	var identity UserGitIdentity
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, name, email, updated_at
		FROM auth_user_git_identities WHERE user_id = $1`, userID).
		Scan(&identity.UserID, &identity.Name, &identity.Email, &identity.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting user git identity: %w", err)
	}
	return &identity, nil
}

func (s *PGStore) UpsertUserGitIdentity(ctx context.Context, identity *UserGitIdentity) (*UserGitIdentity, error) {
	var out UserGitIdentity
	err := s.pool.QueryRow(ctx, `
		INSERT INTO auth_user_git_identities (user_id, name, email)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET
			name = EXCLUDED.name,
			email = EXCLUDED.email,
			updated_at = now()
		RETURNING user_id, name, email, updated_at`,
		identity.UserID, identity.Name, identity.Email).
		Scan(&out.UserID, &out.Name, &out.Email, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upserting user git identity: %w", err)
	}
	return &out, nil
}

// --- Personal role models ---

func (s *PGStore) ListUserRoleModelPreferences(ctx context.Context, userID string) ([]*UserRoleModelPreference, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, role_name, provider, model, updated_at
		FROM auth_user_role_models
		WHERE user_id = $1
		ORDER BY role_name, provider`, userID)
	if err != nil {
		return nil, fmt.Errorf("listing user role models: %w", err)
	}
	defer rows.Close()
	var out []*UserRoleModelPreference
	for rows.Next() {
		preference := &UserRoleModelPreference{}
		if err := rows.Scan(&preference.UserID, &preference.RoleName, &preference.Provider, &preference.Model, &preference.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning user role model: %w", err)
		}
		out = append(out, preference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing user role models: %w", err)
	}
	return out, nil
}

func (s *PGStore) ReplaceUserRoleModelPreferences(ctx context.Context, userID string, preferences []*UserRoleModelPreference) ([]*UserRoleModelPreference, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning user role model replacement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM auth_user_role_models WHERE user_id = $1`, userID); err != nil {
		return nil, fmt.Errorf("clearing user role models: %w", err)
	}
	for _, preference := range preferences {
		if preference == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_user_role_models (user_id, role_name, provider, model)
			VALUES ($1, $2, $3, $4)`, userID, preference.RoleName, preference.Provider, preference.Model); err != nil {
			return nil, fmt.Errorf("inserting user role model: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing user role model replacement: %w", err)
	}
	return s.ListUserRoleModelPreferences(ctx, userID)
}

// --- Sessions ---

func (s *PGStore) CreateSession(ctx context.Context, sess *Session) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth_sessions (user_id, refresh_token_hash, expires_at)
		VALUES ($1, $2, $3)`,
		sess.UserID, sess.RefreshTokenHash, sess.ExpiresAt)
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

func (s *PGStore) GetSessionByTokenHash(ctx context.Context, hash string) (*Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, refresh_token_hash, expires_at, created_at
		FROM auth_sessions WHERE refresh_token_hash = $1`, hash).
		Scan(&sess.ID, &sess.UserID, &sess.RefreshTokenHash, &sess.ExpiresAt, &sess.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	return &sess, nil
}

func (s *PGStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

func (s *PGStore) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE expires_at < now()`)
	if err != nil {
		return fmt.Errorf("deleting expired sessions: %w", err)
	}
	return nil
}

// RotateSession atomically deletes the old session and inserts its replacement
// in one transaction. If the old session no longer exists (already rotated by
// a concurrent request), the transaction is rolled back and an error returned.
func (s *PGStore) RotateSession(ctx context.Context, oldSessionID string, replacement *Session) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning session rotation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `DELETE FROM auth_sessions WHERE id = $1`, oldSessionID)
	if err != nil {
		return fmt.Errorf("consuming session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("refresh token already used")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_sessions (user_id, refresh_token_hash, expires_at)
		VALUES ($1, $2, $3)`,
		replacement.UserID, replacement.RefreshTokenHash, replacement.ExpiresAt); err != nil {
		return fmt.Errorf("creating replacement session: %w", err)
	}
	return tx.Commit(ctx)
}

// --- helpers ---

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.Name, &u.Picture,
		&u.PasswordHash, &u.GoogleID, &u.Role, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, fmt.Errorf("scanning user: %w", err)
	}
	return &u, nil
}

func scanUserRow(rows pgx.Rows) (*User, error) {
	var u User
	err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Name, &u.Picture,
		&u.PasswordHash, &u.GoogleID, &u.Role, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, fmt.Errorf("scanning user: %w", err)
	}
	return &u, nil
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// escapeLikePattern escapes LIKE/ILIKE metacharacters so a search query
// matches literally instead of acting as a wildcard pattern.
func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
