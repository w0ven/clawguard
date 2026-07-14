package store

import (
	"context"
	"time"
)

const upsertPendingVerification = `-- name: UpsertPendingVerification :one
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
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error
`

const getPendingVerification = `-- name: GetPendingVerification :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error
FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2
`

const getActivePendingVerification = `-- name: GetActivePendingVerification :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error
FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2 AND expires_at > NOW()
`

const deletePendingVerification = `-- name: DeletePendingVerification :execrows
DELETE FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2
`

const getPendingVerificationByToken = `-- name: GetPendingVerificationByToken :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error
FROM pending_verifications
WHERE method = 'turnstile' AND payload->>'token' = $1
`

const deletePendingVerificationByToken = `-- name: DeletePendingVerificationByToken :one
DELETE FROM pending_verifications
WHERE method = 'turnstile' AND payload->>'token' = $1 AND expires_at > NOW()
RETURNING id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error
`

const claimExpiredPendingVerifications = `-- name: ClaimExpiredPendingVerifications :many
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
    LIMIT $3
    FOR UPDATE OF pending SKIP LOCKED
)
UPDATE pending_verifications AS pending
SET lease_until = NOW() + make_interval(secs => $1::int),
    lease_owner = $2,
    attempt_count = pending.attempt_count + 1
FROM candidates
WHERE pending.id = candidates.id
RETURNING pending.id, pending.chat_id, pending.user_id, pending.username, pending.first_name,
    pending.method, pending.payload, pending.join_message_id, pending.expires_at, pending.created_at,
    pending.next_attempt_at, pending.lease_until, pending.lease_owner, pending.attempt_count, pending.last_error
`

const listAllPending = `-- name: ListAllPending :many
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at,
    next_attempt_at, lease_until, lease_owner, attempt_count, last_error
FROM pending_verifications
ORDER BY created_at DESC
`

const countActivePendingVerificationsByChat = `-- name: CountActivePendingVerificationsByChat :one
SELECT COUNT(*)
FROM pending_verifications
WHERE chat_id = $1 AND expires_at > NOW()
`

type UpsertPendingVerificationParams struct {
	ChatID        int64
	UserID        int64
	Username      *string
	FirstName     *string
	Method        string
	Payload       []byte
	JoinMessageID *int64
	ExpiresAt     time.Time
}

type GetPendingVerificationParams struct {
	ChatID int64
	UserID int64
}

type DeletePendingVerificationParams struct {
	ChatID int64
	UserID int64
}

type ClaimExpiredPendingVerificationsParams struct {
	LeaseSeconds int32
	LeaseOwner   string
	BatchLimit   int32
}

type DeleteClaimedPendingVerificationParams struct {
	ID         int64
	LeaseOwner string
}

type IsPendingVerificationClaimCurrentParams struct {
	ID         int64
	LeaseOwner string
}

type RescheduleClaimedPendingVerificationParams struct {
	ID            int64
	LeaseOwner    string
	NextAttemptAt time.Time
	LastError     *string
}

func scanPendingVerification(row interface {
	Scan(dest ...any) error
}) (PendingVerification, error) {
	var i PendingVerification
	err := row.Scan(
		&i.ID,
		&i.ChatID,
		&i.UserID,
		&i.Username,
		&i.FirstName,
		&i.Method,
		&i.Payload,
		&i.JoinMessageID,
		&i.ExpiresAt,
		&i.CreatedAt,
		&i.NextAttemptAt,
		&i.LeaseUntil,
		&i.LeaseOwner,
		&i.AttemptCount,
		&i.LastError,
	)
	return i, err
}

func (q *Queries) UpsertPendingVerification(ctx context.Context, arg UpsertPendingVerificationParams) (PendingVerification, error) {
	row := q.db.QueryRow(
		ctx,
		upsertPendingVerification,
		arg.ChatID,
		arg.UserID,
		arg.Username,
		arg.FirstName,
		arg.Method,
		arg.Payload,
		arg.JoinMessageID,
		arg.ExpiresAt,
	)
	return scanPendingVerification(row)
}

func (q *Queries) GetPendingVerification(ctx context.Context, arg GetPendingVerificationParams) (PendingVerification, error) {
	row := q.db.QueryRow(ctx, getPendingVerification, arg.ChatID, arg.UserID)
	return scanPendingVerification(row)
}

func (q *Queries) GetActivePendingVerification(ctx context.Context, arg GetPendingVerificationParams) (PendingVerification, error) {
	row := q.db.QueryRow(ctx, getActivePendingVerification, arg.ChatID, arg.UserID)
	return scanPendingVerification(row)
}

func (q *Queries) CountActivePendingVerificationsByChat(ctx context.Context, chatID int64) (int64, error) {
	row := q.db.QueryRow(ctx, countActivePendingVerificationsByChat, chatID)
	var count int64
	err := row.Scan(&count)
	return count, err
}

func (q *Queries) DeletePendingVerification(ctx context.Context, arg DeletePendingVerificationParams) (int64, error) {
	result, err := q.db.Exec(ctx, deletePendingVerification, arg.ChatID, arg.UserID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (q *Queries) GetPendingVerificationByToken(ctx context.Context, token string) (PendingVerification, error) {
	row := q.db.QueryRow(ctx, getPendingVerificationByToken, token)
	return scanPendingVerification(row)
}

func (q *Queries) DeletePendingVerificationByToken(ctx context.Context, token string) (PendingVerification, error) {
	row := q.db.QueryRow(ctx, deletePendingVerificationByToken, token)
	return scanPendingVerification(row)
}

func (q *Queries) ClaimExpiredPendingVerifications(ctx context.Context, arg ClaimExpiredPendingVerificationsParams) ([]PendingVerification, error) {
	rows, err := q.db.Query(ctx, claimExpiredPendingVerifications, arg.LeaseSeconds, arg.LeaseOwner, arg.BatchLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []PendingVerification
	for rows.Next() {
		item, err := scanPendingVerification(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const deleteClaimedPendingVerification = `-- name: DeleteClaimedPendingVerification :execrows
DELETE FROM pending_verifications
WHERE id = $1 AND lease_owner = $2
`

func (q *Queries) DeleteClaimedPendingVerification(ctx context.Context, arg DeleteClaimedPendingVerificationParams) (int64, error) {
	result, err := q.db.Exec(ctx, deleteClaimedPendingVerification, arg.ID, arg.LeaseOwner)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

const isPendingVerificationClaimCurrent = `-- name: IsPendingVerificationClaimCurrent :one
SELECT EXISTS (
    SELECT 1
    FROM pending_verifications
    WHERE id = $1
      AND lease_owner = $2
      AND lease_until > NOW()
)
`

func (q *Queries) IsPendingVerificationClaimCurrent(ctx context.Context, arg IsPendingVerificationClaimCurrentParams) (bool, error) {
	row := q.db.QueryRow(ctx, isPendingVerificationClaimCurrent, arg.ID, arg.LeaseOwner)
	var exists bool
	err := row.Scan(&exists)
	return exists, err
}

const rescheduleClaimedPendingVerification = `-- name: RescheduleClaimedPendingVerification :execrows
UPDATE pending_verifications
SET next_attempt_at = $3,
    lease_until = NULL,
    lease_owner = NULL,
    last_error = $4
WHERE id = $1 AND lease_owner = $2
`

func (q *Queries) RescheduleClaimedPendingVerification(ctx context.Context, arg RescheduleClaimedPendingVerificationParams) (int64, error) {
	result, err := q.db.Exec(ctx, rescheduleClaimedPendingVerification, arg.ID, arg.LeaseOwner, arg.NextAttemptAt, arg.LastError)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (q *Queries) ListAllPending(ctx context.Context) ([]PendingVerification, error) {
	rows, err := q.db.Query(ctx, listAllPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []PendingVerification
	for rows.Next() {
		item, err := scanPendingVerification(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
