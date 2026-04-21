-- name: ListProviders :many
SELECT id, key, label, type, base_url, api_key_enc, timeout_ms, extra_headers, enabled, created_at, updated_at
FROM llm_providers
ORDER BY created_at ASC, id ASC;

-- name: CountProviders :one
SELECT COUNT(*)::BIGINT
FROM llm_providers;

-- name: GetProviderByKey :one
SELECT id, key, label, type, base_url, api_key_enc, timeout_ms, extra_headers, enabled, created_at, updated_at
FROM llm_providers
WHERE key = $1;

-- name: CreateProvider :one
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
RETURNING id, key, label, type, base_url, api_key_enc, timeout_ms, extra_headers, enabled, created_at, updated_at;

-- name: UpdateProvider :one
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
RETURNING id, key, label, type, base_url, api_key_enc, timeout_ms, extra_headers, enabled, created_at, updated_at;

-- name: DeleteProvider :exec
DELETE FROM llm_providers
WHERE key = $1;
