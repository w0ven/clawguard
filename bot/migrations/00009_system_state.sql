-- +goose Up
CREATE TABLE IF NOT EXISTS system_state (
    id INT PRIMARY KEY DEFAULT 1,
    ai_paused BOOLEAN NOT NULL DEFAULT FALSE,
    actions_paused BOOLEAN NOT NULL DEFAULT FALSE,
    frozen BOOLEAN NOT NULL DEFAULT FALSE,
    ai_paused_reason TEXT NOT NULL DEFAULT '',
    ai_budget_locked BOOLEAN NOT NULL DEFAULT FALSE,
    ai_budget_locked_date DATE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by BIGINT
);

INSERT INTO system_state (id)
VALUES (1)
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS system_state;
