-- +goose Up
CREATE TABLE IF NOT EXISTS global_exempt_users (
    user_id BIGINT PRIMARY KEY CHECK (user_id > 0),
    note TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
-- Preserve the list on rollback; older application versions ignore it.
SELECT 1;
