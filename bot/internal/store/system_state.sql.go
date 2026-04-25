package store

import "context"

const getSystemState = `-- name: GetSystemState :one
SELECT id, ai_paused, actions_paused, frozen, ai_paused_reason, updated_at, updated_by
FROM system_state
WHERE id = 1
`

const updateSystemState = `-- name: UpdateSystemState :one
UPDATE system_state
SET ai_paused = $1,
    actions_paused = $2,
    frozen = $3,
    ai_paused_reason = $4,
    updated_at = NOW(),
    updated_by = $5
WHERE id = 1
RETURNING id, ai_paused, actions_paused, frozen, ai_paused_reason, updated_at, updated_by
`

type UpdateSystemStateParams struct {
	AIPaused       bool
	ActionsPaused  bool
	Frozen         bool
	AIPausedReason string
	UpdatedBy      *int64
}

func scanSystemState(row interface{ Scan(...any) error }, i *SystemState) error {
	return row.Scan(
		&i.ID,
		&i.AIPaused,
		&i.ActionsPaused,
		&i.Frozen,
		&i.AIPausedReason,
		&i.UpdatedAt,
		&i.UpdatedBy,
	)
}

func (q *Queries) GetSystemState(ctx context.Context) (SystemState, error) {
	row := q.db.QueryRow(ctx, getSystemState)
	var i SystemState
	err := scanSystemState(row, &i)
	return i, err
}

func (q *Queries) UpdateSystemState(ctx context.Context, arg UpdateSystemStateParams) (SystemState, error) {
	row := q.db.QueryRow(ctx, updateSystemState, arg.AIPaused, arg.ActionsPaused, arg.Frozen, arg.AIPausedReason, arg.UpdatedBy)
	var i SystemState
	err := scanSystemState(row, &i)
	return i, err
}
