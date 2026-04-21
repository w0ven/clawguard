-- name: InsertWarning :one
INSERT INTO warnings (
    chat_id,
    user_id,
    reason,
    issued_by
) VALUES ($1, $2, $3, $4)
RETURNING id, chat_id, user_id, reason, issued_by, created_at, consumed_at;

-- name: GetActiveWarnings :many
SELECT id, chat_id, user_id, reason, issued_by, created_at, consumed_at
FROM warnings
WHERE chat_id = $1
  AND user_id = $2
  AND consumed_at IS NULL
  AND (
    $3::BIGINT <= 0
    OR created_at > NOW() - ($3::BIGINT * INTERVAL '1 second')
  )
ORDER BY created_at DESC;

-- name: MarkWarningsConsumed :exec
UPDATE warnings
SET consumed_at = NOW()
WHERE chat_id = $1
  AND user_id = $2
  AND consumed_at IS NULL
  AND (
    $3::BIGINT <= 0
    OR created_at > NOW() - ($3::BIGINT * INTERVAL '1 second')
  );

-- name: ClearWarnings :execrows
UPDATE warnings
SET consumed_at = NOW()
WHERE chat_id = $1
  AND user_id = $2
  AND consumed_at IS NULL;

-- name: ListWarningChatsByUser :many
SELECT DISTINCT chat_id
FROM warnings
WHERE user_id = $1
ORDER BY chat_id;
