-- name: GetAdkillerSecret :one
SELECT id, api_key_enc, updated_at
FROM adkiller_secrets
WHERE id = 1;

-- name: UpsertAdkillerSecret :one
INSERT INTO adkiller_secrets (
    id,
    api_key_enc,
    updated_at
) VALUES (1, $1, NOW())
ON CONFLICT (id) DO UPDATE
SET api_key_enc = EXCLUDED.api_key_enc,
    updated_at = NOW()
RETURNING id, api_key_enc, updated_at;

-- name: ClearAdkillerSecret :one
UPDATE adkiller_secrets
SET api_key_enc = '',
    updated_at = NOW()
WHERE id = 1
RETURNING id, api_key_enc, updated_at;
