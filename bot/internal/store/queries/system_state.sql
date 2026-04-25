-- name: GetSystemState :one
SELECT id, ai_paused, actions_paused, frozen, ai_paused_reason, updated_at, updated_by
FROM system_state
WHERE id = 1;

-- name: UpdateSystemState :one
UPDATE system_state
SET ai_paused = $1,
    actions_paused = $2,
    frozen = $3,
    ai_paused_reason = $4,
    updated_at = NOW(),
    updated_by = $5
WHERE id = 1
RETURNING id, ai_paused, actions_paused, frozen, ai_paused_reason, updated_at, updated_by;
