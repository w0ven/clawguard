package bot

import (
	"context"

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

func (s *Service) UnbanChatUser(_ context.Context, chatID, userID int64) error {
	return s.bot.Unban(&tele.Chat{ID: chatID}, &tele.User{ID: userID})
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
