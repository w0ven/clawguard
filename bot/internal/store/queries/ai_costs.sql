-- name: GetTodayAICostCentsScoped :one
SELECT COALESCE(SUM(cost_cents), 0)::DOUBLE PRECISION
FROM ai_decisions
WHERE created_at >= CURRENT_DATE
  AND ($1::BIGINT[] IS NULL OR chat_id = ANY($1::BIGINT[]));

-- name: ListDailyAICostsLast30Days :many
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
      AND ($1::BIGINT[] IS NULL OR chat_id = ANY($1::BIGINT[]))
    GROUP BY 1
) stats USING (day)
ORDER BY date ASC;

-- name: ListAICostsPerModelLast30Days :many
SELECT
    model,
    COALESCE(SUM(cost_cents), 0)::DOUBLE PRECISION AS cost_cents,
    COUNT(*)::BIGINT AS calls
FROM ai_decisions
WHERE created_at >= CURRENT_DATE - INTERVAL '29 day'
  AND ($1::BIGINT[] IS NULL OR chat_id = ANY($1::BIGINT[]))
GROUP BY model
ORDER BY cost_cents DESC, calls DESC, model ASC;

-- name: ListAICostsPerChatLast30Days :many
SELECT
    d.chat_id,
    g.title,
    COALESCE(SUM(d.cost_cents), 0)::DOUBLE PRECISION AS cost_cents,
    COUNT(*)::BIGINT AS calls
FROM ai_decisions d
LEFT JOIN groups g ON g.chat_id = d.chat_id
WHERE d.created_at >= CURRENT_DATE - INTERVAL '29 day'
  AND ($1::BIGINT[] IS NULL OR d.chat_id = ANY($1::BIGINT[]))
GROUP BY d.chat_id, g.title
ORDER BY cost_cents DESC, calls DESC, d.chat_id ASC
LIMIT $2;
