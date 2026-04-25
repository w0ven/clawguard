package store

import (
	"context"
	"time"
)

const getTodayAICostCentsScoped = `-- name: GetTodayAICostCentsScoped :one
SELECT COALESCE(SUM(cost_cents), 0)::DOUBLE PRECISION
FROM ai_decisions
WHERE created_at >= CURRENT_DATE
  AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]))
`

const listDailyAICostsLast30Days = `-- name: ListDailyAICostsLast30Days :many
SELECT
    day::date AS date,
    COALESCE(cost_cents, 0)::DOUBLE PRECISION AS cost_cents,
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
        COALESCE(SUM(cost_cents), 0)::DOUBLE PRECISION AS cost_cents,
        COUNT(*)::BIGINT AS calls
    FROM ai_decisions
    WHERE created_at >= CURRENT_DATE - INTERVAL '29 day'
      AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]))
    GROUP BY 1
) stats USING (day)
ORDER BY date ASC
`

const listAICostsPerModelLast30Days = `-- name: ListAICostsPerModelLast30Days :many
SELECT
    model,
    COALESCE(SUM(cost_cents), 0)::DOUBLE PRECISION AS cost_cents,
    COUNT(*)::BIGINT AS calls
FROM ai_decisions
WHERE created_at >= CURRENT_DATE - INTERVAL '29 day'
  AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]))
GROUP BY model
ORDER BY cost_cents DESC, calls DESC, model ASC
`

const listAICostsPerChatLast30Days = `-- name: ListAICostsPerChatLast30Days :many
SELECT
    d.chat_id,
    g.title,
    COALESCE(SUM(d.cost_cents), 0)::DOUBLE PRECISION AS cost_cents,
    COUNT(*)::BIGINT AS calls
FROM ai_decisions d
LEFT JOIN groups g ON g.chat_id = d.chat_id
WHERE d.created_at >= CURRENT_DATE - INTERVAL '29 day'
  AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR d.chat_id = ANY($1::BIGINT[]))
GROUP BY d.chat_id, g.title
ORDER BY cost_cents DESC, calls DESC, d.chat_id ASC
LIMIT $2
`

func (q *Queries) GetTodayAICostCentsScoped(ctx context.Context, scopedChatIDs []int64) (float64, error) {
	row := q.db.QueryRow(ctx, getTodayAICostCentsScoped, scopedChatIDs)
	var cents float64
	err := row.Scan(&cents)
	return cents, err
}

func (q *Queries) ListDailyAICostsLast30Days(ctx context.Context, scopedChatIDs []int64) ([]AICostDaily, error) {
	rows, err := q.db.Query(ctx, listDailyAICostsLast30Days, scopedChatIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AICostDaily
	for rows.Next() {
		var item AICostDaily
		if err := rows.Scan(&item.Date, &item.CostCents, &item.Calls); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) ListAICostsPerModelLast30Days(ctx context.Context, scopedChatIDs []int64) ([]AICostPerModel, error) {
	rows, err := q.db.Query(ctx, listAICostsPerModelLast30Days, scopedChatIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AICostPerModel
	for rows.Next() {
		var item AICostPerModel
		if err := rows.Scan(&item.Model, &item.CostCents, &item.Calls); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) ListAICostsPerChatLast30Days(ctx context.Context, scopedChatIDs []int64, limit int32) ([]AICostPerChat, error) {
	rows, err := q.db.Query(ctx, listAICostsPerChatLast30Days, scopedChatIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AICostPerChat
	for rows.Next() {
		var item AICostPerChat
		if err := rows.Scan(&item.ChatID, &item.Title, &item.CostCents, &item.Calls); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

var _ = time.Time{}
