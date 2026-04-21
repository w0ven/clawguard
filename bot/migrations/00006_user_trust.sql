-- +goose Up
CREATE TABLE user_trust (
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status TEXT NOT NULL DEFAULT 'new',
    score DOUBLE PRECISION NOT NULL DEFAULT 0.5,
    messages_checked INT NOT NULL DEFAULT 0,
    messages_clean INT NOT NULL DEFAULT 0,
    graduated_at TIMESTAMPTZ,
    notes TEXT,
    PRIMARY KEY (chat_id, user_id)
);

CREATE TABLE ai_decisions (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    message_id BIGINT NOT NULL,
    message_text TEXT,
    model TEXT NOT NULL,
    prompt_version TEXT NOT NULL,
    verdict TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL,
    category TEXT NOT NULL,
    reason TEXT,
    action_taken TEXT NOT NULL DEFAULT 'none',
    admin_override TEXT,
    latency_ms INT NOT NULL DEFAULT 0,
    cost_cents DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_user_trust_status ON user_trust(chat_id, status, user_id);
CREATE INDEX idx_ai_decisions_user ON ai_decisions(chat_id, user_id, created_at DESC);
CREATE INDEX idx_ai_decisions_pending ON ai_decisions(admin_override) WHERE admin_override IS NULL;
CREATE INDEX idx_ai_decisions_created_day ON ai_decisions(created_at DESC);
CREATE INDEX idx_ai_decisions_model_day ON ai_decisions(model, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_ai_decisions_model_day;
DROP INDEX IF EXISTS idx_ai_decisions_created_day;
DROP INDEX IF EXISTS idx_ai_decisions_pending;
DROP INDEX IF EXISTS idx_ai_decisions_user;
DROP INDEX IF EXISTS idx_user_trust_status;
DROP TABLE IF EXISTS ai_decisions;
DROP TABLE IF EXISTS user_trust;
