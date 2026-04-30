package bot

import (
	"context"
	"strings"

	"github.com/redis/go-redis/v9"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
)

func (s *Service) Queries() *store.Queries {
	return s.queries
}

func (s *Service) TelegramBot() *tele.Bot {
	return s.bot
}

func (s *Service) SendLimiter() *SendLimiter {
	return s.sendLimiter
}

func (s *Service) BanChatUser(_ context.Context, chatID, userID int64) error {
	return s.banUser(&tele.Chat{ID: chatID}, &tele.User{ID: userID})
}

func (s *Service) UnbanChatUser(ctx context.Context, chatID, userID int64) error {
	_, err := s.SafeUnbanChatUser(ctx, chatID, userID)
	return err
}

func (s *Service) SafeUnbanChatUser(_ context.Context, chatID, userID int64) (string, error) {
	// Pass only_if_banned=true so Telegram treats this as a pure unban/no-op.
	// Without it, unbanChatMember can also lift a regular member's kick restriction,
	// which makes /unban unsafe when the target is not actually banned.
	err := s.bot.Unban(&tele.Chat{ID: chatID}, &tele.User{ID: userID}, true)
	if err == nil {
		return "", nil
	}
	if isNoUnbanNeededTelegramError(err) {
		return "Telegram 没有可解除的封禁：" + err.Error(), nil
	}
	return "", err
}

func isNoUnbanNeededTelegramError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	benignParts := []string{
		"not banned",
		"not kicked",
		"user not found",
		"member not found",
		"participant_id_invalid",
	}
	for _, part := range benignParts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}

func (s *Service) UnmuteChatUser(_ context.Context, chatID, userID int64) error {
	member := &tele.ChatMember{
		User:   &tele.User{ID: userID},
		Rights: tele.NoRestrictions(),
	}
	return s.bot.Restrict(&tele.Chat{ID: chatID}, member)
}

func (s *Service) AIProviders() ai.ProviderRegistry {
	return s.aiProviders
}

func (s *Service) AIModels() ai.ModelRegistry {
	return s.aiModels
}

func (s *Service) AIModerator() *ai.Moderator {
	return s.aiModerator
}

func (s *Service) Redis() redis.Cmdable {
	return s.redis
}

func (s *Service) LeaveChat(chatID int64) error {
	if chatID == 0 {
		return nil
	}
	return s.bot.Leave(&tele.Chat{ID: chatID})
}
