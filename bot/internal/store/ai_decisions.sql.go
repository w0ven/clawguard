package store

import "context"

const insertAIDecision = `-- name: InsertAIDecision :one
INSERT INTO ai_decisions (
    chat_id,
    user_id,
    message_id,
    message_text,
    provider_id,
    model_id,
    model,
    prompt_version,
    verdict,
    confidence,
    category,
    reason,
    action_taken,
    admin_override,
    latency_ms,
    cost_cents
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING id, chat_id, user_id, message_id, message_text, provider_id, model_id, model, prompt_version, verdict, confidence, category, reason, action_taken, admin_override, latency_ms, cost_cents, created_at
`

const listAIDecisions = `-- name: ListAIDecisions :many
SELECT id, chat_id, user_id, message_id, message_text, provider_id, model_id, model, prompt_version, verdict, confidence, category, reason, action_taken, admin_override, latency_ms, cost_cents, created_at
FROM ai_decisions
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
  AND ($2::BIGINT IS NULL OR user_id = $2)
  AND ($3::TEXT = '' OR verdict = $3)
  AND ($4::TEXT = '' OR COALESCE(admin_override, '') = $4)
  AND ($5::TEXT = '' OR category = $5)
  AND ($6::TEXT = '' OR action_taken = $6)
  AND ($7::TIMESTAMPTZ IS NULL OR created_at >= $7)
  AND ($8::TIMESTAMPTZ IS NULL OR created_at <= $8)
ORDER BY created_at DESC
LIMIT $9
`

const getAIDecisionByID = `-- name: GetAIDecisionByID :one
SELECT id, chat_id, user_id, message_id, message_text, provider_id, model_id, model, prompt_version, verdict, confidence, category, reason, action_taken, admin_override, latency_ms, cost_cents, created_at
FROM ai_decisions
WHERE id = $1
`

const updateAIDecisionOverride = `-- name: UpdateAIDecisionOverride :one
UPDATE ai_decisions
SET admin_override = $2
WHERE id = $1
RETURNING id, chat_id, user_id, message_id, message_text, provider_id, model_id, model, prompt_version, verdict, confidence, category, reason, action_taken, admin_override, latency_ms, cost_cents, created_at
`

const listAICostsByDay = `-- name: ListAICostsByDay :many
SELECT
    date_trunc('day', created_at) AS day,
    COUNT(*)::BIGINT AS calls,
    COALESCE(SUM(cost_cents), 0)::DOUBLE PRECISION AS cost_cents,
    model
FROM ai_decisions
WHERE ($1::BIGINT IS NULL OR chat_id = $1)
GROUP BY day, model
ORDER BY day DESC, model ASC
`

type InsertAIDecisionParams struct {
	ChatID        int64
	UserID        int64
	MessageID     int64
	MessageText   *string
	ProviderID    *int64
	ModelID       *int64
	Model         string
	PromptVersion string
	Verdict       string
	Confidence    float64
	Category      string
	Reason        *string
	ActionTaken   string
	AdminOverride *string
	LatencyMs     int32
	CostCents     float64
}

type ListAIDecisionsParams struct {
	ChatID        *int64
	UserID        *int64
	Verdict       string
	AdminOverride string
	Category      string
	ActionTaken   string
	Since         *string
	Until         *string
	Limit         int32
}

func (q *Queries) InsertAIDecision(ctx context.Context, arg InsertAIDecisionParams) (AIDecision, error) {
	row := q.db.QueryRow(ctx, insertAIDecision, arg.ChatID, arg.UserID, arg.MessageID, arg.MessageText, arg.ProviderID, arg.ModelID, arg.Model, arg.PromptVersion, arg.Verdict, arg.Confidence, arg.Category, arg.Reason, arg.ActionTaken, arg.AdminOverride, arg.LatencyMs, arg.CostCents)
	var i AIDecision
	err := row.Scan(&i.ID, &i.ChatID, &i.UserID, &i.MessageID, &i.MessageText, &i.ProviderID, &i.ModelID, &i.Model, &i.PromptVersion, &i.Verdict, &i.Confidence, &i.Category, &i.Reason, &i.ActionTaken, &i.AdminOverride, &i.LatencyMs, &i.CostCents, &i.CreatedAt)
	return i, err
}

func (q *Queries) ListAIDecisions(ctx context.Context, arg ListAIDecisionsParams) ([]AIDecision, error) {
	rows, err := q.db.Query(ctx, listAIDecisions, arg.ChatID, arg.UserID, arg.Verdict, arg.AdminOverride, arg.Category, arg.ActionTaken, arg.Since, arg.Until, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AIDecision
	for rows.Next() {
		var i AIDecision
		if err := rows.Scan(&i.ID, &i.ChatID, &i.UserID, &i.MessageID, &i.MessageText, &i.ProviderID, &i.ModelID, &i.Model, &i.PromptVersion, &i.Verdict, &i.Confidence, &i.Category, &i.Reason, &i.ActionTaken, &i.AdminOverride, &i.LatencyMs, &i.CostCents, &i.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (q *Queries) GetAIDecisionByID(ctx context.Context, id int64) (AIDecision, error) {
	row := q.db.QueryRow(ctx, getAIDecisionByID, id)
	var i AIDecision
	err := row.Scan(&i.ID, &i.ChatID, &i.UserID, &i.MessageID, &i.MessageText, &i.ProviderID, &i.ModelID, &i.Model, &i.PromptVersion, &i.Verdict, &i.Confidence, &i.Category, &i.Reason, &i.ActionTaken, &i.AdminOverride, &i.LatencyMs, &i.CostCents, &i.CreatedAt)
	return i, err
}

func (q *Queries) UpdateAIDecisionOverride(ctx context.Context, id int64, adminOverride *string) (AIDecision, error) {
	row := q.db.QueryRow(ctx, updateAIDecisionOverride, id, adminOverride)
	var i AIDecision
	err := row.Scan(&i.ID, &i.ChatID, &i.UserID, &i.MessageID, &i.MessageText, &i.ProviderID, &i.ModelID, &i.Model, &i.PromptVersion, &i.Verdict, &i.Confidence, &i.Category, &i.Reason, &i.ActionTaken, &i.AdminOverride, &i.LatencyMs, &i.CostCents, &i.CreatedAt)
	return i, err
}

func (q *Queries) ListAICostsByDay(ctx context.Context, chatID *int64) ([]AICostStat, error) {
	rows, err := q.db.Query(ctx, listAICostsByDay, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AICostStat
	for rows.Next() {
		var i AICostStat
		if err := rows.Scan(&i.Day, &i.Calls, &i.CostCents, &i.Model); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const aggregateAIDecisionsByModel = `-- name: AggregateAIDecisionsByModel :many
SELECT
    model_id,
    COUNT(*) FILTER (WHERE created_at > now() - interval '1 hour'  AND verdict <> 'error')::INT AS success_1h,
    COUNT(*) FILTER (WHERE created_at > now() - interval '1 hour'  AND verdict =  'error')::INT AS fail_1h,
    COUNT(*) FILTER (WHERE created_at > now() - interval '24 hours' AND verdict <> 'error')::INT AS success_24h,
    COUNT(*) FILTER (WHERE created_at > now() - interval '24 hours' AND verdict =  'error')::INT AS fail_24h,
    COALESCE(PERCENTILE_CONT(0.5)  WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE created_at > now() - interval '1 hour' AND latency_ms > 0), 0)::INT AS latency_p50_ms,
    COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE created_at > now() - interval '1 hour' AND latency_ms > 0), 0)::INT AS latency_p95_ms
FROM ai_decisions
WHERE model_id IS NOT NULL
  AND created_at > now() - interval '24 hours'
GROUP BY model_id
`

type AggregateAIDecisionsByModelRow struct {
	ModelID      *int64
	Success1h    int32
	Fail1h       int32
	Success24h   int32
	Fail24h      int32
	LatencyP50Ms int32
	LatencyP95Ms int32
}

func (q *Queries) AggregateAIDecisionsByModel(ctx context.Context) ([]AggregateAIDecisionsByModelRow, error) {
	rows, err := q.db.Query(ctx, aggregateAIDecisionsByModel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AggregateAIDecisionsByModelRow
	for rows.Next() {
		var i AggregateAIDecisionsByModelRow
		if err := rows.Scan(
			&i.ModelID,
			&i.Success1h,
			&i.Fail1h,
			&i.Success24h,
			&i.Fail24h,
			&i.LatencyP50Ms,
			&i.LatencyP95Ms,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
