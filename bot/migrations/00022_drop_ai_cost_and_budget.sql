-- +goose Up
ALTER TABLE ai_decisions DROP COLUMN IF EXISTS cost_cents;
ALTER TABLE system_state DROP COLUMN IF EXISTS ai_budget_locked;
ALTER TABLE system_state DROP COLUMN IF EXISTS ai_budget_locked_date;

-- +goose Down
ALTER TABLE ai_decisions ADD COLUMN cost_cents DOUBLE PRECISION DEFAULT 0;
ALTER TABLE system_state ADD COLUMN ai_budget_locked BOOLEAN DEFAULT FALSE;
ALTER TABLE system_state ADD COLUMN ai_budget_locked_date TIMESTAMPTZ;
