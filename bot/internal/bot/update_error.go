package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

const (
	// nonRetryableAlertCooldown throttles operator alerts so a repeatedly
	// broken template cannot turn into an alert storm.
	nonRetryableAlertCooldown = 30 * time.Minute
	nonRetryableAlertTimeout  = 5 * time.Second
)

// IsNonRetryableUpdateError reports whether an update failed for a reason that
// re-delivering the exact same update cannot fix.
//
// Telegram retries a webhook update whenever the endpoint answers with a 5xx.
// For deterministic client errors (a malformed template, a message that no
// longer exists, a chat the bot was removed from) that retry can never succeed,
// so the update just blocks the queue and starves every later update behind it.
// Those failures must be acknowledged and reported instead.
//
// 429 is deliberately excluded: rate limiting is transient and retrying is the
// correct response.
func IsNonRetryableUpdateError(err error) bool {
	if err == nil {
		return false
	}
	if _, isFlood := floodRetryAfter(err); isFlood {
		return false
	}
	var teleErr *tele.Error
	if errors.As(err, &teleErr) && teleErr != nil {
		return teleErr.Code >= 400 && teleErr.Code < 500 && teleErr.Code != 429
	}
	return false
}

// ReportNonRetryableUpdate logs and alerts operators about an update that was
// dropped on purpose. Alerts are rate limited per reason so one broken
// configuration does not flood the owners' DMs.
func (s *Service) ReportNonRetryableUpdate(updateID int, err error) {
	if err == nil {
		return
	}
	s.logger.Error("drop non-retryable telegram update",
		zap.Int("update_id", updateID),
		zap.Error(err),
	)

	if !s.shouldSendNonRetryableAlert(err) {
		return
	}

	message := fmt.Sprintf(
		"⚠️ ClawGuard 丢弃了一条无法重试的 Telegram 更新（update_id=%d）。\n原因：%s\n\n该更新已被确认（ACK），以免 Telegram 反复重投堵塞队列。常见原因是关键词回复/欢迎语模板的 MarkdownV2 语法错误，请检查相关模板。",
		updateID,
		htmlEscape(err.Error()),
	)
	s.sendNonRetryableAlertToOwners(message)
}

// shouldSendNonRetryableAlert applies a best-effort cooldown keyed by the error
// text. When Redis is unavailable it fails open so alerts are never lost.
func (s *Service) shouldSendNonRetryableAlert(err error) bool {
	if s.redis == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), nonRetryableAlertTimeout)
	defer cancel()

	key := "update:nonretryable:alert:" + alertFingerprint(err)
	ok, redisErr := s.redis.SetNX(ctx, key, "1", nonRetryableAlertCooldown).Result()
	if redisErr != nil {
		s.logger.Warn("non-retryable update alert cooldown unavailable, alerting anyway", zap.Error(redisErr))
		return true
	}
	return ok
}

// alertFingerprint collapses an error into a stable, bounded cooldown key.
func alertFingerprint(err error) string {
	text := err.Error()
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	const maxFingerprint = 160
	if len(text) > maxFingerprint {
		text = text[:maxFingerprint]
	}
	return text
}

func (s *Service) sendNonRetryableAlertToOwners(message string) {
	recipients := map[int64]struct{}{}
	if s.queries != nil {
		ctx, cancel := context.WithTimeout(context.Background(), nonRetryableAlertTimeout)
		defer cancel()
		if owner, err := s.queries.GetFirstOwnerAdmin(ctx); err == nil && owner.TelegramID != 0 {
			recipients[owner.TelegramID] = struct{}{}
		}
	}
	for _, id := range s.cfg.AdminTelegramIDs {
		if id != 0 {
			recipients[id] = struct{}{}
		}
	}
	for id := range recipients {
		if err := s.SendHTMLPrivateMessage(id, message); err != nil {
			s.logger.Warn("send non-retryable update alert failed", zap.Error(err), zap.Int64("telegram_id", id))
		}
	}
}
