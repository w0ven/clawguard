-- +migrate Up
ALTER TABLE ai_decisions ADD COLUMN metadata JSONB;
-- +migrate Down
ALTER TABLE ai_decisions DROP COLUMN metadata;
