-- name: GetGlobalConfig :one
SELECT id, config, updated_at
FROM global_config
WHERE id = 1;

-- name: UpsertGlobalConfig :one
INSERT INTO global_config (
    id,
    config,
    updated_at
) VALUES (1, $1, NOW())
ON CONFLICT (id) DO UPDATE
SET config = EXCLUDED.config,
    updated_at = NOW()
RETURNING id, config, updated_at;
