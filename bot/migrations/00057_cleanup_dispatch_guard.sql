-- +goose Up
-- A dispatch is recorded before contacting Telegram. A surviving dispatched
-- row is deliberately not leased/replayed: the effect may have occurred.
CREATE TABLE IF NOT EXISTS verification_cleanup_dispatch (
    operation_key text PRIMARY KEY,
    chat_id bigint NOT NULL,
    user_id bigint NOT NULL,
    action text NOT NULL,
    state text NOT NULL CHECK (state IN ('dispatched','completed')),
    result text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS verification_cleanup_dispatch_review_idx ON verification_cleanup_dispatch (created_at) WHERE state='dispatched';

-- +goose Down
-- Preserve unknown dispatches across rollback; dropping these would replay them.
SELECT 1;
