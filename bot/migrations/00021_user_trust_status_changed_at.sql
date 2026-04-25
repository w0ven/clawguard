-- +goose Up
ALTER TABLE user_trust
ADD COLUMN IF NOT EXISTS status_changed_at TIMESTAMPTZ;

UPDATE user_trust
SET status_changed_at = COALESCE(graduated_at, banned_at, updated_at, joined_at, NOW())
WHERE status_changed_at IS NULL;

ALTER TABLE user_trust
ALTER COLUMN status_changed_at SET DEFAULT NOW(),
ALTER COLUMN status_changed_at SET NOT NULL;

-- +goose Down
ALTER TABLE user_trust
DROP COLUMN IF EXISTS status_changed_at;
