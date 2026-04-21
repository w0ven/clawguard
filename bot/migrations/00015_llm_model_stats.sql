-- +goose Up
CREATE TABLE llm_model_stats (
    model_id       BIGINT PRIMARY KEY REFERENCES llm_models(id) ON DELETE CASCADE,
    last_check_at  TIMESTAMPTZ,
    last_ok_at     TIMESTAMPTZ,
    last_error     TEXT NOT NULL DEFAULT '',
    healthy        BOOLEAN NOT NULL DEFAULT true,
    latency_p50_ms INT,
    latency_p95_ms INT,
    success_1h     INT NOT NULL DEFAULT 0,
    fail_1h        INT NOT NULL DEFAULT 0,
    success_24h    INT NOT NULL DEFAULT 0,
    fail_24h       INT NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_llm_model_stats_healthy ON llm_model_stats(healthy);

-- +goose Down
DROP INDEX IF EXISTS idx_llm_model_stats_healthy;
DROP TABLE IF EXISTS llm_model_stats;
