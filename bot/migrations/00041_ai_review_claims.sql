-- +goose Up
CREATE TABLE IF NOT EXISTS ai_unban_review_claims (
 decision_id BIGINT PRIMARY KEY REFERENCES ai_decisions(id) ON DELETE CASCADE,
 token TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('claimed','telegram_done','completed','conflict')),
 lease_until TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose Down
-- Preserve recovery history and completed-operation idempotence.
SELECT 1;
