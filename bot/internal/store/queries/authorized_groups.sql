-- name: ListAuthorizedGroups :many
SELECT chat_id, title, authorized_at, authorized_by, enabled, notes
FROM authorized_groups
ORDER BY authorized_at DESC, chat_id DESC;

-- name: GetAuthorizedGroupByChatID :one
SELECT chat_id, title, authorized_at, authorized_by, enabled, notes
FROM authorized_groups
WHERE chat_id = $1;

-- name: UpsertAuthorizedGroup :one
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
RETURNING chat_id, title, authorized_at, authorized_by, enabled, notes;

-- name: DeleteAuthorizedGroup :exec
DELETE FROM authorized_groups
WHERE chat_id = $1;

-- name: SetAuthorizedGroupEnabled :one
UPDATE authorized_groups
SET enabled = $2
WHERE chat_id = $1
RETURNING chat_id, title, authorized_at, authorized_by, enabled, notes;
