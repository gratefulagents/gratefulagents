package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/tools"
)

// soulResolver resolves a teammate handle to that user's saved SOUL by joining
// auth_users to auth_user_souls in the shared Postgres database. It backs the
// ask_teammate tool.
type soulResolver struct {
	pool *pgxpool.Pool
}

// ResolveSoul implements tools.SoulResolver. The lookup is case-insensitive on
// username, preferring an exact-case match and otherwise resolving
// deterministically when usernames collide only by case. found is false when
// there is no such user or they have no SOUL row; an empty SOUL content is
// returned verbatim so the tool can report it.
func (r *soulResolver) ResolveSoul(ctx context.Context, name string) (string, string, bool, error) {
	var username, content string
	err := r.pool.QueryRow(ctx, `
		SELECT u.username, s.content
		FROM auth_users u
		JOIN auth_user_souls s ON s.user_id = u.id
		WHERE lower(u.username) = lower($1)
		ORDER BY (u.username = $1) DESC, u.username
		LIMIT 1`, name).Scan(&username, &content)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	return username, content, true, nil
}

// setupAskTeammateTool registers the ask_teammate tool when a database is
// configured. SOULs live in the same Postgres database the agent already uses,
// so the tool is available whenever DATABASE_URL is set. Returns the tool (for
// wiring the persona runner once the runtime is built) and a cleanup func.
func setupAskTeammateTool(ctx context.Context, registry *tools.Registry) (*tools.AskTeammateTool, func()) {
	noop := func() {}
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		log.Printf("ask_teammate disabled: DATABASE_URL not set")
		return nil, noop
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Printf("WARN: ask_teammate disabled: failed to connect to Postgres: %v", err)
		return nil, noop
	}
	tool := tools.RegisterAskTeammateTool(registry, &soulResolver{pool: pool})
	if tool == nil {
		pool.Close()
		return nil, noop
	}
	log.Printf("ask_teammate tool registered (teammate persona consultation)")
	return tool, pool.Close
}
