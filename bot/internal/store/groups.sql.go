package store

import (
	"context"
)

const upsertGroup = `-- name: UpsertGroup :one
INSERT INTO groups (
    chat_id,
    title,
    type,
    member_count
) VALUES ($1, $2, $3, $4)
ON CONFLICT (chat_id) DO UPDATE
SET title = EXCLUDED.title,
    type = EXCLUDED.type,
    member_count = EXCLUDED.member_count
RETURNING id, chat_id, title, type, member_count, enabled, joined_at, config
`

const getGroupByChatID = `-- name: GetGroupByChatID :one
SELECT id, chat_id, title, type, member_count, enabled, joined_at, config
FROM groups
WHERE chat_id = $1
`

const listGroups = `-- name: ListGroups :many
SELECT id, chat_id, title, type, member_count, enabled, joined_at, config
FROM groups
ORDER BY title ASC, chat_id ASC
`

const listGroupsScoped = `-- name: ListGroupsScoped :many
SELECT id, chat_id, title, type, member_count, enabled, joined_at, config
FROM groups
WHERE ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]))
ORDER BY title ASC, chat_id ASC
`

const updateGroupConfig = `-- name: UpdateGroupConfig :one
UPDATE groups
SET config = $2
WHERE chat_id = $1
RETURNING id, chat_id, title, type, member_count, enabled, joined_at, config
`

type UpsertGroupParams struct {
	ChatID      int64
	Title       string
	Type        string
	MemberCount int32
}

func (q *Queries) UpsertGroup(ctx context.Context, arg UpsertGroupParams) (Group, error) {
	row := q.db.QueryRow(ctx, upsertGroup, arg.ChatID, arg.Title, arg.Type, arg.MemberCount)
	var i Group
	err := row.Scan(
		&i.ID,
		&i.ChatID,
		&i.Title,
		&i.Type,
		&i.MemberCount,
		&i.Enabled,
		&i.JoinedAt,
		&i.Config,
	)
	return i, err
}

func (q *Queries) GetGroupByChatID(ctx context.Context, chatID int64) (Group, error) {
	row := q.db.QueryRow(ctx, getGroupByChatID, chatID)
	var i Group
	err := row.Scan(
		&i.ID,
		&i.ChatID,
		&i.Title,
		&i.Type,
		&i.MemberCount,
		&i.Enabled,
		&i.JoinedAt,
		&i.Config,
	)
	return i, err
}

type UpdateGroupConfigParams struct {
	ChatID int64
	Config []byte
}

func (q *Queries) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := q.db.Query(ctx, listGroups)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Group
	for rows.Next() {
		var i Group
		if err := rows.Scan(
			&i.ID,
			&i.ChatID,
			&i.Title,
			&i.Type,
			&i.MemberCount,
			&i.Enabled,
			&i.JoinedAt,
			&i.Config,
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

func (q *Queries) ListGroupsScoped(ctx context.Context, chatIds []int64) ([]Group, error) {
	rows, err := q.db.Query(ctx, listGroupsScoped, chatIds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Group
	for rows.Next() {
		var i Group
		if err := rows.Scan(
			&i.ID,
			&i.ChatID,
			&i.Title,
			&i.Type,
			&i.MemberCount,
			&i.Enabled,
			&i.JoinedAt,
			&i.Config,
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

func (q *Queries) UpdateGroupConfig(ctx context.Context, arg UpdateGroupConfigParams) (Group, error) {
	row := q.db.QueryRow(ctx, updateGroupConfig, arg.ChatID, arg.Config)
	var i Group
	err := row.Scan(
		&i.ID,
		&i.ChatID,
		&i.Title,
		&i.Type,
		&i.MemberCount,
		&i.Enabled,
		&i.JoinedAt,
		&i.Config,
	)
	return i, err
}
