package store

import "context"

const insertAuditEntry = `-- name: InsertAuditEntry :one
INSERT INTO config_audit (
    scope,
    chat_id,
    admin_id,
    action,
    before,
    after,
    diff
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, scope, chat_id, admin_id, action, before, after, diff, created_at
`

const listAuditByChat = `-- name: ListAuditByChat :many
SELECT id, scope, chat_id, admin_id, action, before, after, diff, created_at
FROM config_audit
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
ORDER BY created_at DESC
LIMIT $2
`

const listAuditByAdmin = `-- name: ListAuditByAdmin :many
SELECT id, scope, chat_id, admin_id, action, before, after, diff, created_at
FROM config_audit
WHERE admin_id = $1
ORDER BY created_at DESC
LIMIT $2
`

type InsertAuditEntryParams struct {
	Scope   string
	ChatID  *int64
	AdminID int64
	Action  string
	Before  []byte
	After   []byte
	Diff    *string
}

type ListAuditByChatParams struct {
	ChatID *int64
	Limit  int32
}

type ListAuditByAdminParams struct {
	AdminID int64
	Limit   int32
}

func (q *Queries) InsertAuditEntry(ctx context.Context, arg InsertAuditEntryParams) (ConfigAudit, error) {
	row := q.db.QueryRow(ctx, insertAuditEntry, arg.Scope, arg.ChatID, arg.AdminID, arg.Action, arg.Before, arg.After, arg.Diff)
	var i ConfigAudit
	err := row.Scan(
		&i.ID,
		&i.Scope,
		&i.ChatID,
		&i.AdminID,
		&i.Action,
		&i.Before,
		&i.After,
		&i.Diff,
		&i.CreatedAt,
	)
	return i, err
}

func (q *Queries) ListAuditByChat(ctx context.Context, arg ListAuditByChatParams) ([]ConfigAudit, error) {
	rows, err := q.db.Query(ctx, listAuditByChat, arg.ChatID, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ConfigAudit
	for rows.Next() {
		var i ConfigAudit
		if err := rows.Scan(
			&i.ID,
			&i.Scope,
			&i.ChatID,
			&i.AdminID,
			&i.Action,
			&i.Before,
			&i.After,
			&i.Diff,
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

func (q *Queries) ListAuditByAdmin(ctx context.Context, arg ListAuditByAdminParams) ([]ConfigAudit, error) {
	rows, err := q.db.Query(ctx, listAuditByAdmin, arg.AdminID, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ConfigAudit
	for rows.Next() {
		var i ConfigAudit
		if err := rows.Scan(
			&i.ID,
			&i.Scope,
			&i.ChatID,
			&i.AdminID,
			&i.Action,
			&i.Before,
			&i.After,
			&i.Diff,
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
