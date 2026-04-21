-- name: ListModels :many
SELECT id, provider_id, model_key, label, api_format, enabled, supports_vision, supports_json, supports_tools, capability_tags, priority, meta, probe_enabled, probe_interval_seconds, created_at, updated_at
FROM llm_models
ORDER BY priority ASC, id ASC;

-- name: ListModelsByProvider :many
SELECT id, provider_id, model_key, label, api_format, enabled, supports_vision, supports_json, supports_tools, capability_tags, priority, meta, probe_enabled, probe_interval_seconds, created_at, updated_at
FROM llm_models
WHERE provider_id = $1
ORDER BY priority ASC, id ASC;

-- name: GetModelByRef :one
SELECT
    m.id,
    m.provider_id,
    p.key AS provider_key,
    m.model_key,
    m.label,
    m.api_format,
    m.enabled,
    m.supports_vision,
    m.supports_json,
    m.supports_tools,
    m.capability_tags,
    m.priority,
    m.meta,
    m.probe_enabled,
    m.probe_interval_seconds,
    m.created_at,
    m.updated_at
FROM llm_models m
JOIN llm_providers p ON p.id = m.provider_id
WHERE p.key = $1
  AND m.model_key = $2;

-- name: CreateModel :one
INSERT INTO llm_models (
    provider_id,
    model_key,
    label,
    api_format,
    enabled,
    supports_vision,
    supports_json,
    supports_tools,
    capability_tags,
    priority,
    meta,
    probe_enabled,
    probe_interval_seconds
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING id, provider_id, model_key, label, api_format, enabled, supports_vision, supports_json, supports_tools, capability_tags, priority, meta, probe_enabled, probe_interval_seconds, created_at, updated_at;

-- name: UpdateModel :one
UPDATE llm_models
SET label = $3,
    api_format = $4,
    enabled = $5,
    supports_vision = $6,
    supports_json = $7,
    supports_tools = $8,
    capability_tags = $9,
    priority = $10,
    meta = $11,
    probe_enabled = $12,
    probe_interval_seconds = $13,
    updated_at = now()
WHERE provider_id = $1
  AND model_key = $2
RETURNING id, provider_id, model_key, label, api_format, enabled, supports_vision, supports_json, supports_tools, capability_tags, priority, meta, probe_enabled, probe_interval_seconds, created_at, updated_at;

-- name: DeleteModel :exec
DELETE FROM llm_models
WHERE provider_id = $1
  AND model_key = $2;

-- name: GetEnabledModelsWithProvider :many
SELECT
    m.id,
    m.provider_id,
    p.key AS provider_key,
    p.label AS provider_label,
    p.type AS provider_type,
    p.base_url,
    p.api_key_enc,
    p.timeout_ms,
    p.extra_headers,
    p.enabled AS provider_enabled,
    m.model_key,
    m.label,
    m.api_format,
    m.enabled,
    m.supports_vision,
    m.supports_json,
    m.supports_tools,
    m.capability_tags,
    m.priority,
    m.meta,
    m.probe_enabled,
    m.probe_interval_seconds,
    m.created_at,
    m.updated_at
FROM llm_models m
JOIN llm_providers p ON p.id = m.provider_id
WHERE p.enabled = true
  AND m.enabled = true
ORDER BY p.key ASC, m.priority ASC, m.id ASC;
