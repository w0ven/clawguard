-- +goose Up
CREATE INDEX idx_pending_expires
ON pending_verifications(expires_at)
WHERE expires_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_pending_expires;
