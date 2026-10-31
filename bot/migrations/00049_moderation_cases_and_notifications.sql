-- +goose Up
ALTER TABLE telegram_private_sessions ADD COLUMN IF NOT EXISTS is_reachable BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE telegram_private_sessions ADD COLUMN IF NOT EXISTS notification_epoch BIGINT NOT NULL DEFAULT 1;
-- NULL means a pre-upgrade sync; do not emit historical notifications on replay.
ALTER TABLE user_trust_sync ADD COLUMN IF NOT EXISTS moderation_operation_id TEXT;
ALTER TABLE ai_unban_review_claims ADD COLUMN IF NOT EXISTS notify_eligible BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE ai_mute_review_claims ADD COLUMN IF NOT EXISTS notify_eligible BOOLEAN NOT NULL DEFAULT false;
CREATE TABLE IF NOT EXISTS moderation_cases (
    id BIGSERIAL PRIMARY KEY,
    source_key TEXT NOT NULL UNIQUE,
    source_kind TEXT NOT NULL,
    source_id TEXT NOT NULL,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    action TEXT NOT NULL,
    result TEXT NOT NULL CHECK (result IN ('executed','partial','failed','reversed')),
    explanation TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    ownership JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS moderation_cases_chat_time_idx ON moderation_cases(chat_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS moderation_cases_user_time_idx ON moderation_cases(user_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS moderation_notifications (
    id BIGSERIAL PRIMARY KEY,
    case_id BIGINT NOT NULL UNIQUE REFERENCES moderation_cases(id),
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','leased','sending','sent','unreachable','failed','expired','uncertain')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '24 hours',
    lease_token TEXT,
    lease_until TIMESTAMPTZ,
    session_version TIMESTAMPTZ,
    session_epoch BIGINT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS moderation_notifications_due_idx ON moderation_notifications(next_attempt_at, id) WHERE state='pending';
CREATE INDEX IF NOT EXISTS moderation_notifications_user_due_idx ON moderation_notifications(user_id,next_attempt_at,id) WHERE state='pending';
CREATE INDEX IF NOT EXISTS moderation_notifications_chat_id_idx ON moderation_notifications(chat_id,id DESC);
CREATE INDEX IF NOT EXISTS moderation_notifications_chat_backlog_idx ON moderation_notifications(chat_id) WHERE state IN ('pending','leased','sending');
CREATE INDEX IF NOT EXISTS moderation_notifications_active_user_idx ON moderation_notifications(user_id,lease_until) WHERE state IN ('leased','sending');

-- +goose Down
-- Preserve cases and notification delivery outcomes through rollback.
SELECT 1;
