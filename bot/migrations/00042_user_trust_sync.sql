-- +goose Up
CREATE TABLE IF NOT EXISTS user_trust_sync (
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    token TEXT NOT NULL,
    target_status TEXT NOT NULL,
    base_status TEXT NOT NULL,
    base_changed_at TIMESTAMPTZ NOT NULL,
    action TEXT NOT NULL,
    action_done BOOLEAN NOT NULL DEFAULT FALSE,
    permission_done BOOLEAN NOT NULL DEFAULT FALSE,
    state TEXT NOT NULL CHECK (state IN ('claimed','retry','completed','conflict')),
    lease_until TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (chat_id,user_id)
);
-- +goose Down
-- Retain retry and idempotence state.
SELECT 1;
