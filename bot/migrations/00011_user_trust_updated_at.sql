-- +goose Up
ALTER TABLE user_trust
ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

UPDATE user_trust
SET updated_at = COALESCE(graduated_at, joined_at, NOW())
WHERE updated_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_user_trust_retention
ON user_trust(status, updated_at, joined_at, messages_checked);

-- +goose Down
DROP INDEX IF EXISTS idx_user_trust_retention;
ALTER TABLE user_trust
DROP COLUMN IF EXISTS updated_at;
