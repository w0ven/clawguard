-- +goose Up
CREATE TABLE IF NOT EXISTS ai_false_positive_reviews (
    decision_id BIGINT PRIMARY KEY REFERENCES ai_decisions(id) ON DELETE CASCADE,
    reviewed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
-- Keep review idempotency history across downgrade/re-upgrade.
SELECT 1;
