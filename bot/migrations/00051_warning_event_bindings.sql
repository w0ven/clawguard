-- +goose Up
CREATE TABLE IF NOT EXISTS moderation_warning_events (
    event_key TEXT PRIMARY KEY,
    warning_id BIGINT NOT NULL REFERENCES warnings(id) ON DELETE CASCADE,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS moderation_warning_escalations (
    warning_id BIGINT PRIMARY KEY REFERENCES warnings(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('claimed','completed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose Down
-- Keep source identities for idempotent notification recovery.
SELECT 1;
