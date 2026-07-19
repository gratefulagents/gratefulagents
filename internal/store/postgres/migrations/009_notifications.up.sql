-- 009_notifications.up.sql
-- In-app notifications for collaboration events.

CREATE TABLE IF NOT EXISTS notifications (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id               TEXT NOT NULL,
    type                  TEXT NOT NULL,
    title                 TEXT NOT NULL,
    body                  TEXT NOT NULL DEFAULT '',
    resource_type         TEXT NOT NULL DEFAULT '',
    resource_id           TEXT NOT NULL DEFAULT '',
    resource_namespace    TEXT NOT NULL DEFAULT '',
    actor_id              TEXT NOT NULL DEFAULT '',
    actor_name            TEXT NOT NULL DEFAULT '',
    read                  BOOLEAN NOT NULL DEFAULT false,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications(user_id, read, created_at DESC);
