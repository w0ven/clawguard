-- name: ListEventsPaginated :many
SELECT type, id, chat_id, user_id, title, detail, extra, created_at
FROM (
    SELECT
        'violation'::TEXT AS type,
        id,
        chat_id,
        user_id,
        rule AS title,
        action AS detail,
        COALESCE(matched, '') AS extra,
        created_at
    FROM violations
    WHERE ($4::BIGINT[] IS NULL OR chat_id = ANY($4::BIGINT[]))

    UNION ALL

    SELECT
        'ai_decision'::TEXT AS type,
        id,
        chat_id,
        user_id,
        verdict AS title,
        COALESCE(category, '') AS detail,
        COALESCE(action_taken, '') AS extra,
        created_at
    FROM ai_decisions
    WHERE ($4::BIGINT[] IS NULL OR chat_id = ANY($4::BIGINT[]))

    UNION ALL

    SELECT
        'config_change'::TEXT AS type,
        id,
        COALESCE(chat_id, 0) AS chat_id,
        admin_id AS user_id,
        action AS title,
        scope AS detail,
        COALESCE(diff, '') AS extra,
        created_at
    FROM config_audit
    WHERE (($4::BIGINT[] IS NULL AND $5::BOOLEAN = TRUE) OR chat_id = ANY($4::BIGINT[]))
) events
WHERE ($3::TEXT = '' OR type = $3)
ORDER BY created_at DESC
LIMIT $1
OFFSET $2;

-- name: CountRecentEvents :one
SELECT COUNT(*)::BIGINT
FROM (
    SELECT 'violation'::TEXT AS type, chat_id
    FROM violations
    WHERE ($2::BIGINT[] IS NULL OR chat_id = ANY($2::BIGINT[]))

    UNION ALL

    SELECT 'ai_decision'::TEXT AS type, chat_id
    FROM ai_decisions
    WHERE ($2::BIGINT[] IS NULL OR chat_id = ANY($2::BIGINT[]))

    UNION ALL

    SELECT 'config_change'::TEXT AS type, chat_id
    FROM config_audit
    WHERE (($2::BIGINT[] IS NULL AND $3::BOOLEAN = TRUE) OR chat_id = ANY($2::BIGINT[]))
) events
WHERE ($1::TEXT = '' OR type = $1);
