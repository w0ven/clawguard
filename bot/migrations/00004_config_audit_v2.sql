-- +goose Up
ALTER TABLE config_audit
ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT 'update',
ADD COLUMN IF NOT EXISTS before JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS after JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE config_audit
ALTER COLUMN diff TYPE TEXT USING diff::text,
ALTER COLUMN diff DROP NOT NULL;

CREATE INDEX IF NOT EXISTS idx_config_audit_chat_created
ON config_audit(chat_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_config_audit_admin_created
ON config_audit(admin_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_config_audit_admin_created;
DROP INDEX IF EXISTS idx_config_audit_chat_created;

ALTER TABLE config_audit
DROP COLUMN IF EXISTS after,
DROP COLUMN IF EXISTS before,
DROP COLUMN IF EXISTS action;
