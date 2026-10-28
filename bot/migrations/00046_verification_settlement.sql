-- +goose Up
-- A completed external cleanup is independent of the mutable pending row.
-- Completed rows remain as idempotency keys even if a new verification replaces it.
CREATE TABLE verification_cleanup_settlements (
    operation_key TEXT PRIMARY KEY,
    pending_id BIGINT,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    username TEXT,
    rule TEXT NOT NULL,
    matched TEXT,
    action TEXT NOT NULL,
    settled BOOLEAN NOT NULL DEFAULT FALSE,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    settled_at TIMESTAMPTZ
);
CREATE INDEX verification_cleanup_unsettled_idx ON verification_cleanup_settlements(next_attempt_at, created_at) WHERE NOT settled;

-- +goose Down
DROP TABLE verification_cleanup_settlements;
