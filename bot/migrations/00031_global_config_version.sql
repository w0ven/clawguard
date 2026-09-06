-- +goose Up
ALTER TABLE global_config
    ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE global_config
    DROP COLUMN IF EXISTS version;
