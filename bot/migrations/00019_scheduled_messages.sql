CREATE TABLE IF NOT EXISTS scheduled_messages (
  id BIGSERIAL PRIMARY KEY,
  chat_id BIGINT NOT NULL,
  name TEXT NOT NULL,
  schedule_type TEXT NOT NULL CHECK (schedule_type IN ('interval','daily')),
  interval_minutes INT,
  daily_times TEXT[],
  timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
  content TEXT NOT NULL,
  buttons JSONB,
  auto_delete_seconds INT NOT NULL DEFAULT 0,
  enabled BOOLEAN NOT NULL DEFAULT true,
  status TEXT NOT NULL DEFAULT 'active',
  last_run_at TIMESTAMPTZ,
  last_message_id BIGINT,
  last_error TEXT,
  last_skip_reason TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_scheduled_messages_chat ON scheduled_messages(chat_id);
CREATE INDEX IF NOT EXISTS idx_scheduled_messages_enabled ON scheduled_messages(enabled, status);

CREATE TABLE IF NOT EXISTS scheduled_message_runs (
  id BIGSERIAL PRIMARY KEY,
  scheduled_message_id BIGINT NOT NULL REFERENCES scheduled_messages(id) ON DELETE CASCADE,
  ran_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  success BOOLEAN NOT NULL,
  tg_message_id BIGINT,
  rendered_preview TEXT,
  error TEXT,
  duration_ms INT
);

CREATE INDEX IF NOT EXISTS idx_scheduled_message_runs_sm ON scheduled_message_runs(scheduled_message_id, ran_at DESC);
