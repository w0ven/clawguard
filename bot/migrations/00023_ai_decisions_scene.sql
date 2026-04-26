-- +goose Up
ALTER TABLE ai_decisions ADD COLUMN scene TEXT NOT NULL DEFAULT 'message';
CREATE INDEX idx_ai_decisions_scene ON ai_decisions(scene, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_ai_decisions_scene;
ALTER TABLE ai_decisions DROP COLUMN scene;
