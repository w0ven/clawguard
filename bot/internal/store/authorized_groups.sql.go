package store

import "context"

const listAuthorizedGroups = `-- name: ListAuthorizedGroups :many
SELECT chat_id, title, authorized_at, authorized_by, enabled, notes
FROM authorized_groups
ORDER BY authorized_at DESC, chat_id DESC
`

const getAuthorizedGroupByChatID = `-- name: GetAuthorizedGroupByChatID :one
SELECT chat_id, title, authorized_at, authorized_by, enabled, notes
FROM authorized_groups
WHERE chat_id = $1
`

const upsertAuthorizedGroup = `-- name: UpsertAuthorizedGroup :one
INSERT INTO authorized_groups (
    chat_id,
    title,
    authorized_by,
    enabled,
    notes
) VALUES ($1, COALESCE($2, ''), $3, COALESCE($4, TRUE), COALESCE($5, ''))
ON CONFLICT (chat_id) DO UPDATE
SET title = COALESCE(NULLIF(EXCLUDED.title, ''), authorized_groups.title),
    authorized_by = EXCLUDED.authorized_by,
    enabled = EXCLUDED.enabled,
    notes = EXCLUDED.notes
RETURNING chat_id, title, authorized_at, authorized_by, enabled, notes
`

const deleteAuthorizedGroup = `-- name: DeleteAuthorizedGroup :exec
DELETE FROM authorized_groups
WHERE chat_id = $1
`

const setAuthorizedGroupEnabled = `-- name: SetAuthorizedGroupEnabled :one
UPDATE authorized_groups
SET enabled = $2
WHERE chat_id = $1
RETURNING chat_id, title, authorized_at, authorized_by, enabled, notes
`

type UpsertAuthorizedGroupParams struct {
	ChatID       int64
	Title        *string
	AuthorizedBy *int64
	Enabled      *bool
	Notes        *string
}

type SetAuthorizedGroupEnabledParams struct {
	ChatID  int64
	Enabled bool
}

func scanAuthorizedGroup(row interface{ Scan(...any) error }, i *AuthorizedGroup) error {
	return row.Scan(
		&i.ChatID,
		&i.Title,
		&i.AuthorizedAt,
		&i.AuthorizedBy,
		&i.Enabled,
		&i.Notes,
	)
}

func (q *Queries) ListAuthorizedGroups(ctx context.Context) ([]AuthorizedGroup, error) {
	rows, err := q.db.Query(ctx, listAuthorizedGroups)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]AuthorizedGroup, 0)
	for rows.Next() {
		var i AuthorizedGroup
		if err := scanAuthorizedGroup(rows, &i); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) GetAuthorizedGroupByChatID(ctx context.Context, chatID int64) (AuthorizedGroup, error) {
	row := q.db.QueryRow(ctx, getAuthorizedGroupByChatID, chatID)
	var i AuthorizedGroup
	err := scanAuthorizedGroup(row, &i)
	return i, err
}

func (q *Queries) UpsertAuthorizedGroup(ctx context.Context, arg UpsertAuthorizedGroupParams) (AuthorizedGroup, error) {
	row := q.db.QueryRow(ctx, upsertAuthorizedGroup, arg.ChatID, arg.Title, arg.AuthorizedBy, arg.Enabled, arg.Notes)
	var i AuthorizedGroup
	err := scanAuthorizedGroup(row, &i)
	return i, err
}

func (q *Queries) DeleteAuthorizedGroup(ctx context.Context, chatID int64) error {
	_, err := q.db.Exec(ctx, deleteAuthorizedGroup, chatID)
	return err
}

func (q *Queries) SetAuthorizedGroupEnabled(ctx context.Context, arg SetAuthorizedGroupEnabledParams) (AuthorizedGroup, error) {
	row := q.db.QueryRow(ctx, setAuthorizedGroupEnabled, arg.ChatID, arg.Enabled)
	var i AuthorizedGroup
	err := scanAuthorizedGroup(row, &i)
	return i, err
}
