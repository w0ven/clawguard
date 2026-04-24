-- +goose Up
-- 旧外键是 NO ACTION，导致删 provider/model 时因为 ai_decisions 里有历史行被阻止
-- 改成 SET NULL：历史审计行保留，provider_id/model_id 变 NULL
ALTER TABLE ai_decisions
  DROP CONSTRAINT IF EXISTS ai_decisions_provider_id_fkey,
  DROP CONSTRAINT IF EXISTS ai_decisions_model_id_fkey;

ALTER TABLE ai_decisions
  ADD CONSTRAINT ai_decisions_provider_id_fkey
    FOREIGN KEY (provider_id) REFERENCES llm_providers(id) ON DELETE SET NULL,
  ADD CONSTRAINT ai_decisions_model_id_fkey
    FOREIGN KEY (model_id) REFERENCES llm_models(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE ai_decisions
  DROP CONSTRAINT IF EXISTS ai_decisions_provider_id_fkey,
  DROP CONSTRAINT IF EXISTS ai_decisions_model_id_fkey;

ALTER TABLE ai_decisions
  ADD CONSTRAINT ai_decisions_provider_id_fkey
    FOREIGN KEY (provider_id) REFERENCES llm_providers(id),
  ADD CONSTRAINT ai_decisions_model_id_fkey
    FOREIGN KEY (model_id) REFERENCES llm_models(id);
