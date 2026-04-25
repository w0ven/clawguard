package store

import (
	"context"
	"time"
)

const listEventsPaginated = `-- name: ListEventsPaginated :many
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
    WHERE ($4::BIGINT[] IS NULL OR cardinality($4::BIGINT[]) = 0 OR chat_id = ANY($4::BIGINT[]))

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
    WHERE ($4::BIGINT[] IS NULL OR cardinality($4::BIGINT[]) = 0 OR chat_id = ANY($4::BIGINT[]))

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
    WHERE ((($4::BIGINT[] IS NULL OR cardinality($4::BIGINT[]) = 0) AND $5::BOOLEAN = TRUE) OR chat_id = ANY($4::BIGINT[]))
) events
WHERE ($3::TEXT = '' OR type = $3)
ORDER BY created_at DESC
LIMIT $1
OFFSET $2
`

const countRecentEvents = `-- name: CountRecentEvents :one
SELECT COUNT(*)::BIGINT
FROM (
    SELECT 'violation'::TEXT AS type, chat_id
    FROM violations
    WHERE ($2::BIGINT[] IS NULL OR cardinality($2::BIGINT[]) = 0 OR chat_id = ANY($2::BIGINT[]))

    UNION ALL

    SELECT 'ai_decision'::TEXT AS type, chat_id
    FROM ai_decisions
    WHERE ($2::BIGINT[] IS NULL OR cardinality($2::BIGINT[]) = 0 OR chat_id = ANY($2::BIGINT[]))

    UNION ALL

    SELECT 'config_change'::TEXT AS type, chat_id
    FROM config_audit
    WHERE ((($2::BIGINT[] IS NULL OR cardinality($2::BIGINT[]) = 0) AND $3::BOOLEAN = TRUE) OR chat_id = ANY($2::BIGINT[]))
) events
WHERE ($1::TEXT = '' OR type = $1)
`

type RecentEvent struct {
	Type      string
	ID        int64
	ChatID    int64
	UserID    int64
	Title     string
	Detail    string
	Extra     string
	CreatedAt time.Time
}

type ListEventsPaginatedParams struct {
	Limit         int32
	Offset        int32
	EventType     string
	ScopedChatIDs []int64
	IncludeGlobal bool
}

func (q *Queries) ListEventsPaginated(ctx context.Context, arg ListEventsPaginatedParams) ([]RecentEvent, error) {
	rows, err := q.db.Query(ctx, listEventsPaginated, arg.Limit, arg.Offset, arg.EventType, arg.ScopedChatIDs, arg.IncludeGlobal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []RecentEvent
	for rows.Next() {
		var item RecentEvent
		if err := rows.Scan(&item.Type, &item.ID, &item.ChatID, &item.UserID, &item.Title, &item.Detail, &item.Extra, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

type CountRecentEventsParams struct {
	EventType     string
	ScopedChatIDs []int64
	IncludeGlobal bool
}

func (q *Queries) CountRecentEvents(ctx context.Context, arg CountRecentEventsParams) (int64, error) {
	row := q.db.QueryRow(ctx, countRecentEvents, arg.EventType, arg.ScopedChatIDs, arg.IncludeGlobal)
	var count int64
	err := row.Scan(&count)
	return count, err
}
