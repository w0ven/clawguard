-- +goose Up
ALTER TABLE warnings
ADD COLUMN IF NOT EXISTS consumed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_warnings_chat_user_active
ON warnings(chat_id, user_id, created_at DESC)
WHERE consumed_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_warnings_chat_user_active;

ALTER TABLE warnings
DROP COLUMN IF EXISTS consumed_at;
