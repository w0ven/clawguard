package store

import (
	"context"
	"time"
)

const getLLMModelStats = `-- name: GetLLMModelStats :one
SELECT model_id, last_check_at, last_ok_at, last_error, healthy,
       latency_p50_ms, latency_p95_ms, success_1h, fail_1h, success_24h, fail_24h, updated_at
FROM llm_model_stats
WHERE model_id = $1
`

func (q *Queries) GetLLMModelStats(ctx context.Context, modelID int64) (LlmModelStat, error) {
	row := q.db.QueryRow(ctx, getLLMModelStats, modelID)
	var i LlmModelStat
	err := row.Scan(
		&i.ModelID,
		&i.LastCheckAt,
		&i.LastOkAt,
		&i.LastError,
		&i.Healthy,
		&i.LatencyP50Ms,
		&i.LatencyP95Ms,
		&i.Success1h,
		&i.Fail1h,
		&i.Success24h,
		&i.Fail24h,
		&i.UpdatedAt,
	)
	return i, err
}

const listLLMModelStats = `-- name: ListLLMModelStats :many
SELECT model_id, last_check_at, last_ok_at, last_error, healthy,
       latency_p50_ms, latency_p95_ms, success_1h, fail_1h, success_24h, fail_24h, updated_at
FROM llm_model_stats
`

func (q *Queries) ListLLMModelStats(ctx context.Context) ([]LlmModelStat, error) {
	rows, err := q.db.Query(ctx, listLLMModelStats)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []LlmModelStat
	for rows.Next() {
		var i LlmModelStat
		if err := rows.Scan(
			&i.ModelID,
			&i.LastCheckAt,
			&i.LastOkAt,
			&i.LastError,
			&i.Healthy,
			&i.LatencyP50Ms,
			&i.LatencyP95Ms,
			&i.Success1h,
			&i.Fail1h,
			&i.Success24h,
			&i.Fail24h,
			&i.UpdatedAt,
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

const upsertLLMProbeResult = `-- name: UpsertLLMProbeResult :exec
INSERT INTO llm_model_stats (model_id, healthy, last_check_at, last_ok_at, last_error, updated_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (model_id) DO UPDATE SET
    healthy       = EXCLUDED.healthy,
    last_check_at = EXCLUDED.last_check_at,
    last_ok_at    = COALESCE(EXCLUDED.last_ok_at, llm_model_stats.last_ok_at),
    last_error    = EXCLUDED.last_error,
    updated_at    = now()
`

type UpsertLLMProbeResultParams struct {
	ModelID     int64
	Healthy     bool
	LastCheckAt *time.Time
	LastOkAt    *time.Time
	LastError   string
}

func (q *Queries) UpsertLLMProbeResult(ctx context.Context, arg UpsertLLMProbeResultParams) error {
	_, err := q.db.Exec(ctx, upsertLLMProbeResult,
		arg.ModelID,
		arg.Healthy,
		arg.LastCheckAt,
		arg.LastOkAt,
		arg.LastError,
	)
	return err
}

const upsertLLMStatsAggregation = `-- name: UpsertLLMStatsAggregation :exec
INSERT INTO llm_model_stats (model_id, latency_p50_ms, latency_p95_ms,
                             success_1h, fail_1h, success_24h, fail_24h, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now())
ON CONFLICT (model_id) DO UPDATE SET
    latency_p50_ms = EXCLUDED.latency_p50_ms,
    latency_p95_ms = EXCLUDED.latency_p95_ms,
    success_1h     = EXCLUDED.success_1h,
    fail_1h        = EXCLUDED.fail_1h,
    success_24h    = EXCLUDED.success_24h,
    fail_24h       = EXCLUDED.fail_24h,
    updated_at     = now()
`

type UpsertLLMStatsAggregationParams struct {
	ModelID      int64
	LatencyP50Ms *int32
	LatencyP95Ms *int32
	Success1h    int32
	Fail1h       int32
	Success24h   int32
	Fail24h      int32
}

func (q *Queries) UpsertLLMStatsAggregation(ctx context.Context, arg UpsertLLMStatsAggregationParams) error {
	_, err := q.db.Exec(ctx, upsertLLMStatsAggregation,
		arg.ModelID,
		arg.LatencyP50Ms,
		arg.LatencyP95Ms,
		arg.Success1h,
		arg.Fail1h,
		arg.Success24h,
		arg.Fail24h,
	)
	return err
}

const deleteOrphanLLMStats = `-- name: DeleteOrphanLLMStats :exec
DELETE FROM llm_model_stats
WHERE model_id NOT IN (SELECT id FROM llm_models)
`

func (q *Queries) DeleteOrphanLLMStats(ctx context.Context) error {
	_, err := q.db.Exec(ctx, deleteOrphanLLMStats)
	return err
}
