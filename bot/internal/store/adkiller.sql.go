package store

import (
	"context"
	"time"
)

const getAdkillerSecret = `-- name: GetAdkillerSecret :one
SELECT id, api_key_enc, updated_at
FROM adkiller_secrets
WHERE id = 1
`

const upsertAdkillerSecret = `-- name: UpsertAdkillerSecret :one
INSERT INTO adkiller_secrets (
    id,
    api_key_enc,
    updated_at
) VALUES (1, $1, NOW())
ON CONFLICT (id) DO UPDATE
SET api_key_enc = EXCLUDED.api_key_enc,
    updated_at = NOW()
RETURNING id, api_key_enc, updated_at
`

const clearAdkillerSecret = `-- name: ClearAdkillerSecret :one
UPDATE adkiller_secrets
SET api_key_enc = '',
    updated_at = NOW()
WHERE id = 1
RETURNING id, api_key_enc, updated_at
`

type AdkillerSecret struct {
	ID        int32
	ApiKeyEnc string
	UpdatedAt time.Time
}

func (q *Queries) GetAdkillerSecret(ctx context.Context) (AdkillerSecret, error) {
	row := q.db.QueryRow(ctx, getAdkillerSecret)
	var i AdkillerSecret
	err := row.Scan(&i.ID, &i.ApiKeyEnc, &i.UpdatedAt)
	return i, err
}

func (q *Queries) UpsertAdkillerSecret(ctx context.Context, apiKeyEnc string) (AdkillerSecret, error) {
	row := q.db.QueryRow(ctx, upsertAdkillerSecret, apiKeyEnc)
	var i AdkillerSecret
	err := row.Scan(&i.ID, &i.ApiKeyEnc, &i.UpdatedAt)
	return i, err
}

func (q *Queries) ClearAdkillerSecret(ctx context.Context) (AdkillerSecret, error) {
	row := q.db.QueryRow(ctx, clearAdkillerSecret)
	var i AdkillerSecret
	err := row.Scan(&i.ID, &i.ApiKeyEnc, &i.UpdatedAt)
	return i, err
}
