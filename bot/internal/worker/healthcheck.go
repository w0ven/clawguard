package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/store"
)

const (
	healthcheckInterval      = 5 * time.Minute
	healthcheckAlertCooldown = 10 * time.Minute
	healthcheckSampleSize    = 10
	healthcheckAlertKey      = "alert:cooldown"
)

type Healthcheck struct {
	logger     *zap.Logger
	queries    *store.Queries
	botService *bot.Service
}

func NewHealthcheck(logger *zap.Logger, queries *store.Queries, botService *bot.Service) *Healthcheck {
	return &Healthcheck{
		logger:     logger,
		queries:    queries,
		botService: botService,
	}
}

func (w *Healthcheck) Run(ctx context.Context) {
	w.checkOnce(ctx)

	ticker := time.NewTicker(healthcheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("healthcheck worker stopped")
			return
		case <-ticker.C:
			w.checkOnce(ctx)
		}
	}
}

func (w *Healthcheck) checkOnce(ctx context.Context) {
	verdicts, err := w.queries.ListRecentAIDecisionVerdicts(ctx, healthcheckSampleSize)
	if err != nil {
		w.logger.Warn("load recent ai decision verdicts failed", zap.Error(err))
		return
	}
	if len(verdicts) < healthcheckSampleSize {
		return
	}
	for _, verdict := range verdicts {
		if strings.TrimSpace(strings.ToLower(verdict)) != "error" {
			return
		}
	}
	if err := w.sendAlert(ctx, fmt.Sprintf("ClawGuard 告警：AI 审核最近 %d 次全部失败，请尽快检查模型服务、网络和 Redis/Postgres 状态。", healthcheckSampleSize)); err != nil {
		w.logger.Warn("send healthcheck alert failed", zap.Error(err))
	}
}

func (w *Healthcheck) sendAlert(ctx context.Context, text string) error {
	if w.botService == nil || w.queries == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	if redisClient := w.botService.Redis(); redisClient != nil {
		ok, err := redisClient.SetNX(ctx, healthcheckAlertKey, "1", healthcheckAlertCooldown).Result()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	admin, err := w.queries.GetFirstOwnerAdmin(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if admin.TelegramID == 0 {
		return nil
	}
	return w.botService.SendHTMLPrivateMessage(admin.TelegramID, text)
}
