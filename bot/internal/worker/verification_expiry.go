package worker

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/store"
)

const (
	verificationExpiryInterval = 30 * time.Second
	verificationExpiryBatch    = 100
)

type VerificationExpiry struct {
	logger     *zap.Logger
	queries    *store.Queries
	botService *bot.Service
}

func NewVerificationExpiry(logger *zap.Logger, queries *store.Queries, botService *bot.Service) *VerificationExpiry {
	return &VerificationExpiry{
		logger:     logger,
		queries:    queries,
		botService: botService,
	}
}

func (w *VerificationExpiry) Run(ctx context.Context) {
	w.processExpired(ctx)

	ticker := time.NewTicker(verificationExpiryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("verification expiry worker stopped")
			return
		case <-ticker.C:
			w.processExpired(ctx)
		}
	}
}

func (w *VerificationExpiry) processExpired(ctx context.Context) {
	pendingList, err := w.queries.GetExpiredPendingVerifications(ctx, verificationExpiryBatch)
	if err != nil {
		w.logger.Error("load expired pending verifications", zap.Error(err))
		return
	}

	if len(pendingList) == 0 {
		return
	}

	for _, pending := range pendingList {
		if err := w.botService.HandleVerificationExpiry(ctx, pending); err != nil {
			w.logger.Error(
				"handle expired pending verification",
				zap.Error(err),
				zap.Int64("chat_id", pending.ChatID),
				zap.Int64("user_id", pending.UserID),
				zap.String("method", pending.Method),
				zap.Time("expires_at", pending.ExpiresAt),
			)
		}
	}
}
