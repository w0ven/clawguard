package store

import (
	"context"
	"time"
)

const getSystemState = `-- name: GetSystemState :one
SELECT id, ai_paused, actions_paused, frozen, ai_paused_reason, ai_budget_locked, ai_budget_locked_date, updated_at, updated_by
FROM system_state
WHERE id = 1
`

const updateSystemState = `-- name: UpdateSystemState :one
UPDATE system_state
SET ai_paused = $1,
    actions_paused = $2,
    frozen = $3,
    ai_paused_reason = $4,
    ai_budget_locked = $5,
    ai_budget_locked_date = $6,
    updated_at = NOW(),
    updated_by = $7
WHERE id = 1
RETURNING id, ai_paused, actions_paused, frozen, ai_paused_reason, ai_budget_locked, ai_budget_locked_date, updated_at, updated_by
`

type UpdateSystemStateParams struct {
	AIPaused           bool
	ActionsPaused      bool
	Frozen             bool
	AIPausedReason     string
	AIBudgetLocked     bool
	AIBudgetLockedDate *time.Time
	UpdatedBy          *int64
}

func scanSystemState(row interface{ Scan(...any) error }, i *SystemState) error {
	return row.Scan(
		&i.ID,
		&i.AIPaused,
		&i.ActionsPaused,
		&i.Frozen,
		&i.AIPausedReason,
		&i.AIBudgetLocked,
		&i.AIBudgetLockedDate,
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
	row := q.db.QueryRow(ctx, updateSystemState, arg.AIPaused, arg.ActionsPaused, arg.Frozen, arg.AIPausedReason, arg.AIBudgetLocked, arg.AIBudgetLockedDate, arg.UpdatedBy)
	var i SystemState
	err := scanSystemState(row, &i)
	return i, err
}
