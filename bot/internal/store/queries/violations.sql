-- name: InsertViolation :one
INSERT INTO violations (
    chat_id,
    user_id,
    username,
    rule,
    matched,
    action,
    message_text
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, chat_id, user_id, username, rule, matched, action, message_text, created_at;

-- name: ListViolationsByChat :many
SELECT id, chat_id, user_id, username, rule, matched, action, message_text, created_at
FROM violations
WHERE chat_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: ListViolationsByUser :many
SELECT id, chat_id, user_id, username, rule, matched, action, message_text, created_at
FROM violations
WHERE chat_id = $1
  AND user_id = $2
ORDER BY created_at DESC
LIMIT $3;
