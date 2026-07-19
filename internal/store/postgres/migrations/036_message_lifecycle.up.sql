-- Durable user-message and interrupt lifecycle.
ALTER TABLE conversation_messages
    ADD COLUMN IF NOT EXISTS delivery_state TEXT NOT NULL DEFAULT 'completed',
    ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS delivery_sequence BIGINT,
    ADD COLUMN IF NOT EXISTS claim_token UUID;

-- Existing rows predate authoritative state. A delivery stamp proves the row
-- was consumed and must never be replayed. Unstamped follow-ups newer than the
-- latest assistant are unresolved; older unstamped rows are legacy history.
UPDATE conversation_messages AS message
SET delivery_state = CASE
    WHEN message.role <> 'user' THEN 'completed'
    WHEN message.metadata ? 'cancelled_at_unix' THEN 'cancelled'
    WHEN message.metadata ? 'delivered_at_unix' THEN 'completed'
    WHEN message.id > COALESCE((SELECT max(a.id) FROM conversation_messages a WHERE a.session_id = message.session_id AND a.role = 'assistant'), 0) THEN 'pending'
    ELSE 'completed'
END,
claimed_at = CASE
    WHEN role = 'user' AND metadata ? 'delivered_at_unix'
    THEN to_timestamp((metadata->>'delivered_at_unix')::double precision)
    ELSE claimed_at
END;

ALTER TABLE conversation_messages
    DROP CONSTRAINT IF EXISTS conversation_messages_delivery_state_check;
ALTER TABLE conversation_messages
    ADD CONSTRAINT conversation_messages_delivery_state_check
    CHECK (delivery_state IN ('pending', 'claimed', 'completed', 'cancelled'));

CREATE SEQUENCE IF NOT EXISTS conversation_delivery_sequence;
-- Preserve historical conversation order deterministically. IDs and the
-- sequence share one global monotonic domain for migrated rows.
UPDATE conversation_messages
SET delivery_sequence = id
WHERE delivery_sequence IS NULL AND delivery_state IN ('claimed', 'completed');
SELECT setval('conversation_delivery_sequence', GREATEST(COALESCE((SELECT max(delivery_sequence) FROM conversation_messages), 0) + 1, 1), false);

CREATE OR REPLACE FUNCTION initialize_conversation_message_lifecycle()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.role = 'user' THEN
        IF COALESCE(NEW.metadata, '{}'::jsonb) ? 'cancelled_at_unix' THEN
            NEW.delivery_state := 'cancelled';
        ELSIF COALESCE(NEW.metadata, '{}'::jsonb) ? 'delivered_at_unix' THEN
            NEW.delivery_state := 'completed';
            NEW.claimed_at := to_timestamp((NEW.metadata->>'delivered_at_unix')::double precision);
            NEW.delivery_sequence := nextval('conversation_delivery_sequence');
        ELSE
            NEW.delivery_state := 'pending';
            NEW.claimed_at := NULL;
            NEW.delivery_sequence := NULL;
        END IF;
    ELSE
        NEW.delivery_state := 'completed';
        NEW.delivery_sequence := nextval('conversation_delivery_sequence');
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS initialize_conversation_message_lifecycle_on_insert ON conversation_messages;
CREATE TRIGGER initialize_conversation_message_lifecycle_on_insert
BEFORE INSERT ON conversation_messages
FOR EACH ROW EXECUTE FUNCTION initialize_conversation_message_lifecycle();

CREATE INDEX IF NOT EXISTS idx_conversation_messages_pending
    ON conversation_messages (session_id, id)
    WHERE role = 'user' AND delivery_state = 'pending';
CREATE INDEX IF NOT EXISTS idx_conversation_messages_delivery_sequence
    ON conversation_messages (session_id, delivery_sequence)
    WHERE delivery_sequence IS NOT NULL;

CREATE TABLE IF NOT EXISTS session_interrupts (
    id BIGSERIAL PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    requested_by TEXT NOT NULL DEFAULT '',
    consumed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_session_interrupts_pending
    ON session_interrupts (session_id, id)
    WHERE consumed_at IS NULL;

CREATE TABLE IF NOT EXISTS agent_run_wake_intents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    message_id BIGINT NOT NULL REFERENCES conversation_messages(id) ON DELETE CASCADE,
    target_wake_requests BIGINT NOT NULL,
    applied_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, idempotency_key)
);
