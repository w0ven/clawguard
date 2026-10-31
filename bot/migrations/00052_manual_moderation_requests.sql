-- +goose Up
CREATE TABLE IF NOT EXISTS manual_moderation_requests (
    request_key TEXT PRIMARY KEY,
    admin_id BIGINT NOT NULL,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    action TEXT NOT NULL,
    response_code INTEGER,
    response_body TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

-- +goose Down
-- Preserve request outcomes to prevent accidental replay after rollback.
SELECT 1;
