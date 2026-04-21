-- +goose Up
CREATE TABLE IF NOT EXISTS authorized_groups (
    chat_id BIGINT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    authorized_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    authorized_by BIGINT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    notes TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE IF EXISTS authorized_groups;
