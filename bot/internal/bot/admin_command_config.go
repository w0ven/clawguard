package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/store"
)

const magicLoginTTL = 60 * time.Minute

type magicLinkPayload struct {
	TelegramID  int64  `json:"telegram_id"`
	ScopeChatID int64  `json:"scope_chat_id"`
	CreatedAt   string `json:"created_at"`
}

func (s *Service) handleConfigCommand(c tele.Context) error {
	msg := c.Message()
	chat := c.Chat()
	sender := c.Sender()
	if msg == nil || chat == nil || sender == nil {
		return nil
	}

	ctx := context.Background()
	admin, scopeChatID, err := s.authorizeConfigCommand(ctx, chat, sender.ID)
	if err != nil {
		return err
	}
	if admin == nil {
		return c.Send("仅管理员可用", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	token, err := generateMagicToken()
	if err != nil {
		s.logger.Warn("generate magic token failed", zap.Error(err), zap.Int64("telegram_id", sender.ID))
		return c.Send("生成登录链接失败", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	payload := magicLinkPayload{
		TelegramID:  sender.ID,
		ScopeChatID: scopeChatID,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return c.Send("生成登录链接失败", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	key := "clawguard:magic:" + token
	if err := s.redis.Set(ctx, key, raw, magicLoginTTL).Err(); err != nil {
		s.logger.Warn("store magic login token failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("telegram_id", sender.ID))
		return c.Send("生成登录链接失败", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	diff := "expires in 60min"
	if _, err := s.queries.InsertAuditEntry(ctx, store.InsertAuditEntryParams{
		Scope:   "magic_link",
		ChatID:  scopeChatIDPtr(scopeChatID),
		AdminID: admin.ID,
		Action:  "issue_magic_token",
		Before:  []byte("null"),
		After:   mustJSONBytes(payload),
		Diff:    &diff,
	}); err != nil {
		s.logger.Warn("write magic token issue audit failed", zap.Error(err), zap.Int64("admin_id", admin.ID))
	}

	magicURL := s.cfg.AdminWebBaseURL() + "/auth/magic?token=" + token
	text := fmt.Sprintf("一次性登录链接（60 分钟内有效）：\n\n<code>%s</code>", htmlEscape(magicURL))
	sendOptions := &tele.SendOptions{
		ParseMode: tele.ModeHTML,
		ReplyMarkup: &tele.ReplyMarkup{
			InlineKeyboard: [][]tele.InlineButton{{
				{Text: "🔧 打开配置面板", URL: magicURL},
			}},
		},
	}

	if msg.Private() {
		_, err = s.bot.Send(sender, text, sendOptions)
		return err
	}

	if _, err = s.bot.Send(sender, text, sendOptions); err != nil {
		return c.Send("无法私聊发送配置入口，请先私聊机器人后再试", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	return c.Send("已私聊发送配置入口链接 👉", &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (s *Service) authorizeConfigCommand(ctx context.Context, chat *tele.Chat, telegramID int64) (*store.Admin, int64, error) {
	admin, err := s.queries.GetAdminByTelegramID(ctx, telegramID)
	if err != nil && err != pgx.ErrNoRows {
		return nil, 0, err
	}

	if chat != nil && (chat.Type == tele.ChatGroup || chat.Type == tele.ChatSuperGroup) {
		isAdmin, adminErr := s.isChatAdmin(ctx, chat.ID, telegramID)
		if adminErr != nil {
			return nil, 0, adminErr
		}
		if !isAdmin || err == pgx.ErrNoRows {
			return nil, 0, nil
		}
		return &admin, chat.ID, nil
	}

	if err == pgx.ErrNoRows {
		return nil, 0, nil
	}
	return &admin, 0, nil
}

func generateMagicToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func scopeChatIDPtr(chatID int64) *int64 {
	if chatID == 0 {
		return nil
	}
	return &chatID
}
