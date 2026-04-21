-- name: InsertAuditEntry :one
INSERT INTO config_audit (
    scope,
    chat_id,
    admin_id,
    action,
    before,
    after,
    diff
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, scope, chat_id, admin_id, action, before, after, diff, created_at;

-- name: ListAuditByChat :many
SELECT id, scope, chat_id, admin_id, action, before, after, diff, created_at
FROM config_audit
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
ORDER BY created_at DESC
LIMIT $2;

-- name: ListAuditByAdmin :many
SELECT id, scope, chat_id, admin_id, action, before, after, diff, created_at
FROM config_audit
WHERE admin_id = $1
ORDER BY created_at DESC
LIMIT $2;
