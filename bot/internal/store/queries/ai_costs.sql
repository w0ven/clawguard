-- name: GetTodayAICallCountScoped :one
SELECT COUNT(*)::BIGINT
FROM ai_decisions
WHERE created_at >= CURRENT_DATE
  AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]));

-- name: ListDailyAICallsLast30Days :many
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
ORDER BY date ASC;

-- name: ListAICallsPerModelLast30Days :many
SELECT
    model,
    COUNT(*)::BIGINT AS calls
FROM ai_decisions
WHERE created_at >= CURRENT_DATE - INTERVAL '29 day'
  AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]))
GROUP BY model
ORDER BY calls DESC, model ASC;

-- name: ListAICallsPerSceneLast30Days :many
SELECT
    scene,
    COUNT(*)::BIGINT AS calls
FROM ai_decisions
WHERE created_at >= CURRENT_DATE - INTERVAL '29 day'
  AND ($1::BIGINT[] IS NULL OR cardinality($1::BIGINT[]) = 0 OR chat_id = ANY($1::BIGINT[]))
GROUP BY scene
ORDER BY calls DESC, scene ASC;

-- name: ListAICallsPerChatLast30Days :many
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
LIMIT $2;
