package store

import "context"

const listModels = `-- name: ListModels :many
SELECT id, provider_id, model_key, label, api_format, enabled, supports_vision, supports_json, supports_tools, capability_tags, priority, meta, probe_enabled, probe_interval_seconds, created_at, updated_at
FROM llm_models
ORDER BY priority ASC, id ASC
`

const listModelsByProvider = `-- name: ListModelsByProvider :many
SELECT id, provider_id, model_key, label, api_format, enabled, supports_vision, supports_json, supports_tools, capability_tags, priority, meta, probe_enabled, probe_interval_seconds, created_at, updated_at
FROM llm_models
WHERE provider_id = $1
ORDER BY priority ASC, id ASC
`

const getModelByRef = `-- name: GetModelByRef :one
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
  AND m.model_key = $2
`

const createModel = `-- name: CreateModel :one
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
RETURNING id, provider_id, model_key, label, api_format, enabled, supports_vision, supports_json, supports_tools, capability_tags, priority, meta, probe_enabled, probe_interval_seconds, created_at, updated_at
`

const updateModel = `-- name: UpdateModel :one
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
RETURNING id, provider_id, model_key, label, api_format, enabled, supports_vision, supports_json, supports_tools, capability_tags, priority, meta, probe_enabled, probe_interval_seconds, created_at, updated_at
`

const deleteModel = `-- name: DeleteModel :exec
DELETE FROM llm_models
WHERE provider_id = $1
  AND model_key = $2
`

const getEnabledModelsWithProvider = `-- name: GetEnabledModelsWithProvider :many
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
ORDER BY p.key ASC, m.priority ASC, m.id ASC
`

type CreateModelParams struct {
	ProviderID           int64
	ModelKey             string
	Label                string
	ApiFormat            string
	Enabled              bool
	SupportsVision       bool
	SupportsJson         bool
	SupportsTools        bool
	CapabilityTags       []string
	Priority             int32
	Meta                 []byte
	ProbeEnabled         bool
	ProbeIntervalSeconds int32
}

type UpdateModelParams struct {
	ProviderID           int64
	ModelKey             string
	Label                string
	ApiFormat            string
	Enabled              bool
	SupportsVision       bool
	SupportsJson         bool
	SupportsTools        bool
	CapabilityTags       []string
	Priority             int32
	Meta                 []byte
	ProbeEnabled         bool
	ProbeIntervalSeconds int32
}

func (q *Queries) ListModels(ctx context.Context) ([]LlmModel, error) {
	rows, err := q.db.Query(ctx, listModels)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []LlmModel
	for rows.Next() {
		var i LlmModel
		if err := rows.Scan(&i.ID, &i.ProviderID, &i.ModelKey, &i.Label, &i.ApiFormat, &i.Enabled, &i.SupportsVision, &i.SupportsJson, &i.SupportsTools, &i.CapabilityTags, &i.Priority, &i.Meta, &i.ProbeEnabled, &i.ProbeIntervalSeconds, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) ListModelsByProvider(ctx context.Context, providerID int64) ([]LlmModel, error) {
	rows, err := q.db.Query(ctx, listModelsByProvider, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []LlmModel
	for rows.Next() {
		var i LlmModel
		if err := rows.Scan(&i.ID, &i.ProviderID, &i.ModelKey, &i.Label, &i.ApiFormat, &i.Enabled, &i.SupportsVision, &i.SupportsJson, &i.SupportsTools, &i.CapabilityTags, &i.Priority, &i.Meta, &i.ProbeEnabled, &i.ProbeIntervalSeconds, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) GetModelByRef(ctx context.Context, providerKey, modelKey string) (GetModelByRefRow, error) {
	row := q.db.QueryRow(ctx, getModelByRef, providerKey, modelKey)
	var i GetModelByRefRow
	err := row.Scan(&i.ID, &i.ProviderID, &i.ProviderKey, &i.ModelKey, &i.Label, &i.ApiFormat, &i.Enabled, &i.SupportsVision, &i.SupportsJson, &i.SupportsTools, &i.CapabilityTags, &i.Priority, &i.Meta, &i.ProbeEnabled, &i.ProbeIntervalSeconds, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

func (q *Queries) CreateModel(ctx context.Context, arg CreateModelParams) (LlmModel, error) {
	row := q.db.QueryRow(ctx, createModel, arg.ProviderID, arg.ModelKey, arg.Label, arg.ApiFormat, arg.Enabled, arg.SupportsVision, arg.SupportsJson, arg.SupportsTools, arg.CapabilityTags, arg.Priority, arg.Meta, arg.ProbeEnabled, arg.ProbeIntervalSeconds)
	var i LlmModel
	err := row.Scan(&i.ID, &i.ProviderID, &i.ModelKey, &i.Label, &i.ApiFormat, &i.Enabled, &i.SupportsVision, &i.SupportsJson, &i.SupportsTools, &i.CapabilityTags, &i.Priority, &i.Meta, &i.ProbeEnabled, &i.ProbeIntervalSeconds, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

func (q *Queries) UpdateModel(ctx context.Context, arg UpdateModelParams) (LlmModel, error) {
	row := q.db.QueryRow(ctx, updateModel, arg.ProviderID, arg.ModelKey, arg.Label, arg.ApiFormat, arg.Enabled, arg.SupportsVision, arg.SupportsJson, arg.SupportsTools, arg.CapabilityTags, arg.Priority, arg.Meta, arg.ProbeEnabled, arg.ProbeIntervalSeconds)
	var i LlmModel
	err := row.Scan(&i.ID, &i.ProviderID, &i.ModelKey, &i.Label, &i.ApiFormat, &i.Enabled, &i.SupportsVision, &i.SupportsJson, &i.SupportsTools, &i.CapabilityTags, &i.Priority, &i.Meta, &i.ProbeEnabled, &i.ProbeIntervalSeconds, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

func (q *Queries) DeleteModel(ctx context.Context, providerID int64, modelKey string) error {
	_, err := q.db.Exec(ctx, deleteModel, providerID, modelKey)
	return err
}

func (q *Queries) GetEnabledModelsWithProvider(ctx context.Context) ([]GetEnabledModelsWithProviderRow, error) {
	rows, err := q.db.Query(ctx, getEnabledModelsWithProvider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []GetEnabledModelsWithProviderRow
	for rows.Next() {
		var i GetEnabledModelsWithProviderRow
		if err := rows.Scan(&i.ID, &i.ProviderID, &i.ProviderKey, &i.ProviderLabel, &i.ProviderType, &i.BaseURL, &i.ApiKeyEnc, &i.TimeoutMs, &i.ExtraHeaders, &i.ProviderEnabled, &i.ModelKey, &i.Label, &i.ApiFormat, &i.Enabled, &i.SupportsVision, &i.SupportsJson, &i.SupportsTools, &i.CapabilityTags, &i.Priority, &i.Meta, &i.ProbeEnabled, &i.ProbeIntervalSeconds, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
