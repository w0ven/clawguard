-- +goose Up
ALTER TABLE admins
    ADD COLUMN IF NOT EXISTS notes TEXT,
    ADD COLUMN IF NOT EXISTS group_scope JSONB NOT NULL DEFAULT '[]'::jsonb;

-- This migration originally ended with a one-off UPDATE that promoted a single
-- hardcoded operator account to role 'owner'. It was dropped before the public
-- release: it only ever matched that one private Telegram ID, and on a fresh
-- database it matched nothing, because migrations run before any admin row is
-- seeded. The ADD COLUMN default above already backfills group_scope = '[]'
-- for every existing row, so no data change is lost here.
--
-- Owner bootstrapping is now driven by SUPER_ADMIN_IDS (see cmd/clawguard).

-- +goose Down
ALTER TABLE admins
    DROP COLUMN IF EXISTS group_scope,
    DROP COLUMN IF EXISTS notes;
