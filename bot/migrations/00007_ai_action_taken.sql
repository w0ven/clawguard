-- +goose Up
ALTER TABLE ai_decisions
ADD COLUMN IF NOT EXISTS action_taken TEXT NOT NULL DEFAULT 'none';

-- +goose Down
ALTER TABLE ai_decisions
DROP COLUMN IF EXISTS action_taken;
