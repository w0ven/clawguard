package store

import "context"

const getTodayAICallCountScoped = `-- name: GetTodayAICallCountScoped :one
SELECT COUNT(*)::BIGINT
FROM ai_decisions
WHERE created_at >= CURRENT_DATE
  AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]))
`

const listDailyAICallsLast30Days = `-- name: ListDailyAICallsLast30Days :many
SELECT
    day::date AS date,
    COALESCE(calls, 0)::BIGINT AS calls
FROM (
    SELECT generate_series(
        CURRENT_DATE - INTERVAL '29 day',
        CURRENT_DATE,
        INTERVAL '1 day'
    ) AS day
) calendar
LEFT JOIN (
    SELECT
        date_trunc('day', created_at) AS day,
        COUNT(*)::BIGINT AS calls
    FROM ai_decisions
    WHERE created_at >= CURRENT_DATE - INTERVAL '29 day'
      AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]))
    GROUP BY 1
) stats USING (day)
ORDER BY date ASC
`

const listAICallsPerModelLast30Days = `-- name: ListAICallsPerModelLast30Days :many
SELECT
    model,
    COUNT(*)::BIGINT AS calls
FROM ai_decisions
WHERE created_at >= CURRENT_DATE - INTERVAL '29 day'
  AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]))
GROUP BY model
ORDER BY calls DESC, model ASC
`

const listAICallsPerChatLast30Days = `-- name: ListAICallsPerChatLast30Days :many
SELECT
    d.chat_id,
    g.title,
    COUNT(*)::BIGINT AS calls
FROM ai_decisions d
LEFT JOIN groups g ON g.chat_id = d.chat_id
WHERE d.created_at >= CURRENT_DATE - INTERVAL '29 day'
  AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR d.chat_id = ANY($1::BIGINT[]))
GROUP BY d.chat_id, g.title
ORDER BY calls DESC, d.chat_id ASC
LIMIT $2
`

func (q *Queries) GetTodayAICallCountScoped(ctx context.Context, scopedChatIDs []int64) (int64, error) {
	row := q.db.QueryRow(ctx, getTodayAICallCountScoped, scopedChatIDs)
	var calls int64
	err := row.Scan(&calls)
	return calls, err
}

func (q *Queries) ListDailyAICallsLast30Days(ctx context.Context, scopedChatIDs []int64) ([]AICallDaily, error) {
	rows, err := q.db.Query(ctx, listDailyAICallsLast30Days, scopedChatIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AICallDaily
	for rows.Next() {
		var item AICallDaily
		if err := rows.Scan(&item.Date, &item.Calls); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) ListAICallsPerModelLast30Days(ctx context.Context, scopedChatIDs []int64) ([]AICallPerModel, error) {
	rows, err := q.db.Query(ctx, listAICallsPerModelLast30Days, scopedChatIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AICallPerModel
	for rows.Next() {
		var item AICallPerModel
		if err := rows.Scan(&item.Model, &item.Calls); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) ListAICallsPerChatLast30Days(ctx context.Context, scopedChatIDs []int64, limit int32) ([]AICallPerChat, error) {
	rows, err := q.db.Query(ctx, listAICallsPerChatLast30Days, scopedChatIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AICallPerChat
	for rows.Next() {
		var item AICallPerChat
		if err := rows.Scan(&item.ChatID, &item.Title, &item.Calls); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
