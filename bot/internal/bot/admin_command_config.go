package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/store"
)

var errConfigGroupOutOfScope = errors.New("config group out of scope")

func (s *Service) handleConfigCommand(c tele.Context) error {
	msg := c.Message()
	chat := c.Chat()
	sender := c.Sender()
	if msg == nil || chat == nil || sender == nil {
		return nil
	}

	ctx := context.Background()
	admin, scopeChatID, err := s.authorizeConfigCommand(ctx, chat, sender.ID)
	if errors.Is(err, errConfigGroupOutOfScope) {
		return c.Send("该群不在你的管理范围内", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	if err != nil {
		return err
	}
	if admin == nil {
		return c.Send("仅管理员可用", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	webURL := s.cfg.AdminWebBaseURL()
	miniAppURL := webURL + "/miniapp"
	if scopeChatID != 0 {
		miniAppURL += "?next=" + "/groups/" + strconv.FormatInt(scopeChatID, 10)
	}

	markup := &tele.ReplyMarkup{}
	markup.Inline(
		markup.Row(markup.WebApp("🔧 在 Telegram 内打开管理面板", &tele.WebApp{URL: miniAppURL})),
	)
	text := fmt.Sprintf(
		"<b>ClawGuard 管理面板入口</b>\n\n• 手机 / 桌面客户端：直接点击下方按钮打开（基于 Telegram 官方公钥验证）\n• 独立浏览器大屏：访问 <code>%s</code> 并在本聊天内确认 6 位核对码即可登录",
		htmlEscape(webURL),
	)
	sendOptions := &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	}

	if msg.Private() {
		_, err = s.bot.Send(sender, text, sendOptions)
		return err
	}

	if _, err = s.bot.Send(sender, text, sendOptions); err != nil {
		return c.Send("无法私聊发送配置入口，请先私聊机器人后再试", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	return c.Send("已私聊发送管理面板入口 👉", &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (s *Service) authorizeConfigCommand(ctx context.Context, chat *tele.Chat, telegramID int64) (*store.Admin, int64, error) {
	admin, err := s.queries.GetAdminByTelegramID(ctx, telegramID)
	if err != nil && err != pgx.ErrNoRows {
		return nil, 0, err
	}

	if chat != nil && (chat.Type == tele.ChatGroup || chat.Type == tele.ChatSuperGroup) {
		authorized, authErr := s.IsAuthorizedGroup(ctx, chat.ID)
		if authErr != nil {
			return nil, 0, authErr
		}
		if !authorized {
			return nil, 0, nil
		}

		isAdmin, adminErr := s.isChatAdmin(ctx, chat.ID, telegramID)
		if adminErr != nil {
			return nil, 0, adminErr
		}
		if !isAdmin || err == pgx.ErrNoRows {
			return nil, 0, nil
		}
		if !botAdminCanAccessChat(admin, chat.ID) {
			return nil, 0, errConfigGroupOutOfScope
		}
		return &admin, chat.ID, nil
	}

	if err == pgx.ErrNoRows {
		return nil, 0, nil
	}
	return &admin, 0, nil
}

func scopeChatIDPtr(chatID int64) *int64 {
	if chatID == 0 {
		return nil
	}
	return &chatID
}
