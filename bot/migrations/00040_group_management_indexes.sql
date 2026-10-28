-- +goose Up
CREATE INDEX IF NOT EXISTS idx_user_trust_known_bot_user ON user_trust (user_id) WHERE is_bot;
CREATE INDEX IF NOT EXISTS idx_group_exempt_expiry ON group_exempt_users (chat_id, expires_at, user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_user_trust_known_bot_user;
DROP INDEX IF EXISTS idx_group_exempt_expiry;
