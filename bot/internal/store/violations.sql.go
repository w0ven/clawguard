package store

import (
	"context"
)

const insertViolation = `-- name: InsertViolation :one
INSERT INTO violations (
    chat_id,
    user_id,
    username,
    rule,
    matched,
    action,
    message_text
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, chat_id, user_id, username, rule, matched, action, message_text, created_at
`

const listViolationsByChat = `-- name: ListViolationsByChat :many
SELECT id, chat_id, user_id, username, rule, matched, action, message_text, created_at
FROM violations
WHERE chat_id = $1
ORDER BY created_at DESC
LIMIT $2
`

const listViolationsByUser = `-- name: ListViolationsByUser :many
SELECT id, chat_id, user_id, username, rule, matched, action, message_text, created_at
FROM violations
WHERE chat_id = $1
  AND user_id = $2
ORDER BY created_at DESC
LIMIT $3
`

type InsertViolationParams struct {
	ChatID      int64
	UserID      int64
	Username    *string
	Rule        string
	Matched     *string
	Action      string
	MessageText *string
}

type ListViolationsByChatParams struct {
	ChatID int64
	Limit  int32
}

type ListViolationsByUserParams struct {
	ChatID int64
	UserID int64
	Limit  int32
}

func (q *Queries) InsertViolation(ctx context.Context, arg InsertViolationParams) (Violation, error) {
	row := q.db.QueryRow(
		ctx,
		insertViolation,
		arg.ChatID,
		arg.UserID,
		arg.Username,
		arg.Rule,
		arg.Matched,
		arg.Action,
		arg.MessageText,
	)
	var i Violation
	err := row.Scan(
		&i.ID,
		&i.ChatID,
		&i.UserID,
		&i.Username,
		&i.Rule,
		&i.Matched,
		&i.Action,
		&i.MessageText,
		&i.CreatedAt,
	)
	return i, err
}

func (q *Queries) ListViolationsByChat(ctx context.Context, arg ListViolationsByChatParams) ([]Violation, error) {
	rows, err := q.db.Query(ctx, listViolationsByChat, arg.ChatID, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Violation
	for rows.Next() {
		var i Violation
		if err := rows.Scan(
			&i.ID,
			&i.ChatID,
			&i.UserID,
			&i.Username,
			&i.Rule,
			&i.Matched,
			&i.Action,
			&i.MessageText,
			&i.CreatedAt,
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

func (q *Queries) ListViolationsByUser(ctx context.Context, arg ListViolationsByUserParams) ([]Violation, error) {
	rows, err := q.db.Query(ctx, listViolationsByUser, arg.ChatID, arg.UserID, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Violation
	for rows.Next() {
		var i Violation
		if err := rows.Scan(
			&i.ID,
			&i.ChatID,
			&i.UserID,
			&i.Username,
			&i.Rule,
			&i.Matched,
			&i.Action,
			&i.MessageText,
			&i.CreatedAt,
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
