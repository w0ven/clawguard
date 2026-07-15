package bot

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	operationsBackupKey          = "clawguard:ops:last-backup-at"
	operationsWorkerKeyPrefix    = "clawguard:ops:worker:"
	operationsWorkerHeartbeatTTL = 7 * 24 * time.Hour
)

type OperationsRuntimeStatus struct {
	JoinCleanupDeadLetters       *int64  `json:"join_cleanup_dead_letters"`
	BackupLastAt                 *string `json:"backup_last_at"`
	BackupSecondsAgo             *int64  `json:"backup_seconds_ago"`
	VerificationWorkerSecondsAgo *int64  `json:"verification_worker_seconds_ago"`
	JoinRecoveryWorkerSecondsAgo *int64  `json:"join_recovery_worker_seconds_ago"`
}

func (s *Service) RecordWorkerSuccess(ctx context.Context, worker string) {
	if s == nil || s.redis == nil || worker == "" {
		return
	}
	key := operationsWorkerKeyPrefix + worker + ":last-success-at"
	if err := s.redis.Set(ctx, key, time.Now().UTC().Format(time.RFC3339), operationsWorkerHeartbeatTTL).Err(); err != nil && s.logger != nil {
		s.logger.Warn("record worker heartbeat failed", zap.String("worker", worker), zap.Error(err))
	}
}

func (s *Service) OperationsRuntimeStatus(ctx context.Context) OperationsRuntimeStatus {
	var status OperationsRuntimeStatus
	if s == nil || s.redis == nil {
		return status
	}

	if count, err := s.redis.ZCard(ctx, joinProtectionCleanupDeadKey).Result(); err == nil {
		status.JoinCleanupDeadLetters = &count
	}
	status.BackupLastAt, status.BackupSecondsAgo = s.operationsTimestamp(ctx, operationsBackupKey)
	_, status.VerificationWorkerSecondsAgo = s.operationsTimestamp(ctx, operationsWorkerKeyPrefix+"verification-expiry:last-success-at")
	_, status.JoinRecoveryWorkerSecondsAgo = s.operationsTimestamp(ctx, operationsWorkerKeyPrefix+"join-protection-recovery:last-success-at")
	return status
}

func (s *Service) operationsTimestamp(ctx context.Context, key string) (*string, *int64) {
	raw, err := s.redis.Get(ctx, key).Result()
	if err != nil {
		if err != redis.Nil && s.logger != nil {
			s.logger.Warn("load operations timestamp failed", zap.String("key", key), zap.Error(err))
		}
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return &raw, nil
	}
	seconds := int64(max(0, time.Since(parsed).Seconds()))
	return &raw, &seconds
}
