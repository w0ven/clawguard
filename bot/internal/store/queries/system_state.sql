-- name: GetSystemState :one
SELECT id, ai_paused, actions_paused, frozen, ai_paused_reason, ai_budget_locked, ai_budget_locked_date, updated_at, updated_by
FROM system_state
WHERE id = 1;

-- name: UpdateSystemState :one
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
RETURNING id, ai_paused, actions_paused, frozen, ai_paused_reason, ai_budget_locked, ai_budget_locked_date, updated_at, updated_by;
