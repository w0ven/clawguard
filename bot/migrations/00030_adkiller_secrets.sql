-- +goose Up
CREATE TABLE adkiller_secrets (
  id           INT PRIMARY KEY CHECK (id = 1),
  api_key_enc  TEXT NOT NULL DEFAULT '',
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO adkiller_secrets (id, api_key_enc)
VALUES (1, '')
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS adkiller_secrets;
