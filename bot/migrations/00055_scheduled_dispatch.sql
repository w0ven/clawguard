-- +goose Up
-- Attempts are retained even if a scheduled message is deleted. A dispatched
-- attempt is never automatically replayed: Telegram has no idempotency key.
CREATE TABLE IF NOT EXISTS scheduled_dispatch_attempts (
  id BIGSERIAL PRIMARY KEY,
  scheduled_message_id BIGINT REFERENCES scheduled_messages(id) ON DELETE SET NULL,
  chat_id BIGINT NOT NULL,
  scheduled_at TIMESTAMPTZ,
  state TEXT NOT NULL CHECK (state IN ('dispatching','sent','failed','uncertain')),
  telegram_message_id BIGINT,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((state='sent') = (telegram_message_id IS NOT NULL)),
  CHECK (telegram_message_id IS NULL OR telegram_message_id > 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS scheduled_dispatch_slot ON scheduled_dispatch_attempts(scheduled_message_id, scheduled_at) WHERE scheduled_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS scheduled_delete_jobs (
  id BIGSERIAL PRIMARY KEY,
  attempt_id BIGINT NOT NULL UNIQUE REFERENCES scheduled_dispatch_attempts(id),
  chat_id BIGINT NOT NULL,
  telegram_message_id BIGINT NOT NULL,
  delete_at TIMESTAMPTZ NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','leased','done','failed')),
  attempts INT NOT NULL DEFAULT 0,
  lease_token TEXT,
  lease_until TIMESTAMPTZ,
  last_error TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS scheduled_delete_due ON scheduled_delete_jobs(delete_at, id) WHERE state IN ('pending','leased');

-- +goose Down
-- Preserve attempts and deletion tasks; losing them would permit replay and
-- abandon confirmed messages. Re-applying this migration is idempotent.
SELECT 1;
