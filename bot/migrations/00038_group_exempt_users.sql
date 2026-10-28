-- +goose Up
CREATE TABLE IF NOT EXISTS group_exempt_users (
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL CHECK (user_id > 0),
    note TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (chat_id, user_id)
);
-- +goose Down
-- Preserve group and legacy global records on rollback.
SELECT 1;
