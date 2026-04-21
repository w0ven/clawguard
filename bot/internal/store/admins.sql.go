package store

import (
	"context"
)

const upsertAdmin = `-- name: UpsertAdmin :one
INSERT INTO admins (
    telegram_id,
    username,
    first_name,
    photo_url,
    role,
    notes,
    group_scope
) VALUES (
    $1,
    $2,
    $3,
    $4,
    COALESCE(NULLIF($5, ''), 'admin'),
    $6,
    COALESCE($7, '[]'::jsonb)
)
ON CONFLICT (telegram_id) DO UPDATE
SET username = EXCLUDED.username,
    first_name = EXCLUDED.first_name,
    photo_url = EXCLUDED.photo_url,
    role = COALESCE(NULLIF(EXCLUDED.role, ''), admins.role),
    notes = COALESCE(EXCLUDED.notes, admins.notes),
    group_scope = COALESCE(EXCLUDED.group_scope, admins.group_scope)
RETURNING id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
`

const touchAdminLogin = `-- name: TouchAdminLogin :one
UPDATE admins
SET username = COALESCE($2, username),
    first_name = COALESCE($3, first_name),
    photo_url = COALESCE($4, photo_url),
    last_login_at = NOW()
WHERE telegram_id = $1
RETURNING id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
`

const listAdmins = `-- name: ListAdmins :many
SELECT id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
FROM admins
ORDER BY created_at DESC, id DESC
`

const getAdminByTelegramID = `-- name: GetAdminByTelegramID :one
SELECT id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
FROM admins
WHERE telegram_id = $1
`

const getAdminByID = `-- name: GetAdminByID :one
SELECT id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
FROM admins
WHERE id = $1
`

const updateAdminRole = `-- name: UpdateAdminRole :one
UPDATE admins
SET role = $2
WHERE id = $1
RETURNING id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
`

const updateAdminDetails = `-- name: UpdateAdminDetails :one
UPDATE admins
SET role = COALESCE(NULLIF($2, ''), role),
    notes = $3,
    group_scope = COALESCE($4, group_scope)
WHERE id = $1
RETURNING id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
`

const deleteAdmin = `-- name: DeleteAdmin :exec
DELETE FROM admins
WHERE id = $1
`

type UpsertAdminParams struct {
	TelegramID int64
	Username   *string
	FirstName  *string
	PhotoURL   *string
	Role       string
	Notes      *string
	GroupScope []byte
}

type TouchAdminLoginParams struct {
	TelegramID int64
	Username   *string
	FirstName  *string
	PhotoURL   *string
}

type UpdateAdminRoleParams struct {
	ID   int64
	Role string
}

type UpdateAdminDetailsParams struct {
	ID         int64
	Role       string
	Notes      *string
	GroupScope []byte
}

func scanAdmin(row interface{ Scan(...any) error }, i *Admin) error {
	return row.Scan(
		&i.ID,
		&i.TelegramID,
		&i.Username,
		&i.FirstName,
		&i.PhotoURL,
		&i.Role,
		&i.Notes,
		&i.GroupScope,
		&i.CreatedAt,
		&i.LastLoginAt,
	)
}

func (q *Queries) UpsertAdmin(ctx context.Context, arg UpsertAdminParams) (Admin, error) {
	row := q.db.QueryRow(ctx, upsertAdmin, arg.TelegramID, arg.Username, arg.FirstName, arg.PhotoURL, arg.Role, arg.Notes, arg.GroupScope)
	var i Admin
	err := scanAdmin(row, &i)
	return i, err
}

func (q *Queries) TouchAdminLogin(ctx context.Context, arg TouchAdminLoginParams) (Admin, error) {
	row := q.db.QueryRow(ctx, touchAdminLogin, arg.TelegramID, arg.Username, arg.FirstName, arg.PhotoURL)
	var i Admin
	err := scanAdmin(row, &i)
	return i, err
}

func (q *Queries) ListAdmins(ctx context.Context) ([]Admin, error) {
	rows, err := q.db.Query(ctx, listAdmins)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Admin, 0)
	for rows.Next() {
		var i Admin
		if err := scanAdmin(rows, &i); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) GetAdminByTelegramID(ctx context.Context, telegramID int64) (Admin, error) {
	row := q.db.QueryRow(ctx, getAdminByTelegramID, telegramID)
	var i Admin
	err := scanAdmin(row, &i)
	return i, err
}

func (q *Queries) GetAdminByID(ctx context.Context, id int64) (Admin, error) {
	row := q.db.QueryRow(ctx, getAdminByID, id)
	var i Admin
	err := scanAdmin(row, &i)
	return i, err
}

func (q *Queries) UpdateAdminRole(ctx context.Context, arg UpdateAdminRoleParams) (Admin, error) {
	row := q.db.QueryRow(ctx, updateAdminRole, arg.ID, arg.Role)
	var i Admin
	err := scanAdmin(row, &i)
	return i, err
}

func (q *Queries) UpdateAdminDetails(ctx context.Context, arg UpdateAdminDetailsParams) (Admin, error) {
	row := q.db.QueryRow(ctx, updateAdminDetails, arg.ID, arg.Role, arg.Notes, arg.GroupScope)
	var i Admin
	err := scanAdmin(row, &i)
	return i, err
}

func (q *Queries) DeleteAdmin(ctx context.Context, id int64) error {
	_, err := q.db.Exec(ctx, deleteAdmin, id)
	return err
}
