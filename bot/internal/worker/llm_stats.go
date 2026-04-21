package worker

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/store"
)

const defaultStatsInterval = 30 * time.Second

// LLMStatsAggregator periodically recomputes rolling success/fail counts and
// latency percentiles from ai_decisions and upserts them into llm_model_stats.
// It never touches the healthy / last_error / last_check_at columns: those are
// exclusively owned by the prober.
type LLMStatsAggregator struct {
	logger   *zap.Logger
	queries  *store.Queries
	interval time.Duration
}

func NewLLMStatsAggregator(logger *zap.Logger, queries *store.Queries) *LLMStatsAggregator {
	return &LLMStatsAggregator{
		logger:   logger,
		queries:  queries,
		interval: defaultStatsInterval,
	}
}

func (a *LLMStatsAggregator) Run(ctx context.Context) {
	a.logger.Info("starting llm stats aggregator", zap.Duration("interval", a.interval))

	a.once(ctx)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("llm stats aggregator stopped")
			return
		case <-ticker.C:
			a.once(ctx)
		}
	}
}

func (a *LLMStatsAggregator) once(ctx context.Context) {
	rows, err := a.queries.AggregateAIDecisionsByModel(ctx)
	if err != nil {
		a.logger.Warn("aggregate ai decisions failed", zap.Error(err))
		return
	}

	for _, row := range rows {
		if row.ModelID == nil {
			continue
		}
		var p50, p95 *int32
		if row.LatencyP50Ms > 0 {
			v := row.LatencyP50Ms
			p50 = &v
		}
		if row.LatencyP95Ms > 0 {
			v := row.LatencyP95Ms
			p95 = &v
		}
		if err := a.queries.UpsertLLMStatsAggregation(ctx, store.UpsertLLMStatsAggregationParams{
			ModelID:      *row.ModelID,
			LatencyP50Ms: p50,
			LatencyP95Ms: p95,
			Success1h:    row.Success1h,
			Fail1h:       row.Fail1h,
			Success24h:   row.Success24h,
			Fail24h:      row.Fail24h,
		}); err != nil {
			a.logger.Warn("upsert llm stats aggregation failed", zap.Error(err), zap.Int64("model_id", *row.ModelID))
		}
	}
}
