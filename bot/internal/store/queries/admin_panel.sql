-- name: ListViolations :many
SELECT id, chat_id, user_id, username, rule, matched, action, message_text, created_at
FROM violations
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
  AND ($2::BIGINT IS NULL OR user_id = $2)
  AND ($3::TEXT = '' OR rule = $3)
  AND ($4::TEXT = '' OR action = $4)
  AND ($5::TIMESTAMPTZ IS NULL OR created_at >= $5)
  AND ($6::TIMESTAMPTZ IS NULL OR created_at <= $6)
ORDER BY created_at DESC
LIMIT $7;

-- name: ListWarnings :many
SELECT id, chat_id, user_id, reason, issued_by, created_at, consumed_at
FROM warnings
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
  AND ($2::BIGINT IS NULL OR user_id = $2)
ORDER BY created_at DESC
LIMIT $3;

-- name: UpsertBannedUser :one
INSERT INTO banned_users (
    user_id,
    reason,
    source,
    banned_by
) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE
SET reason = EXCLUDED.reason,
    source = EXCLUDED.source,
    banned_by = EXCLUDED.banned_by,
    banned_at = NOW()
RETURNING user_id, reason, source, banned_at, banned_by;

-- name: DeleteBannedUser :exec
DELETE FROM banned_users
WHERE user_id = $1;

-- name: GetAdminStats :one
SELECT
    (SELECT COUNT(*)::BIGINT FROM groups) AS groups_count,
    (SELECT COUNT(*)::BIGINT FROM pending_verifications WHERE expires_at > NOW()) AS active_verifications,
    (SELECT COUNT(*)::BIGINT FROM violations WHERE created_at >= date_trunc('day', NOW())) AS today_violations;

-- name: GetTodayAICostCents :one
SELECT COALESCE(SUM(cost_cents), 0)::DOUBLE PRECISION
FROM ai_decisions
WHERE created_at >= CURRENT_DATE;

-- name: ListRecentAIDecisionVerdicts :many
SELECT verdict
FROM ai_decisions
ORDER BY created_at DESC
LIMIT $1;

-- name: GetFirstOwnerAdmin :one
SELECT id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
FROM admins
WHERE LOWER(role) = 'owner'
ORDER BY created_at ASC, id ASC
LIMIT 1;
