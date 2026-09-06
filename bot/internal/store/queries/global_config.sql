-- name: GetGlobalConfig :one
SELECT id, config, updated_at, version
FROM global_config
WHERE id = 1;

-- name: GetGlobalConfigForUpdate :one
SELECT id, config, updated_at, version
FROM global_config
WHERE id = 1
FOR UPDATE;

-- name: UpdateGlobalConfig :one
UPDATE global_config
SET config = $1,
    updated_at = NOW(),
    version = version + 1
WHERE id = 1
RETURNING id, config, updated_at, version;

-- name: UpsertGlobalConfig :one
INSERT INTO global_config (
    id,
    config,
    updated_at,
    version
) VALUES (1, $1, NOW(), 1)
ON CONFLICT (id) DO UPDATE
SET config = EXCLUDED.config,
    updated_at = NOW(),
    version = global_config.version + 1
RETURNING id, config, updated_at, version;
