-- +goose Up
CREATE INDEX IF NOT EXISTS idx_violations_created_at ON violations(created_at);
CREATE INDEX IF NOT EXISTS idx_ai_decisions_created_at ON ai_decisions(created_at);
CREATE INDEX IF NOT EXISTS idx_pending_verifications_expires_at ON pending_verifications(expires_at);
CREATE INDEX IF NOT EXISTS idx_config_audit_created_at ON config_audit(created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_config_audit_created_at;
DROP INDEX IF EXISTS idx_pending_verifications_expires_at;
DROP INDEX IF EXISTS idx_ai_decisions_created_at;
DROP INDEX IF EXISTS idx_violations_created_at;
