package store

import (
	"context"
)

const getGlobalConfig = `-- name: GetGlobalConfig :one
SELECT id, config, updated_at, version
FROM global_config
WHERE id = 1
`

const getGlobalConfigForUpdate = `-- name: GetGlobalConfigForUpdate :one
SELECT id, config, updated_at, version
FROM global_config
WHERE id = 1
FOR UPDATE
`

const updateGlobalConfig = `-- name: UpdateGlobalConfig :one
UPDATE global_config
SET config = $1,
    updated_at = NOW(),
    version = version + 1
WHERE id = 1
RETURNING id, config, updated_at, version
`

const upsertGlobalConfig = `-- name: UpsertGlobalConfig :one
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
RETURNING id, config, updated_at, version
`

type UpdateGlobalConfigParams struct {
	Config []byte
}

type UpsertGlobalConfigParams struct {
	Config []byte
}

func scanGlobalConfig(row interface{ Scan(...any) error }, i *GlobalConfig) error {
	return row.Scan(&i.ID, &i.Config, &i.UpdatedAt, &i.Version)
}

func (q *Queries) GetGlobalConfig(ctx context.Context) (GlobalConfig, error) {
	row := q.db.QueryRow(ctx, getGlobalConfig)
	var i GlobalConfig
	err := scanGlobalConfig(row, &i)
	return i, err
}

func (q *Queries) GetGlobalConfigForUpdate(ctx context.Context) (GlobalConfig, error) {
	row := q.db.QueryRow(ctx, getGlobalConfigForUpdate)
	var i GlobalConfig
	err := scanGlobalConfig(row, &i)
	return i, err
}

func (q *Queries) UpdateGlobalConfig(ctx context.Context, arg UpdateGlobalConfigParams) (GlobalConfig, error) {
	row := q.db.QueryRow(ctx, updateGlobalConfig, arg.Config)
	var i GlobalConfig
	err := scanGlobalConfig(row, &i)
	return i, err
}

func (q *Queries) UpsertGlobalConfig(ctx context.Context, arg UpsertGlobalConfigParams) (GlobalConfig, error) {
	row := q.db.QueryRow(ctx, upsertGlobalConfig, arg.Config)
	var i GlobalConfig
	err := scanGlobalConfig(row, &i)
	return i, err
}
