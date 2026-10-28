-- +goose Up
CREATE TABLE IF NOT EXISTS ai_mute_ownership (
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    decision_id BIGINT NOT NULL REFERENCES ai_decisions(id) ON DELETE CASCADE,
    until_date BIGINT NOT NULL,
    token TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','active','reviewing','done','conflict')),
    lease_until TIMESTAMPTZ,
    PRIMARY KEY (chat_id,user_id)
);
CREATE TABLE IF NOT EXISTS ai_mute_review_claims (
    decision_id BIGINT PRIMARY KEY REFERENCES ai_decisions(id) ON DELETE CASCADE,
    token TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('claimed','telegram_done','completed','conflict')),
    lease_until TIMESTAMPTZ NOT NULL
);
-- +goose Down
-- Keep provenance and retry history.
SELECT 1;
