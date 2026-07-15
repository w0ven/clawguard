package worker

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/bot"
)

const (
	joinProtectionRecoveryInterval = 15 * time.Second
	joinProtectionRecoveryBatch    = 100
)

type JoinProtectionRecovery struct {
	logger     *zap.Logger
	botService *bot.Service
}

func NewJoinProtectionRecovery(logger *zap.Logger, botService *bot.Service) *JoinProtectionRecovery {
	return &JoinProtectionRecovery{logger: logger, botService: botService}
}

func (w *JoinProtectionRecovery) Run(ctx context.Context) {
	w.process(ctx)
	ticker := time.NewTicker(joinProtectionRecoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.process(ctx)
		}
	}
}

func (w *JoinProtectionRecovery) process(ctx context.Context) {
	succeeded := true
	if err := w.botService.ProcessJoinProtectionRecoveries(ctx, joinProtectionRecoveryBatch); err != nil {
		w.logger.Error("process join protection recoveries", zap.Error(err))
		succeeded = false
	}
	if err := w.botService.ProcessJoinProtectionCleanupFallbacks(ctx, joinProtectionRecoveryBatch); err != nil {
		w.logger.Error("process join protection cleanup fallbacks", zap.Error(err))
		succeeded = false
	}
	if succeeded {
		w.botService.RecordWorkerSuccess(ctx, "join-protection-recovery")
	}
}
