-- name: UpsertGroup :one
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
RETURNING id, chat_id, title, type, member_count, enabled, joined_at, config;

-- name: GetGroupByChatID :one
SELECT id, chat_id, title, type, member_count, enabled, joined_at, config
FROM groups
WHERE chat_id = $1;

-- name: ListGroups :many
SELECT id, chat_id, title, type, member_count, enabled, joined_at, config
FROM groups
ORDER BY title ASC, chat_id ASC;

-- name: ListGroupsScoped :many
SELECT id, chat_id, title, type, member_count, enabled, joined_at, config
FROM groups
WHERE ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]))
ORDER BY title ASC, chat_id ASC;

-- name: UpdateGroupConfig :one
UPDATE groups
SET config = $2
WHERE chat_id = $1
RETURNING id, chat_id, title, type, member_count, enabled, joined_at, config;
