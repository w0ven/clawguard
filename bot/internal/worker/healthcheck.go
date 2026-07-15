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
	return &Healthcheck{logger: logger, queries: queries, botService: botService}
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
	alerts := operationsAlerts(w.loadOperationsStatus(ctx))
	verdicts, err := w.queries.ListRecentAIDecisionVerdicts(ctx, healthcheckSampleSize)
	if err != nil {
		w.logger.Warn("load recent ai decision verdicts failed", zap.Error(err))
	} else if len(verdicts) >= healthcheckSampleSize {
		allFailed := true
		for _, verdict := range verdicts {
			if strings.TrimSpace(strings.ToLower(verdict)) != "error" {
				allFailed = false
				break
			}
		}
		if allFailed {
			alerts = append(alerts, fmt.Sprintf("AI moderation failed for the last %d decisions", healthcheckSampleSize))
		}
	}

	if len(alerts) == 0 {
		return
	}
	message := "<b>ClawGuard operations alert</b><br>" + strings.Join(alerts, "<br>")
	if err := w.sendAlert(ctx, message); err != nil {
		w.logger.Warn("send healthcheck alert failed", zap.Error(err))
	}
}

type operationsStatus struct {
	backlog store.OperationsBacklog
	runtime bot.OperationsRuntimeStatus
}

func (w *Healthcheck) loadOperationsStatus(ctx context.Context) operationsStatus {
	status := operationsStatus{runtime: w.botService.OperationsRuntimeStatus(ctx)}
	backlog, err := w.queries.GetOperationsBacklog(ctx)
	if err != nil {
		w.logger.Warn("load operations backlog failed", zap.Error(err))
		return status
	}
	status.backlog = backlog
	return status
}

func operationsAlerts(status operationsStatus) []string {
	alerts := make([]string, 0, 6)
	if status.backlog.DueCleanup > 100 || status.backlog.OldestDueCleanupSeconds > 15*60 {
		alerts = append(alerts, fmt.Sprintf("Verification cleanup backlog: due=%d, oldest=%ds", status.backlog.DueCleanup, status.backlog.OldestDueCleanupSeconds))
	}
	if status.backlog.RetryingCleanup > 20 {
		alerts = append(alerts, fmt.Sprintf("Verification cleanup retries: %d", status.backlog.RetryingCleanup))
	}
	if status.runtime.JoinCleanupDeadLetters != nil && *status.runtime.JoinCleanupDeadLetters > 0 {
		alerts = append(alerts, fmt.Sprintf("Join cleanup dead letters: %d", *status.runtime.JoinCleanupDeadLetters))
	}
	if status.runtime.BackupSecondsAgo != nil && *status.runtime.BackupSecondsAgo > int64((36*time.Hour)/time.Second) {
		alerts = append(alerts, fmt.Sprintf("Last successful backup: %ds ago", *status.runtime.BackupSecondsAgo))
	}
	if status.runtime.VerificationWorkerSecondsAgo != nil && *status.runtime.VerificationWorkerSecondsAgo > 120 {
		alerts = append(alerts, fmt.Sprintf("Verification worker heartbeat: %ds ago", *status.runtime.VerificationWorkerSecondsAgo))
	}
	if status.runtime.JoinRecoveryWorkerSecondsAgo != nil && *status.runtime.JoinRecoveryWorkerSecondsAgo > 90 {
		alerts = append(alerts, fmt.Sprintf("Join recovery worker heartbeat: %ds ago", *status.runtime.JoinRecoveryWorkerSecondsAgo))
	}
	return alerts
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
