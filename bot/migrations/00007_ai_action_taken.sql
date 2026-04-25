-- +goose Up
ALTER TABLE ai_decisions
ADD COLUMN IF NOT EXISTS action_taken TEXT NOT NULL DEFAULT 'none';

-- +goose Down
-- action_taken belongs to 00006_user_trust.sql; down must preserve the 00006 schema.
