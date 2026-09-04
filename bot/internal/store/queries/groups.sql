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

-- name: ListManagedGroupsScoped :many
SELECT g.id, g.chat_id, g.title, g.type, g.member_count, g.enabled, g.joined_at, g.config
FROM groups g
INNER JOIN authorized_groups a ON a.chat_id = g.chat_id
WHERE a.enabled = TRUE
  AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR g.chat_id = ANY($1::BIGINT[]))
ORDER BY g.title ASC, g.chat_id ASC;

-- name: DeleteGroupByChatID :exec
DELETE FROM groups
WHERE chat_id = $1;

-- name: UpdateGroupConfig :one
UPDATE groups
SET config = CASE
    WHEN jsonb_typeof(config) = 'object' AND config ? 'join_protection'
        THEN jsonb_set($2::jsonb, '{join_protection}', config->'join_protection', true)
    ELSE $2::jsonb
END
WHERE chat_id = $1
RETURNING id, chat_id, title, type, member_count, enabled, joined_at, config;

-- name: UpdateGroupJoinProtection :one
UPDATE groups
SET config = jsonb_set(
    CASE WHEN jsonb_typeof(config) = 'object' THEN config ELSE '{}'::jsonb END,
    '{join_protection}',
    $2::jsonb,
    true
)
WHERE chat_id = $1
RETURNING id, chat_id, title, type, member_count, enabled, joined_at, config;
