-- +goose Up
CREATE INDEX IF NOT EXISTS idx_pending_verifications_chat_expires_at
ON pending_verifications(chat_id, expires_at);

-- +goose Down
DROP INDEX IF EXISTS idx_pending_verifications_chat_expires_at;
