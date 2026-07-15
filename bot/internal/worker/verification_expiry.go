package worker

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

var verificationExpiryLeaseSequence atomic.Uint64

const (
	verificationExpiryInterval = 30 * time.Second
	verificationExpiryBatch    = 20
	verificationExpiryLease    = 15 * time.Minute
)

type VerificationExpiry struct {
	logger     *zap.Logger
	queries    *store.Queries
	botService *bot.Service
	leaseOwner string
}

func NewVerificationExpiry(logger *zap.Logger, queries *store.Queries, botService *bot.Service) *VerificationExpiry {
	return &VerificationExpiry{
		logger:     logger,
		queries:    queries,
		botService: botService,
		leaseOwner: verificationExpiryLeaseOwner(),
	}
}

func verificationExpiryLeaseOwner() string {
	hostname, _ := os.Hostname()
	return fmt.Sprintf("%s:%d:%d:%d", hostname, os.Getpid(), time.Now().UnixNano(), verificationExpiryLeaseSequence.Add(1))
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
	pendingList, err := w.queries.ClaimExpiredPendingVerifications(ctx, store.ClaimExpiredPendingVerificationsParams{
		LeaseSeconds: int32(verificationExpiryLease / time.Second),
		LeaseOwner:   w.leaseOwner,
		BatchLimit:   verificationExpiryBatch,
	})
	if err != nil {
		w.logger.Error("load expired pending verifications", zap.Error(err))
		return
	}
	w.botService.RecordWorkerSuccess(ctx, "verification-expiry")

	if len(pendingList) == 0 {
		return
	}

	for _, pending := range pendingList {
		result, handleErr := w.botService.HandleVerificationExpiry(ctx, pending)
		if handleErr != nil {
			w.logger.Error(
				"handle expired pending verification",
				zap.Error(handleErr),
				zap.Int64("chat_id", pending.ChatID),
				zap.Int64("user_id", pending.UserID),
				zap.String("method", pending.Method),
				zap.Time("expires_at", pending.ExpiresAt),
			)
			result = bot.VerificationExpiryResult{
				RetryAt:     time.Now().Add(verificationExpiryRetryDelay(pending.AttemptCount)),
				RetryReason: redact.ErrorString(handleErr),
			}
		}
		if result.RetryAt.IsZero() {
			continue
		}
		reason := stringPtr(result.RetryReason)
		updated, err := w.queries.RescheduleClaimedPendingVerification(ctx, store.RescheduleClaimedPendingVerificationParams{
			ID:            pending.ID,
			LeaseOwner:    w.leaseOwner,
			NextAttemptAt: result.RetryAt,
			LastError:     reason,
		})
		if err != nil {
			w.logger.Error(
				"reschedule expired pending verification",
				zap.Error(err),
				zap.Int64("chat_id", pending.ChatID),
				zap.Int64("user_id", pending.UserID),
				zap.Time("retry_at", result.RetryAt),
			)
		} else if updated == 0 {
			w.logger.Warn(
				"expired pending verification lease was lost before reschedule",
				zap.Int64("chat_id", pending.ChatID),
				zap.Int64("user_id", pending.UserID),
				zap.String("lease_owner", w.leaseOwner),
			)
		} else {
			w.logger.Info(
				"expired pending verification rescheduled",
				zap.String("event", "verification_cleanup_rescheduled"),
				zap.Int64("chat_id", pending.ChatID),
				zap.Int64("user_id", pending.UserID),
				zap.Int32("attempt", pending.AttemptCount),
				zap.String("reason", result.RetryReason),
				zap.Time("retry_at", result.RetryAt),
			)
		}
	}
}

func verificationExpiryRetryDelay(attempt int32) time.Duration {
	delay := 30 * time.Second
	for current := int32(1); current < attempt && delay < time.Hour; current++ {
		delay *= 2
	}
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
