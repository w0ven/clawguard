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

	windows := resolveRetentionWindows(cfg)
	if n, err := q.DeleteOldViolations(runCtx, windows.violationsDays); err != nil {
		logger.Warn("retention cleanup violations failed", zap.Error(err))
	} else {
		logger.Info("retention cleanup violations", zap.Int64("deleted", n), zap.Int32("days", windows.violationsDays))
	}
	if n, err := q.DeleteOldAIDecisions(runCtx, windows.aiDecisionsDays); err != nil {
		logger.Warn("retention cleanup ai decisions failed", zap.Error(err))
	} else {
		logger.Info("retention cleanup ai decisions", zap.Int64("deleted", n), zap.Int32("days", windows.aiDecisionsDays))
	}
	if n, err := q.DeleteOldConfigAudit(runCtx, windows.configAuditDays); err != nil {
		logger.Warn("retention cleanup config audit failed", zap.Error(err))
	} else {
		logger.Info("retention cleanup config audit", zap.Int64("deleted", n), zap.Int32("days", windows.configAuditDays))
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

type retentionWindows struct {
	violationsDays  int32
	aiDecisionsDays int32
	configAuditDays int32
}

func resolveRetentionWindows(cfg config.RetentionConfig) retentionWindows {
	return retentionWindows{
		violationsDays:  int32(maxRetention(cfg.ViolationsDays, 90)),
		aiDecisionsDays: int32(maxRetention(cfg.AIDecisionsDays, 90)),
		configAuditDays: int32(maxRetention(cfg.ConfigAuditDays, 365)),
	}
}

func maxRetention(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
