-- name: UpsertAdmin :one
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
RETURNING id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at;

-- name: TouchAdminLogin :one
UPDATE admins
SET username = COALESCE($2, username),
    first_name = COALESCE($3, first_name),
    photo_url = COALESCE($4, photo_url),
    last_login_at = NOW()
WHERE telegram_id = $1
RETURNING id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at;

-- name: ListAdmins :many
SELECT id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
FROM admins
ORDER BY created_at DESC, id DESC;

-- name: GetAdminByTelegramID :one
SELECT id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
FROM admins
WHERE telegram_id = $1;

-- name: GetAdminByID :one
SELECT id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at
FROM admins
WHERE id = $1;

-- name: UpdateAdminRole :one
UPDATE admins
SET role = $2
WHERE id = $1
RETURNING id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at;

-- name: UpdateAdminDetails :one
UPDATE admins
SET role = COALESCE(NULLIF($2, ''), role),
    notes = $3,
    group_scope = COALESCE($4, group_scope)
WHERE id = $1
RETURNING id, telegram_id, username, first_name, photo_url, role, notes, group_scope, created_at, last_login_at;

-- name: DeleteAdmin :exec
DELETE FROM admins
WHERE id = $1;
