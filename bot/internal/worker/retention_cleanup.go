package worker

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

func StartRetentionCleanup(ctx context.Context, q *store.Queries, cfg config.RetentionConfig, logger *zap.Logger) {
	if q == nil || logger == nil {
		return
	}

	go func() {
		runRetentionOnce(ctx, q, cfg, logger)

		for {
			next := nextRun3AM(time.Now())
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				runRetentionOnce(ctx, q, cfg, logger)
			}
		}
	}()
}

func nextRun3AM(now time.Time) time.Time {
	loc := time.FixedZone("CST", 8*3600)
	local := now.In(loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), 3, 0, 0, 0, loc)
	if !next.After(local) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func runRetentionOnce(ctx context.Context, q *store.Queries, cfg config.RetentionConfig, logger *zap.Logger) {
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	eventDays := int32(maxRetention(cfg.EventsDays, 30))
	if n, err := q.DeleteOldViolations(runCtx, eventDays); err != nil {
		logger.Warn("retention cleanup event violations failed", zap.Error(err))
	} else {
		logger.Info("retention cleanup event violations", zap.Int64("deleted", n), zap.Int32("days", eventDays))
	}
	if n, err := q.DeleteOldAIDecisions(runCtx, eventDays); err != nil {
		logger.Warn("retention cleanup event ai decisions failed", zap.Error(err))
	} else {
		logger.Info("retention cleanup event ai decisions", zap.Int64("deleted", n), zap.Int32("days", eventDays))
	}
	if n, err := q.DeleteOldConfigAudit(runCtx, eventDays); err != nil {
		logger.Warn("retention cleanup event config audit failed", zap.Error(err))
	} else {
		logger.Info("retention cleanup event config audit", zap.Int64("deleted", n), zap.Int32("days", eventDays))
	}
	if n, err := q.DeleteExpiredPendingVerifications(runCtx, int32(maxRetention(cfg.PendingVerificationsHours, 1))); err != nil {
		logger.Warn("retention cleanup pending verifications failed", zap.Error(err))
	} else {
		logger.Info("retention cleanup pending verifications", zap.Int64("deleted", n))
	}

	zombieDays := int32(maxRetention(cfg.ZombieDays, 30))
	if n, err := q.ArchiveZombieUserTrust(runCtx, zombieDays); err != nil {
		logger.Warn("retention archive zombie users failed", zap.Error(err))
	} else {
		logger.Info("retention archive zombie users", zap.Int64("archived", n), zap.Int32("days", zombieDays))
	}

	bannedDays := int32(maxRetention(cfg.BannedDays, 90))
	if n, err := q.DeleteOldBannedUserTrust(runCtx, bannedDays); err != nil {
		logger.Warn("retention cleanup banned users failed", zap.Error(err))
	} else {
		logger.Info("retention cleanup banned users", zap.Int64("deleted", n), zap.Int32("days", bannedDays))
	}
}

func maxRetention(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
