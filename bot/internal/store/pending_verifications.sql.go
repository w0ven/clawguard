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
    expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (chat_id, user_id) DO UPDATE
SET username = EXCLUDED.username,
    first_name = EXCLUDED.first_name,
    method = EXCLUDED.method,
    payload = EXCLUDED.payload,
    join_message_id = EXCLUDED.join_message_id,
    expires_at = EXCLUDED.expires_at
RETURNING id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
`

const getPendingVerification = `-- name: GetPendingVerification :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2
`

const getActivePendingVerification = `-- name: GetActivePendingVerification :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2 AND expires_at > NOW()
`

const deletePendingVerification = `-- name: DeletePendingVerification :exec
DELETE FROM pending_verifications
WHERE chat_id = $1 AND user_id = $2
`

const getPendingVerificationByToken = `-- name: GetPendingVerificationByToken :one
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
FROM pending_verifications
WHERE method = 'turnstile' AND payload->>'token' = $1
`

const deletePendingVerificationByToken = `-- name: DeletePendingVerificationByToken :one
DELETE FROM pending_verifications
WHERE method = 'turnstile' AND payload->>'token' = $1 AND expires_at > NOW()
RETURNING id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
`

const getExpiredPendingVerifications = `-- name: GetExpiredPendingVerifications :many
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
FROM pending_verifications
WHERE expires_at < NOW()
ORDER BY expires_at ASC
LIMIT $1
`

const listAllPending = `-- name: ListAllPending :many
SELECT id, chat_id, user_id, username, first_name, method, payload, join_message_id, expires_at, created_at
FROM pending_verifications
ORDER BY created_at DESC
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

func (q *Queries) DeletePendingVerification(ctx context.Context, arg DeletePendingVerificationParams) error {
	_, err := q.db.Exec(ctx, deletePendingVerification, arg.ChatID, arg.UserID)
	return err
}

func (q *Queries) GetPendingVerificationByToken(ctx context.Context, token string) (PendingVerification, error) {
	row := q.db.QueryRow(ctx, getPendingVerificationByToken, token)
	return scanPendingVerification(row)
}

func (q *Queries) DeletePendingVerificationByToken(ctx context.Context, token string) (PendingVerification, error) {
	row := q.db.QueryRow(ctx, deletePendingVerificationByToken, token)
	return scanPendingVerification(row)
}

func (q *Queries) GetExpiredPendingVerifications(ctx context.Context, limit int32) ([]PendingVerification, error) {
	rows, err := q.db.Query(ctx, getExpiredPendingVerifications, limit)
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
