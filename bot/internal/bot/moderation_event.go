package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

const moderationEventTTL = 48 * time.Hour

func moderationEventKey(msg *tele.Message, scope, rule, action string) string {
	if msg == nil || msg.Chat == nil || msg.ID <= 0 {
		return ""
	}
	raw := fmt.Sprintf("%d:%d:%d:%s:%s:%s", msg.Chat.ID, messageSenderID(msg), msg.ID, strings.TrimSpace(scope), strings.TrimSpace(rule), strings.TrimSpace(action))
	digest := sha256.Sum256([]byte(raw))
	return "clawguard:moderation-event:" + hex.EncodeToString(digest[:16])
}

func (s *Service) moderationEventState(ctx context.Context, key string) string {
	if s.redis == nil || key == "" {
		return ""
	}
	state, err := s.redis.Get(ctx, key).Result()
	if err == nil {
		return state
	}
	if err != redis.Nil && s.logger != nil {
		s.logger.Warn("load moderation event state failed", zap.Error(err), zap.String("key", key))
	}
	return ""
}

func (s *Service) setModerationEventState(ctx context.Context, key, state string) {
	if s.redis == nil || key == "" || state == "" {
		return
	}
	if err := s.redis.Set(ctx, key, state, moderationEventTTL).Err(); err != nil && s.logger != nil {
		s.logger.Warn("store moderation event state failed", zap.Error(err), zap.String("key", key), zap.String("state", state))
	}
}
