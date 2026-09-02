package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// WriteActivityEvents inserts a batch of activity events in one statement.
// Implements store.ActivityEventBatchWriter. One statement means the
// statement-level change_seq trigger (migration 056) fires once for the whole
// batch, so a burst of tool-call events costs one session-row update and one
// wake-up notification instead of one per event.
func (s *Store) WriteActivityEvents(ctx context.Context, sessionID uuid.UUID, events []store.ActivityEventInput) ([]int64, error) {
	if len(events) == 0 {
		return nil, nil
	}
	eventTypes := make([]string, len(events))
	summaries := make([]string, len(events))
	details := make([]string, len(events))
	for i, ev := range events {
		eventTypes[i] = ev.EventType
		summaries[i] = ev.Summary
		if len(ev.Detail) == 0 {
			details[i] = "{}"
		} else {
			if !json.Valid(ev.Detail) {
				return nil, fmt.Errorf("activity event %d: detail is not valid JSON", i)
			}
			details[i] = string(ev.Detail)
		}
	}

	// The detail column is jsonb; the payloads travel as text[] and are cast
	// per element so the batch does not depend on driver-side jsonb[] encoding.
	// unnest with ordinality keeps RETURNING in input order.
	rows, err := s.pool.Query(ctx, `
		INSERT INTO activity_events (session_id, event_type, summary, detail)
		SELECT $1, e.event_type, e.summary, e.detail::jsonb
		FROM unnest($2::text[], $3::text[], $4::text[])
		     WITH ORDINALITY AS e(event_type, summary, detail, ord)
		ORDER BY e.ord
		RETURNING id`,
		sessionID, eventTypes, summaries, details)
	if err != nil {
		return nil, fmt.Errorf("writing activity events: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0, len(events))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning activity event id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading activity event ids: %w", err)
	}
	if len(ids) != len(events) {
		return nil, fmt.Errorf("writing activity events: inserted %d of %d rows", len(ids), len(events))
	}
	return ids, nil
}
