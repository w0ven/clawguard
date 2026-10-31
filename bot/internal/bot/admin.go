package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/adkiller"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/redact"
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

// BanChatUserNoLock requires the caller to hold the subject moderation gate.
func (s *Service) BanChatUserNoLock(_ context.Context, chatID, userID int64) error {
	return s.banUserWithOwnershipLocked(&tele.Chat{ID: chatID}, &tele.User{ID: userID}, false)
}

func (s *Service) UnbanChatUser(ctx context.Context, chatID, userID int64) error {
	_, err := s.SafeUnbanChatUser(ctx, chatID, userID)
	return err
}

func (s *Service) SafeUnbanChatUser(ctx context.Context, chatID, userID int64) (string, error) {
	release, err := s.AcquireModerationReview(ctx, chatID, userID)
	if err != nil {
		return "", err
	}
	defer release()
	return s.SafeUnbanChatUserNoLock(ctx, chatID, userID)
}

// SafeUnbanChatUserNoLock requires the caller to hold the subject gate.
func (s *Service) SafeUnbanChatUserNoLock(_ context.Context, chatID, userID int64) (string, error) {
	// Pass only_if_banned=true so Telegram treats this as a pure unban/no-op.
	// Without it, unbanChatMember can also lift a regular member's kick restriction,
	// which makes /unban unsafe when the target is not actually banned.
	err := s.bot.Unban(&tele.Chat{ID: chatID}, &tele.User{ID: userID}, true)
	if err == nil {
		return "", nil
	}
	if isNoUnbanNeededTelegramError(err) {
		return "Telegram 没有可解除的封禁：" + redact.ErrorString(err), nil
	}
	return "", err
}

// ReviewUnbanNoLock is only for a caller already holding the subject gate.
// Telegram's response must explicitly confirm the boolean result; telebot's
// Raw otherwise accepts malformed JSON as success.
func (s *Service) ReviewUnbanNoLock(_ context.Context, chatID, userID int64) error {
	raw, err := s.bot.Raw("unbanChatMember", map[string]string{"chat_id": strconv.FormatInt(chatID, 10), "user_id": strconv.FormatInt(userID, 10), "only_if_banned": "true"})
	if err != nil {
		if isNoUnbanNeededTelegramError(err) {
			return errors.New("telegram state does not confirm original ban")
		}
		return err
	}
	return confirmTelegramBoolean(raw)
}
func confirmTelegramBoolean(raw []byte) error {
	var response struct {
		OK     bool  `json:"ok"`
		Result *bool `json:"result"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.OK || response.Result == nil || !*response.Result {
		return errors.New("telegram response not confirmed")
	}
	return nil
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

func (s *Service) UnmuteChatUser(ctx context.Context, chatID, userID int64) error {
	release, err := s.AcquireModerationReview(ctx, chatID, userID)
	if err != nil {
		return err
	}
	defer release()
	return s.UnmuteChatUserNoLock(ctx, chatID, userID)
}

// UnmuteChatUserNoLock requires the caller to hold the subject gate.
func (s *Service) UnmuteChatUserNoLock(_ context.Context, chatID, userID int64) error {
	member := &tele.ChatMember{
		User:   &tele.User{ID: userID},
		Rights: tele.NoRestrictions(),
	}
	return s.bot.Restrict(&tele.Chat{ID: chatID}, member)
}

// ReviewUnmuteNoLock is only for a caller already holding the subject gate.
func (s *Service) ReviewUnmuteNoLock(_ context.Context, chatID, userID int64) error {
	raw, err := s.bot.Raw("restrictChatMember", map[string]any{"chat_id": strconv.FormatInt(chatID, 10), "user_id": strconv.FormatInt(userID, 10), "until_date": "0", "permissions": tele.NoRestrictions()})
	if err != nil {
		return err
	}
	return confirmTelegramBoolean(raw)
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

func (s *Service) AdKiller() *adkiller.Client {
	return s.adkiller
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
