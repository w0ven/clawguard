package store

import "context"

const listProviders = `-- name: ListProviders :many
SELECT id, key, label, type, base_url, api_key_enc, timeout_ms, extra_headers, enabled, created_at, updated_at
FROM llm_providers
ORDER BY created_at ASC, id ASC
`

const countProviders = `-- name: CountProviders :one
SELECT COUNT(*)::BIGINT
FROM llm_providers
`

const getProviderByKey = `-- name: GetProviderByKey :one
SELECT id, key, label, type, base_url, api_key_enc, timeout_ms, extra_headers, enabled, created_at, updated_at
FROM llm_providers
WHERE key = $1
`

const createProvider = `-- name: CreateProvider :one
INSERT INTO llm_providers (
    key,
    label,
    type,
    base_url,
    api_key_enc,
    timeout_ms,
    extra_headers,
    enabled
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, key, label, type, base_url, api_key_enc, timeout_ms, extra_headers, enabled, created_at, updated_at
`

const updateProvider = `-- name: UpdateProvider :one
UPDATE llm_providers
SET label = $2,
    type = $3,
    base_url = $4,
    api_key_enc = $5,
    timeout_ms = $6,
    extra_headers = $7,
    enabled = $8,
    updated_at = now()
WHERE key = $1
RETURNING id, key, label, type, base_url, api_key_enc, timeout_ms, extra_headers, enabled, created_at, updated_at
`

const deleteProvider = `-- name: DeleteProvider :exec
DELETE FROM llm_providers
WHERE key = $1
`

type CreateProviderParams struct {
	Key          string
	Label        string
	Type         string
	BaseUrl      string
	ApiKeyEnc    string
	TimeoutMs    int32
	ExtraHeaders []byte
	Enabled      bool
}

type UpdateProviderParams struct {
	Key          string
	Label        string
	Type         string
	BaseUrl      string
	ApiKeyEnc    string
	TimeoutMs    int32
	ExtraHeaders []byte
	Enabled      bool
}

func (q *Queries) ListProviders(ctx context.Context) ([]LlmProvider, error) {
	rows, err := q.db.Query(ctx, listProviders)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []LlmProvider
	for rows.Next() {
		var i LlmProvider
		if err := rows.Scan(&i.ID, &i.Key, &i.Label, &i.Type, &i.BaseURL, &i.ApiKeyEnc, &i.TimeoutMs, &i.ExtraHeaders, &i.Enabled, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) CountProviders(ctx context.Context) (int64, error) {
	row := q.db.QueryRow(ctx, countProviders)
	var count int64
	err := row.Scan(&count)
	return count, err
}

func (q *Queries) GetProviderByKey(ctx context.Context, key string) (LlmProvider, error) {
	row := q.db.QueryRow(ctx, getProviderByKey, key)
	var i LlmProvider
	err := row.Scan(&i.ID, &i.Key, &i.Label, &i.Type, &i.BaseURL, &i.ApiKeyEnc, &i.TimeoutMs, &i.ExtraHeaders, &i.Enabled, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

func (q *Queries) CreateProvider(ctx context.Context, arg CreateProviderParams) (LlmProvider, error) {
	row := q.db.QueryRow(ctx, createProvider, arg.Key, arg.Label, arg.Type, arg.BaseUrl, arg.ApiKeyEnc, arg.TimeoutMs, arg.ExtraHeaders, arg.Enabled)
	var i LlmProvider
	err := row.Scan(&i.ID, &i.Key, &i.Label, &i.Type, &i.BaseURL, &i.ApiKeyEnc, &i.TimeoutMs, &i.ExtraHeaders, &i.Enabled, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

func (q *Queries) UpdateProvider(ctx context.Context, arg UpdateProviderParams) (LlmProvider, error) {
	row := q.db.QueryRow(ctx, updateProvider, arg.Key, arg.Label, arg.Type, arg.BaseUrl, arg.ApiKeyEnc, arg.TimeoutMs, arg.ExtraHeaders, arg.Enabled)
	var i LlmProvider
	err := row.Scan(&i.ID, &i.Key, &i.Label, &i.Type, &i.BaseURL, &i.ApiKeyEnc, &i.TimeoutMs, &i.ExtraHeaders, &i.Enabled, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

func (q *Queries) DeleteProvider(ctx context.Context, key string) error {
	_, err := q.db.Exec(ctx, deleteProvider, key)
	return err
}
