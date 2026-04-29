-- +goose Up
ALTER TABLE ai_decisions ADD COLUMN metadata JSONB;

-- +goose Down
ALTER TABLE ai_decisions DROP COLUMN metadata;
