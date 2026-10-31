-- +goose Up
-- Keep keys and snapshots even when the original scheduled message is removed.
CREATE TABLE IF NOT EXISTS scheduled_manual_requests (
    key_hash TEXT PRIMARY KEY,
    admin_id BIGINT NOT NULL,
    chat_id BIGINT NOT NULL,
    task_id BIGINT NOT NULL,
    config_snapshot JSONB NOT NULL,
    attempt_id BIGINT NOT NULL UNIQUE REFERENCES scheduled_dispatch_attempts(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
-- Retain keys and attempts: forgetting them could cause a duplicate send.
SELECT 1;
