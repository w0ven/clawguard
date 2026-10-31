-- +goose Up
CREATE TABLE IF NOT EXISTS dual_captcha_claims (
    pending_id BIGINT PRIMARY KEY,
    token TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('validating', 'verified', 'applying', 'completed', 'conflict')),
    target_rights JSONB,
    lease_until TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
-- Preserve completed work and recovery state when reapplying this migration.
SELECT 1;
