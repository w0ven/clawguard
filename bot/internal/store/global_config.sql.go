package store

import (
	"context"
)

const getGlobalConfig = `-- name: GetGlobalConfig :one
SELECT id, config, updated_at
FROM global_config
WHERE id = 1
`

const upsertGlobalConfig = `-- name: UpsertGlobalConfig :one
INSERT INTO global_config (
    id,
    config,
    updated_at
) VALUES (1, $1, NOW())
ON CONFLICT (id) DO UPDATE
SET config = EXCLUDED.config,
    updated_at = NOW()
RETURNING id, config, updated_at
`

type UpsertGlobalConfigParams struct {
	Config []byte
}

func (q *Queries) GetGlobalConfig(ctx context.Context) (GlobalConfig, error) {
	row := q.db.QueryRow(ctx, getGlobalConfig)
	var i GlobalConfig
	err := row.Scan(&i.ID, &i.Config, &i.UpdatedAt)
	return i, err
}

func (q *Queries) UpsertGlobalConfig(ctx context.Context, arg UpsertGlobalConfigParams) (GlobalConfig, error) {
	row := q.db.QueryRow(ctx, upsertGlobalConfig, arg.Config)
	var i GlobalConfig
	err := row.Scan(&i.ID, &i.Config, &i.UpdatedAt)
	return i, err
}
