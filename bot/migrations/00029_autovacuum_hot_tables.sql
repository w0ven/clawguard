-- +goose Up
-- Retention cleanup and trust counters churn these tables faster than the
-- PostgreSQL defaults notice at the current production size. Keep vacuum and
-- planner statistics current without resorting to blocking VACUUM FULL runs.
ALTER TABLE user_trust SET (
  autovacuum_vacuum_scale_factor = 0.05,
  autovacuum_vacuum_threshold = 100,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_analyze_threshold = 100
);

ALTER TABLE profile_check_logs SET (
  autovacuum_vacuum_scale_factor = 0.05,
  autovacuum_vacuum_threshold = 100,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_analyze_threshold = 100
);

ALTER TABLE ai_decisions SET (
  autovacuum_vacuum_scale_factor = 0.05,
  autovacuum_vacuum_threshold = 50,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_analyze_threshold = 50
);

ALTER TABLE scheduled_message_runs SET (
  autovacuum_vacuum_scale_factor = 0.05,
  autovacuum_vacuum_threshold = 25,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_analyze_threshold = 25
);

-- This table has only a handful of live rows but each health probe updates them,
-- so the default 50-row threshold is disproportionately high.
ALTER TABLE llm_model_stats SET (
  autovacuum_vacuum_scale_factor = 0.0,
  autovacuum_vacuum_threshold = 10,
  autovacuum_analyze_scale_factor = 0.0,
  autovacuum_analyze_threshold = 10
);

-- +goose Down
ALTER TABLE user_trust RESET (
  autovacuum_vacuum_scale_factor,
  autovacuum_vacuum_threshold,
  autovacuum_analyze_scale_factor,
  autovacuum_analyze_threshold
);
ALTER TABLE profile_check_logs RESET (
  autovacuum_vacuum_scale_factor,
  autovacuum_vacuum_threshold,
  autovacuum_analyze_scale_factor,
  autovacuum_analyze_threshold
);
ALTER TABLE ai_decisions RESET (
  autovacuum_vacuum_scale_factor,
  autovacuum_vacuum_threshold,
  autovacuum_analyze_scale_factor,
  autovacuum_analyze_threshold
);
ALTER TABLE scheduled_message_runs RESET (
  autovacuum_vacuum_scale_factor,
  autovacuum_vacuum_threshold,
  autovacuum_analyze_scale_factor,
  autovacuum_analyze_threshold
);
ALTER TABLE llm_model_stats RESET (
  autovacuum_vacuum_scale_factor,
  autovacuum_vacuum_threshold,
  autovacuum_analyze_scale_factor,
  autovacuum_analyze_threshold
);
