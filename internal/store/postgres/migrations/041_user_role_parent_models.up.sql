-- 041_user_role_parent_models.up.sql
-- Role-wide preferences that force a specialist to follow its parent run model.

CREATE TABLE IF NOT EXISTS auth_user_role_parent_models (
    user_id     UUID NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    role_name   TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_name),
    CHECK (octet_length(role_name) BETWEEN 1 AND 253)
);
