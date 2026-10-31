package bot

import (
	"context"
	"errors"
	"fmt"

	"github.com/openclaw/clawguard/internal/store"
)

var errCleanupManualReview = errors.New("Telegram 清理结果不明，请管理员人工核查")
var errCleanupNotDispatched = errors.New("cleanup dispatch not sent")

func membershipCleanupKey(kind string, chatID, userID int64, generation string) string {
	if generation == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d:%d:%s", kind, chatID, userID, generation)
}

// guardedCleanup persists a dispatch before contacting Telegram. A surviving
// dispatch is never replayed after a process crash, cancellation, or lease expiry.
// A confirmed result can be used to resume database settlement without Telegram.
func (s *Service) guardedCleanup(ctx context.Context, key string, chatID, userID int64, action string, execute func() (string, error)) (string, error) {
	if s.queries == nil || key == "" {
		return "", fmt.Errorf("cleanup dispatch storage unavailable: %w", errCleanupManualReview)
	}
	claimed, result, err := s.queries.BeginCleanupDispatch(ctx, key, chatID, userID, action)
	if err != nil {
		if errors.Is(err, store.ErrCleanupDispatchConflict) {
			return "", errors.Join(err, errCleanupManualReview)
		}
		return "", errors.Join(err, errCleanupNotDispatched)
	}
	if !claimed {
		if result != "" {
			return result, nil
		}
		return "", errCleanupManualReview
	}
	result, err = execute()
	var partial *actionAppliedError
	if errors.As(err, &partial) {
		if e := s.queries.CompleteCleanupDispatch(ctx, key, "partial_kick"); e != nil {
			return "", errors.Join(err, e, errCleanupManualReview)
		}
		return "partial_kick", nil
	}
	if err != nil {
		if isExplicitTelegramRejection(err) {
			if isTerminalTelegramCleanupError(err) {
				result = "already_absent"
			} else {
				if e := s.queries.ClearCleanupDispatch(ctx, key); e != nil {
					return "", errors.Join(err, e, errCleanupManualReview)
				}
				return "", err
			}
		} else {
			return "", errors.Join(err, errCleanupManualReview)
		}
	}
	if result == "" {
		return "", errCleanupManualReview
	}
	if err := s.queries.CompleteCleanupDispatch(ctx, key, result); err != nil {
		return "", errors.Join(err, errCleanupManualReview)
	}
	return result, nil
}
