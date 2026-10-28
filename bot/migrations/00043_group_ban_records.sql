-- +goose Up
CREATE TABLE IF NOT EXISTS group_ban_records (
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    reason TEXT,
    source TEXT NOT NULL,
    banned_by BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id,user_id)
);
-- +goose Down
-- Preserve records with explicit group provenance.
SELECT 1;
