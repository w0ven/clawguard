-- +goose Up
ALTER TABLE llm_models
  ADD COLUMN probe_enabled BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN probe_interval_seconds INT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE llm_models
  DROP COLUMN IF EXISTS probe_interval_seconds,
  DROP COLUMN IF EXISTS probe_enabled;
