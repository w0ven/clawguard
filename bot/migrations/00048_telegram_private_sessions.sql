-- +goose Up
CREATE TABLE IF NOT EXISTS telegram_private_sessions (
    user_id BIGINT PRIMARY KEY,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS miniapp_verification_notifications (
    pending_id BIGINT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'unconfirmed' CHECK (status IN ('unconfirmed', 'sent', 'failed')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
-- Preserve private-chat consent history across rollback and reapplication.
SELECT 1;
