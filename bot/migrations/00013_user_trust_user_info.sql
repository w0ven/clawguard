-- +goose Up
ALTER TABLE user_trust
ADD COLUMN IF NOT EXISTS username TEXT,
ADD COLUMN IF NOT EXISTS first_name TEXT,
ADD COLUMN IF NOT EXISTS last_name TEXT;

CREATE INDEX IF NOT EXISTS idx_user_trust_username
ON user_trust(username)
WHERE username IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_user_trust_username;

ALTER TABLE user_trust
DROP COLUMN IF EXISTS last_name,
DROP COLUMN IF EXISTS first_name,
DROP COLUMN IF EXISTS username;
