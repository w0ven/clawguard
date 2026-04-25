-- name: UpsertPendingVerification :one
INSERT INTO pending_verifications (
    chat_id,
    user_id,
    username,
    first_name,
    method,
    payload,
    join_message_id,
    expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (chat_id, user_id) DO UPDATE
SET username = EXCLUDED.username,
    first_name = EXCLUDED.first_name,
    method = EXCLUDED.method,
    payload = EXCLUDED.payload,
    join_message_id = EXCLUDED.join_message_id,
    expires_at = EXCLUDED.expires_at
RETURNING id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at;

-- name: GetPendingVerification :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2;


-- name: GetActivePendingVerification :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2 AND expires_at > NOW();

-- name: DeletePendingVerification :exec
DELETE FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2;

-- name: GetPendingVerificationByToken :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
FROM pending_verifications
WHERE method = 'turnstile' AND payload->>'token' = $1;

-- name: DeletePendingVerificationByToken :one
DELETE FROM pending_verifications
WHERE method = 'turnstile' AND payload->>'token' = $1 AND expires_at > NOW()
RETURNING id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at;

-- name: GetExpiredPendingVerifications :many
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
FROM pending_verifications
WHERE expires_at < NOW()
ORDER BY expires_at ASC
LIMIT $1;

-- name: ListAllPending :many
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
FROM pending_verifications
ORDER BY created_at DESC;
