-- +goose Up
-- Existing claims/retries predate dispatch markers; do not infer that an
-- unfinished external action was never sent. No historical data is rewritten.
ALTER TABLE user_trust_sync ADD COLUMN IF NOT EXISTS dispatch_guarded BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE user_trust_sync ADD COLUMN IF NOT EXISTS dispatch_stage TEXT;

-- +goose Down
-- Retain dispatch uncertainty and never make interrupted actions replayable.
SELECT 1;
