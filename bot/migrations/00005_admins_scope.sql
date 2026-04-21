-- +goose Up
ALTER TABLE admins
    ADD COLUMN IF NOT EXISTS notes TEXT,
    ADD COLUMN IF NOT EXISTS group_scope JSONB NOT NULL DEFAULT '[]'::jsonb;

UPDATE admins
SET role = 'owner',
    group_scope = '[]'::jsonb
WHERE telegram_id = 6425070392;

-- +goose Down
ALTER TABLE admins
    DROP COLUMN IF EXISTS group_scope,
    DROP COLUMN IF EXISTS notes;
