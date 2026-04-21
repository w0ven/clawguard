package store

import "context"

const insertWarning = `-- name: InsertWarning :one
INSERT INTO warnings (
    chat_id,
    user_id,
    reason,
    issued_by
) VALUES ($1, $2, $3, $4)
RETURNING id, chat_id, user_id, reason, issued_by, created_at, consumed_at
`

const getActiveWarnings = `-- name: GetActiveWarnings :many
SELECT id, chat_id, user_id, reason, issued_by, created_at, consumed_at
FROM warnings
WHERE chat_id = $1
  AND user_id = $2
  AND consumed_at IS NULL
  AND (
    $3::BIGINT <= 0
    OR created_at > NOW() - ($3::BIGINT * INTERVAL '1 second')
  )
ORDER BY created_at DESC
`

const markWarningsConsumed = `-- name: MarkWarningsConsumed :exec
UPDATE warnings
SET consumed_at = NOW()
WHERE chat_id = $1
  AND user_id = $2
  AND consumed_at IS NULL
  AND (
    $3::BIGINT <= 0
    OR created_at > NOW() - ($3::BIGINT * INTERVAL '1 second')
  )
`

const listWarningChatsByUser = `-- name: ListWarningChatsByUser :many
SELECT DISTINCT chat_id
FROM warnings
WHERE user_id = $1
ORDER BY chat_id
`

const clearWarnings = `-- name: ClearWarnings :execrows
UPDATE warnings
SET consumed_at = NOW()
WHERE chat_id = $1
  AND user_id = $2
  AND consumed_at IS NULL
`

type InsertWarningParams struct {
	ChatID   int64
	UserID   int64
	Reason   *string
	IssuedBy *int64
}

type GetActiveWarningsParams struct {
	ChatID       int64
	UserID       int64
	DecaySeconds int64
}

type MarkWarningsConsumedParams struct {
	ChatID       int64
	UserID       int64
	DecaySeconds int64
}

func (q *Queries) InsertWarning(ctx context.Context, arg InsertWarningParams) (Warning, error) {
	row := q.db.QueryRow(ctx, insertWarning, arg.ChatID, arg.UserID, arg.Reason, arg.IssuedBy)
	var i Warning
	err := row.Scan(
		&i.ID,
		&i.ChatID,
		&i.UserID,
		&i.Reason,
		&i.IssuedBy,
		&i.CreatedAt,
		&i.ConsumedAt,
	)
	return i, err
}

func (q *Queries) GetActiveWarnings(ctx context.Context, arg GetActiveWarningsParams) ([]Warning, error) {
	rows, err := q.db.Query(ctx, getActiveWarnings, arg.ChatID, arg.UserID, arg.DecaySeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Warning
	for rows.Next() {
		var i Warning
		if err := rows.Scan(
			&i.ID,
			&i.ChatID,
			&i.UserID,
			&i.Reason,
			&i.IssuedBy,
			&i.CreatedAt,
			&i.ConsumedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) MarkWarningsConsumed(ctx context.Context, arg MarkWarningsConsumedParams) error {
	_, err := q.db.Exec(ctx, markWarningsConsumed, arg.ChatID, arg.UserID, arg.DecaySeconds)
	return err
}

func (q *Queries) ListWarningChatsByUser(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := q.db.Query(ctx, listWarningChatsByUser, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []int64
	for rows.Next() {
		var chatID int64
		if err := rows.Scan(&chatID); err != nil {
			return nil, err
		}
		items = append(items, chatID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) ClearWarnings(ctx context.Context, chatID int64, userID int64) (int64, error) {
	result, err := q.db.Exec(ctx, clearWarnings, chatID, userID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}
