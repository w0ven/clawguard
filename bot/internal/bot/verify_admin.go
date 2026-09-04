package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

func (s *Service) existingVerificationMessageID(ctx context.Context, chatID, userID int64) int {
	if s == nil || s.queries == nil || chatID == 0 || userID == 0 {
		return 0
	}
	pending, err := s.queries.GetPendingVerification(ctx, store.GetPendingVerificationParams{
		ChatID: chatID,
		UserID: userID,
	})
	if err != nil || pending.JoinMessageID == nil || *pending.JoinMessageID <= 0 {
		return 0
	}
	return int(*pending.JoinMessageID)
}

func (s *Service) verificationMessageRef(chat *tele.Chat, messageID int) *tele.Message {
	if chat == nil || messageID <= 0 {
		return nil
	}
	return &tele.Message{ID: messageID, Chat: chat}
}

func (s *Service) adminVerificationRow(markup *tele.ReplyMarkup, userID int64) tele.Row {
	if markup == nil {
		markup = &tele.ReplyMarkup{}
	}
	approve := markup.Data("✅ 同意入群", s.verifyAdminBtn.Unique, formatVerifyCallbackData(userID, "approve"))
	reject := markup.Data("❌ 拒绝入群", s.verifyAdminBtn.Unique, formatVerifyCallbackData(userID, "reject"))
	return markup.Row(approve, reject)
}

func (s *Service) sendOrEditVerificationText(
	ctx context.Context,
	chat *tele.Chat,
	existingID int,
	html string,
	markup *tele.ReplyMarkup,
) (*tele.Message, error) {
	opts := &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	}
	if existingID > 0 {
		msg := s.verificationMessageRef(chat, existingID)
		if _, err := s.bot.Edit(msg, html, opts); err == nil {
			return msg, nil
		} else if s.logger != nil {
			s.logger.Warn("edit verification text failed, sending a new message",
				zap.Error(err),
				zap.Int64("chat_id", chat.ID),
				zap.Int("message_id", existingID),
			)
		}
		s.deleteVerificationMessage(chat, int64Ptr(int64(existingID)))
	}
	return s.sendThrottled(ctx, chat, html, opts)
}

func (s *Service) sendOrEditVerificationPhoto(
	chat *tele.Chat,
	existingID int,
	photo *tele.Photo,
	markup *tele.ReplyMarkup,
) (*tele.Message, error) {
	opts := &tele.SendOptions{
		ParseMode:   tele.ModeHTML,
		ReplyMarkup: markup,
	}
	if existingID > 0 {
		msg := s.verificationMessageRef(chat, existingID)
		if _, err := s.bot.EditCaption(msg, photo.Caption, opts); err == nil {
			if markup != nil {
				if _, markupErr := s.bot.EditReplyMarkup(msg, markup); markupErr != nil && s.logger != nil {
					s.logger.Warn("edit verification photo markup failed", zap.Error(markupErr), zap.Int64("chat_id", chat.ID), zap.Int("message_id", existingID))
				}
			}
			return msg, nil
		} else if s.logger != nil {
			s.logger.Warn("edit verification photo failed, sending a new message",
				zap.Error(err),
				zap.Int64("chat_id", chat.ID),
				zap.Int("message_id", existingID),
			)
		}
		s.deleteVerificationMessage(chat, int64Ptr(int64(existingID)))
	}
	return s.bot.Send(chat, photo, opts)
}

func (s *Service) finalizeVerificationPrompt(chat *tele.Chat, pending store.PendingVerification, html string) {
	if chat == nil || pending.JoinMessageID == nil || *pending.JoinMessageID <= 0 {
		return
	}
	msg := s.verificationMessageRef(chat, int(*pending.JoinMessageID))
	opts := &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           &tele.ReplyMarkup{},
	}
	var err error
	if pending.Method == "math_image" {
		_, err = s.bot.EditCaption(msg, html, opts)
	} else {
		_, err = s.bot.Edit(msg, html, opts)
	}
	if err != nil && s.logger != nil {
		s.logger.Warn("edit verification result failed",
			zap.Error(err),
			zap.Int64("chat_id", chat.ID),
			zap.Int64("message_id", *pending.JoinMessageID),
		)
	}
}

func verificationResultHTML(user *tele.User, kind string) string {
	mention := mentionHTML(user)
	switch kind {
	case "passed":
		return fmt.Sprintf("✅ %s 已通过入群验证", mention)
	case "admin_pass":
		return fmt.Sprintf("✅ 管理员已同意 %s 入群", mention)
	case "failed":
		return fmt.Sprintf("❌ %s 验证失败，已移出群组", mention)
	case "admin_reject":
		return fmt.Sprintf("❌ 管理员已拒绝 %s 入群", mention)
	case "timeout":
		return fmt.Sprintf("⌛ %s 验证超时，已移出群组", mention)
	default:
		return fmt.Sprintf("ℹ️ %s 的入群验证已结束", mention)
	}
}

func verificationFailKind(rule string) string {
	switch strings.TrimSpace(rule) {
	case "verify_admin_reject":
		return "admin_reject"
	case "verify_timeout":
		return "timeout"
	default:
		return "failed"
	}
}

func (s *Service) handleVerifyAdmin(c tele.Context) error {
	callback := c.Callback()
	chat := c.Chat()
	sender := c.Sender()
	if callback == nil || chat == nil || sender == nil {
		return nil
	}

	payload, err := parseVerifyCallbackData(c.Data())
	if err != nil {
		s.logger.Warn("invalid admin verification callback payload", zap.Error(err))
		return c.Respond(&tele.CallbackResponse{Text: "验证参数无效", ShowAlert: true})
	}
	action := strings.ToLower(strings.TrimSpace(payload.Value))
	if action != "approve" && action != "reject" {
		return c.Respond(&tele.CallbackResponse{Text: "验证参数无效", ShowAlert: true})
	}

	isAdmin, err := s.isChatAdmin(context.Background(), chat.ID, sender.ID)
	if err != nil {
		s.logger.Warn("check chat admin for verification failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "暂时无法确认管理员身份", ShowAlert: true})
	}
	if !isAdmin {
		return c.Respond(&tele.CallbackResponse{Text: "只有群管理员可以操作", ShowAlert: true})
	}

	pending, err := s.queries.GetActivePendingVerification(context.Background(), store.GetPendingVerificationParams{
		ChatID: chat.ID,
		UserID: payload.UserID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.Respond(&tele.CallbackResponse{Text: "验证已失效或已处理", ShowAlert: true})
		}
		s.logger.Error("load pending verification for admin action", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", payload.UserID))
		return c.Respond(&tele.CallbackResponse{Text: "暂时无法处理，请稍后重试", ShowAlert: true})
	}

	user := &tele.User{
		ID:        pending.UserID,
		Username:  derefString(pending.Username),
		FirstName: derefString(pending.FirstName),
	}
	if action == "approve" {
		if err := s.completeVerification(context.Background(), chat, user, pending, "admin_pass"); err != nil {
			s.logger.Error("admin approve verification failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
			return c.Respond(&tele.CallbackResponse{Text: "同意入群失败，请稍后重试", ShowAlert: true})
		}
		s.logger.Info("admin approved join verification", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID), zap.Int64("admin_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "已同意入群", ShowAlert: false})
	}

	policy, polErr := s.LoadGuardPolicy(context.Background(), chat.ID)
	if polErr != nil {
		s.logger.Warn("load policy for admin reject", zap.Error(polErr), zap.Int64("chat_id", chat.ID))
		policy = config.DefaultPolicy
	}
	if err := s.failVerificationImmediately(context.Background(), chat, user, pending, policy.Verify.FailAction, "verify_admin_reject"); err != nil {
		s.logger.Error("admin reject verification failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return c.Respond(&tele.CallbackResponse{Text: "拒绝入群失败，请稍后重试", ShowAlert: true})
	}
	s.sendActionFeedback(chat, nil, policy.Feedback.VerifyFail, map[string]string{
		"user":         feedbackUserLabel(user, policy.Feedback.VerifyFail.ParseMode),
		"user_mention": feedbackUserMention(user, policy.Feedback.VerifyFail.ParseMode),
		"reason":       "管理员拒绝入群",
	})
	s.logger.Info("admin rejected join verification", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID), zap.Int64("admin_id", sender.ID))
	return c.Respond(&tele.CallbackResponse{Text: "已拒绝入群", ShowAlert: false})
}
