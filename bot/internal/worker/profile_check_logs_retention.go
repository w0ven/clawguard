package worker

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

func StartProfileCheckLogsRetention(ctx context.Context, q *store.Queries, cfg config.RetentionConfig, logger *zap.Logger) {
	if q == nil || logger == nil {
		return
	}

	go func() {
		runProfileCheckLogsRetentionOnce(ctx, q, cfg, logger)

		for {
			next := nextRun3AM(time.Now())
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				runProfileCheckLogsRetentionOnce(ctx, q, cfg, logger)
			}
		}
	}()
}

func runProfileCheckLogsRetentionOnce(ctx context.Context, q *store.Queries, cfg config.RetentionConfig, logger *zap.Logger) {
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	days := int32(maxRetention(cfg.ProfileCheckLogsDays, 90))
	if n, err := q.DeleteOldProfileCheckLogs(runCtx, days); err != nil {
		logger.Warn("profile check logs retention cleanup failed", zap.Error(err))
	} else {
		logger.Info("profile check logs retention cleanup", zap.Int64("deleted", n), zap.Int32("days", days))
	}
}
