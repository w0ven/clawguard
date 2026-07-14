-- name: UpsertPendingVerification :one
INSERT INTO pending_verifications (
    chat_id,
    user_id,
    username,
    first_name,
    method,
    payload,
    join_message_id,
    expires_at,
    next_attempt_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
ON CONFLICT (chat_id, user_id) DO UPDATE
SET username = EXCLUDED.username,
    first_name = EXCLUDED.first_name,
    method = EXCLUDED.method,
    payload = EXCLUDED.payload,
    join_message_id = EXCLUDED.join_message_id,
    expires_at = EXCLUDED.expires_at,
    next_attempt_at = EXCLUDED.expires_at,
    lease_until = NULL,
    lease_owner = NULL,
    attempt_count = 0,
    last_error = NULL
RETURNING id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error;

-- name: GetPendingVerification :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error
FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2;


-- name: GetActivePendingVerification :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error
FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2 AND expires_at > NOW();

-- name: DeletePendingVerification :execrows
DELETE FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2;

-- name: GetPendingVerificationByToken :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error
FROM pending_verifications
WHERE method = 'turnstile' AND payload->>'token' = $1;

-- name: DeletePendingVerificationByToken :one
DELETE FROM pending_verifications
WHERE method = 'turnstile' AND payload->>'token' = $1 AND expires_at > NOW()
RETURNING id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error;

-- name: ClaimExpiredPendingVerifications :many
WITH ranked AS MATERIALIZED (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY chat_id
               ORDER BY next_attempt_at ASC, expires_at ASC, id ASC
           ) AS chat_rank
    FROM pending_verifications
    WHERE expires_at < NOW()
      AND next_attempt_at <= NOW()
      AND (lease_until IS NULL OR lease_until < NOW())
), candidates AS (
    SELECT pending.id
    FROM pending_verifications AS pending
    INNER JOIN ranked ON ranked.id = pending.id
    ORDER BY ranked.chat_rank ASC, pending.next_attempt_at ASC, pending.expires_at ASC, pending.id ASC
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE OF pending SKIP LOCKED
)
UPDATE pending_verifications AS pending
SET lease_until = NOW() + make_interval(secs => sqlc.arg(lease_seconds)::int),
    lease_owner = sqlc.arg(lease_owner),
    attempt_count = pending.attempt_count + 1
FROM candidates
WHERE pending.id = candidates.id
RETURNING pending.id, pending.chat_id, pending.user_id, pending.username, pending.first_name,
    pending.method, pending.payload, pending.join_message_id, pending.expires_at, pending.created_at,
    pending.next_attempt_at, pending.lease_until, pending.lease_owner, pending.attempt_count, pending.last_error;

-- name: IsPendingVerificationClaimCurrent :one
SELECT EXISTS (
    SELECT 1
    FROM pending_verifications
    WHERE id = $1
      AND lease_owner = $2
      AND lease_until > NOW()
);

-- name: RescheduleClaimedPendingVerification :execrows
UPDATE pending_verifications
SET next_attempt_at = $3,
    lease_until = NULL,
    lease_owner = NULL,
    last_error = $4
WHERE id = $1 AND lease_owner = $2;

-- name: DeleteClaimedPendingVerification :execrows
DELETE FROM pending_verifications
WHERE id = $1 AND lease_owner = $2;

-- name: ListAllPending :many
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error
FROM pending_verifications
ORDER BY created_at DESC;

-- name: CountActivePendingVerificationsByChat :one
SELECT COUNT(*)
FROM pending_verifications
WHERE chat_id = $1 AND expires_at > NOW();
