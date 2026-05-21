-- +goose Up
ALTER TABLE user_trust ADD COLUMN IF NOT EXISTS is_bot BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS idx_user_trust_is_bot ON user_trust(is_bot) WHERE is_bot = TRUE;

-- +goose Down
DROP INDEX IF EXISTS idx_user_trust_is_bot;
ALTER TABLE user_trust DROP COLUMN IF EXISTS is_bot;
