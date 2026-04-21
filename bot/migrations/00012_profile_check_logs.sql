-- +goose Up
CREATE TABLE IF NOT EXISTS profile_check_logs (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    user_name TEXT,
    username TEXT,
    bio TEXT,
    check_mode TEXT NOT NULL,
    result TEXT NOT NULL,
    matched_rule TEXT,
    ai_confidence REAL,
    ai_verdict TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_profile_check_logs_chat_created_at
ON profile_check_logs (chat_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_profile_check_logs_user_id
ON profile_check_logs (user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_profile_check_logs_user_id;
DROP INDEX IF EXISTS idx_profile_check_logs_chat_created_at;
DROP TABLE IF EXISTS profile_check_logs;
