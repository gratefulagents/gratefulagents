package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// clientMessageIDMaxLen bounds SendAgentRunMessageRequest.client_message_id.
const clientMessageIDMaxLen = 128

// validateClientMessageID normalizes and validates an optional client-chosen
// idempotency key: at most clientMessageIDMaxLen characters, printable, no
// control characters. Empty means the client did not request idempotency.
func validateClientMessageID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", nil
	}
	if !utf8.ValidString(id) || utf8.RuneCountInString(id) > clientMessageIDMaxLen {
		return "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("client_message_id must be valid UTF-8 of at most %d characters", clientMessageIDMaxLen))
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return "", connect.NewError(connect.CodeInvalidArgument,
				errors.New("client_message_id must contain only printable characters"))
		}
	}
	return id, nil
}

// withClientMessageID records the idempotency key in the user message
// metadata under client_message_id so a retry can find the stored copy.
func withClientMessageID(metadata json.RawMessage, clientMessageID string) json.RawMessage {
	if clientMessageID == "" {
		return metadata
	}
	payload := map[string]any{}
	if len(metadata) > 0 && json.Unmarshal(metadata, &payload) != nil {
		payload = map[string]any{}
	}
	payload["client_message_id"] = clientMessageID
	encoded, err := json.Marshal(payload)
	if err != nil {
		return metadata
	}
	return encoded
}

// pgPoolProvider is satisfied by the Postgres store, which exposes its pool
// for read-only lookups the store interface does not cover.
type pgPoolProvider interface {
	Pool() *pgxpool.Pool
}

// findMessageByClientID looks up a previously stored user message carrying
// the given client_message_id in this session. Stores without a Postgres
// pool report not-found, which degrades to the non-idempotent behavior.
//
// Sequential retries are answered from this lookup; truly concurrent
// duplicates are caught by the partial unique index from migration 059
// (idx_conversation_messages_client_message_id), whose violation
// resolveDuplicateClientMessage maps back to the stored message.
func (s *Server) findMessageByClientID(ctx context.Context, sessionID uuid.UUID, clientMessageID string) (int64, bool, error) {
	provider, ok := s.stateStore.(pgPoolProvider)
	if !ok || provider.Pool() == nil {
		return 0, false, nil
	}
	var id int64
	err := provider.Pool().QueryRow(ctx,
		`SELECT id FROM conversation_messages
		 WHERE session_id = $1 AND role = 'user' AND metadata->>'client_message_id' = $2
		 ORDER BY id DESC LIMIT 1`,
		sessionID, clientMessageID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}
