-- +goose Up
CREATE TABLE llm_providers (
  id            BIGSERIAL PRIMARY KEY,
  key           TEXT UNIQUE NOT NULL,
  label         TEXT NOT NULL,
  type          TEXT NOT NULL DEFAULT 'openai_compatible',
  base_url      TEXT NOT NULL,
  api_key_enc   TEXT NOT NULL,
  timeout_ms    INT  NOT NULL DEFAULT 30000,
  extra_headers JSONB NOT NULL DEFAULT '{}'::jsonb,
  enabled       BOOLEAN NOT NULL DEFAULT true,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE llm_models (
  id                BIGSERIAL PRIMARY KEY,
  provider_id       BIGINT NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
  model_key         TEXT NOT NULL,
  label             TEXT NOT NULL,
  api_format        TEXT NOT NULL DEFAULT 'openai_chat',
  enabled           BOOLEAN NOT NULL DEFAULT true,
  supports_vision   BOOLEAN NOT NULL DEFAULT false,
  supports_json     BOOLEAN NOT NULL DEFAULT true,
  supports_tools    BOOLEAN NOT NULL DEFAULT false,
  capability_tags   TEXT[] NOT NULL DEFAULT '{}',
  priority          INT    NOT NULL DEFAULT 100,
  meta              JSONB  NOT NULL DEFAULT '{}'::jsonb,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(provider_id, model_key)
);

ALTER TABLE ai_decisions
  ADD COLUMN IF NOT EXISTS provider_id BIGINT REFERENCES llm_providers(id),
  ADD COLUMN IF NOT EXISTS model_id    BIGINT REFERENCES llm_models(id);

CREATE INDEX IF NOT EXISTS idx_ai_decisions_model_id ON ai_decisions(model_id);

-- +goose Down
DROP INDEX IF EXISTS idx_ai_decisions_model_id;

ALTER TABLE ai_decisions
  DROP COLUMN IF EXISTS model_id,
  DROP COLUMN IF EXISTS provider_id;

DROP TABLE IF EXISTS llm_models;
DROP TABLE IF EXISTS llm_providers;
