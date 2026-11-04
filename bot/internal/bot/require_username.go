package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

const requireUsernameRule = "filter_require_username"

var beijingZone = time.FixedZone("+0800", 8*3600)

func formatRequireUsernameNoticeHTML(user *tele.User, until time.Time) string {
	mention := mentionHTML(user)
	untilStr := until.In(beijingZone).Format("2006-01-02 15:04:05 +0800")
	return fmt.Sprintf("%s 请先设置用户名后才能发言.\n惩罚: 禁言到 - %s", mention, untilStr)
}

func (s *Service) hasActivePendingVerification(ctx context.Context, chatID, userID int64) bool {
	if s == nil || s.queries == nil || chatID == 0 || userID == 0 {
		return false
	}
	_, err := s.queries.GetActivePendingVerification(ctx, store.GetPendingVerificationParams{
		ChatID: chatID,
		UserID: userID,
	})
	return err == nil
}

// deleteMessageWhilePendingVerification silently deletes messages sent in the
// brief window before join verification restriction takes effect, without
// sending conflicting filter notices or overwriting the verification mute.
func (s *Service) deleteMessageWhilePendingVerification(ctx context.Context, msg *tele.Message, isAdmin, actionsPaused bool) bool {
	if s == nil || msg == nil || msg.Chat == nil || msg.Sender == nil || isAdmin {
		return false
	}
	if !s.hasActivePendingVerification(ctx, msg.Chat.ID, msg.Sender.ID) {
		return false
	}
	if !actionsPaused {
		if err := s.deleteMessage(msg); err != nil && !errors.Is(err, errActionsPaused) && s.logger != nil {
			s.logger.Debug("delete message during pending verification failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
		}
	}
	return true
}

func (s *Service) requireUsernameApplicable(trust store.UserTrust, policy config.FilterRequireUsernamePolicy) bool {
	if !policy.Enabled {
		return false
	}
	scope := strings.TrimSpace(strings.ToLower(policy.Scope))
	switch scope {
	case "all":
		return trust.Status != "banned"
	default:
		return isUngraduatedTrustStatus(trust.Status)
	}
}

func (s *Service) handleRequireUsernameCheck(ctx context.Context, msg *tele.Message, trust store.UserTrust, policy config.GuardPolicy, isAdmin, actionsPaused bool) (bool, error) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil || isAdmin {
		return false, nil
	}
	if !policy.Filter.RequireUsername.Enabled {
		return false, nil
	}
	if strings.TrimSpace(msg.Sender.Username) != "" {
		return false, nil
	}
	if isOtherBot(msg.Sender, s.bot) && isBotWhitelisted(msg.Sender, policy.Filter.BotWhitelist) {
		return false, nil
	}
	if s.exemptMessage(ctx, msg) {
		return false, nil
	}
	if trust.Status == "banned" {
		return true, s.enforceExistingBannedMessage(ctx, msg, policy)
	}
	if !s.requireUsernameApplicable(trust, policy.Filter.RequireUsername) {
		return false, nil
	}

	muteSeconds := policy.Filter.RequireUsername.MuteSeconds
	if muteSeconds <= 0 {
		muteSeconds = config.DefaultPolicy.Filter.RequireUsername.MuteSeconds
	}
	if muteSeconds < 60 {
		muteSeconds = 60
	}

	messageText := truncateString(collectMessageContent(msg), 1000)
	if actionsPaused {
		if s.queries != nil {
			_, _ = s.queries.InsertViolation(ctx, store.InsertViolationParams{
				ChatID:      msg.Chat.ID,
				UserID:      msg.Sender.ID,
				Username:    nil,
				Rule:        requireUsernameRule,
				Matched:     stringPtr("missing_username"),
				Action:      "skipped_paused:delete_mute",
				MessageText: stringPtr(messageText),
			})
		}
		return true, nil
	}

	release, err := s.AcquireModerationReview(ctx, msg.Chat.ID, msg.Sender.ID)
	if err != nil {
		return false, err
	}
	defer release()

	// Re-check under the moderation gate in case join verification just created
	// a pending record while this message was waiting for the lock.
	if s.hasActivePendingVerification(ctx, msg.Chat.ID, msg.Sender.ID) {
		_ = s.deleteMessage(msg)
		return true, nil
	}

	eventKey := moderationEventKey(msg, "filter", requireUsernameRule, "delete_mute")
	eventState := s.moderationEventState(ctx, eventKey)
	if eventState == "completed" {
		return true, nil
	}

	if eventState != "message_deleted" && eventState != "executed" {
		if err := s.deleteMessage(msg); err != nil {
			if errors.Is(err, errActionsPaused) {
				if s.queries != nil {
					_, _ = s.queries.InsertViolation(ctx, store.InsertViolationParams{
						ChatID:      msg.Chat.ID,
						UserID:      msg.Sender.ID,
						Username:    nil,
						Rule:        requireUsernameRule,
						Matched:     stringPtr("missing_username"),
						Action:      "skipped_paused:delete_mute",
						MessageText: stringPtr(messageText),
					})
				}
				return true, nil
			}
			s.logger.Warn("delete message for require_username failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
		} else {
			s.setModerationEventState(ctx, eventKey, "message_deleted")
		}
	}

	until := time.Now().Add(time.Duration(muteSeconds) * time.Second)
	skipRestrict := false
	if s.queries != nil {
		// Do not overwrite an active independent AI mute or group ban.
		if safe, safeErr := s.queries.DualCaptchaSafety(ctx, msg.Chat.ID, msg.Sender.ID); safeErr == nil && !safe {
			skipRestrict = true
		}
	}
	if !skipRestrict {
		member := &tele.ChatMember{
			User:            msg.Sender,
			Rights:          tele.NoRights(),
			RestrictedUntil: until.Unix(),
		}
		if err := telegramConfirmedRestrict(s.bot, msg.Chat, member); err != nil {
			s.logger.Warn("restrict user for require_username failed", zap.Error(normalizeTelegramActionError("restrict", err)), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID))
		}
	}

	s.setModerationEventState(ctx, eventKey, "executed")

	if s.queries != nil {
		if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
			ChatID:      msg.Chat.ID,
			UserID:      msg.Sender.ID,
			Username:    nil,
			Rule:        requireUsernameRule,
			Matched:     stringPtr("missing_username"),
			Action:      "delete_mute",
			MessageText: stringPtr(messageText),
		}); err != nil && s.logger != nil {
			s.logger.Warn("insert require_username violation failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID))
		}
	}

	s.setModerationEventState(ctx, eventKey, "completed")
	s.sendRequireUsernamePrompt(ctx, msg.Chat, msg.Sender, until, policy.Filter.RequireUsername.AutoDeleteSeconds)
	return true, nil
}

func (s *Service) requireUsernamePromptKey(chatID, userID int64) string {
	return fmt.Sprintf("clawguard:req_uname:prompt:%d:%d", chatID, userID)
}

func (s *Service) shouldSendRequireUsernamePrompt(ctx context.Context, chatID, userID int64, window time.Duration) bool {
	if window <= 0 {
		window = 10 * time.Minute
	}
	key := s.requireUsernamePromptKey(chatID, userID)
	if s.redis != nil {
		if ok, err := s.redis.SetNX(ctx, key, "1", window).Result(); err == nil {
			return ok
		}
	}
	now := time.Now()
	if raw, loaded := s.requireUsernamePromptUntil.Load(key); loaded {
		if exp, ok := raw.(time.Time); ok && now.Before(exp) {
			return false
		}
	}
	s.requireUsernamePromptUntil.Store(key, now.Add(window))
	return true
}

func (s *Service) clearRequireUsernamePromptCooldown(ctx context.Context, chatID, userID int64) {
	key := s.requireUsernamePromptKey(chatID, userID)
	s.requireUsernamePromptUntil.Delete(key)
	if s.redis != nil {
		_ = s.redis.Del(ctx, key).Err()
	}
}

func (s *Service) sendRequireUsernamePrompt(ctx context.Context, chat *tele.Chat, user *tele.User, until time.Time, autoDeleteSeconds int) {
	if chat == nil || user == nil || s.bot == nil {
		return
	}
	cooldown := time.Until(until)
	if autoDeleteSeconds > 0 && time.Duration(autoDeleteSeconds)*time.Second < cooldown {
		cooldown = time.Duration(autoDeleteSeconds) * time.Second
	}
	if !s.shouldSendRequireUsernamePrompt(ctx, chat.ID, user.ID, cooldown) {
		return
	}
	unique := s.requireUsernameBtn.Unique
	if unique == "" {
		unique = "req_uname_unmute"
	}
	markup := &tele.ReplyMarkup{}
	btn := markup.Data("☑️ 自助解禁", unique, strconv.FormatInt(user.ID, 10))
	markup.Inline(markup.Row(btn))

	text := formatRequireUsernameNoticeHTML(user, until)
	opts := &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	}
	sent, err := s.sendThrottled(ctx, chat, text, opts)
	if err != nil {
		s.clearRequireUsernamePromptCooldown(ctx, chat.ID, user.ID)
		if s.logger != nil {
			s.logger.Warn("send require_username notice failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		}
		return
	}
	if autoDeleteSeconds > 0 && sent != nil {
		secs := autoDeleteSeconds
		s.runDelayed(time.Duration(secs)*time.Second, func() {
			s.clearRequireUsernamePromptCooldown(context.Background(), chat.ID, user.ID)
			if delErr := normalizeTelegramActionError("delete", s.deleteDelayedMessage(sent)); delErr != nil && s.logger != nil {
				s.logger.Debug("auto delete require_username notice failed", zap.Error(delErr), zap.Int64("chat_id", chat.ID), zap.Int("message_id", sent.ID))
			}
		})
	}
}

func (s *Service) handleRequireUsernameUnmute(c tele.Context) error {
	callback := c.Callback()
	chat := c.Chat()
	sender := c.Sender()
	if callback == nil || chat == nil || sender == nil {
		return nil
	}

	rawData := strings.TrimSpace(c.Data())
	targetUserID, err := strconv.ParseInt(rawData, 10, 64)
	if err != nil || targetUserID <= 0 {
		return c.Respond(&tele.CallbackResponse{Text: "解禁参数无效", ShowAlert: true})
	}
	if sender.ID != targetUserID {
		return c.Respond(&tele.CallbackResponse{Text: "仅限本人点击自助解禁", ShowAlert: true})
	}

	ctx := context.Background()
	state, err := s.GetSystemState(ctx)
	if err != nil || state.Frozen || state.ActionsPaused {
		return c.Respond(&tele.CallbackResponse{Text: "当前系统暂停处理，请稍后重试", ShowAlert: true})
	}

	username := strings.TrimPrefix(strings.TrimSpace(sender.Username), "@")
	if username == "" && s.bot != nil {
		if member, memberErr := s.bot.ChatMemberOf(chat, sender); memberErr == nil && member != nil && member.User != nil {
			username = strings.TrimPrefix(strings.TrimSpace(member.User.Username), "@")
		}
	}
	if username == "" {
		return c.Respond(&tele.CallbackResponse{
			Text:      "请先在 Telegram 设置中添加 @用户名，设置完成后再点击自助解禁",
			ShowAlert: true,
		})
	}

	release, err := s.AcquireModerationReview(ctx, chat.ID, sender.ID)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: "处理正在进行，请稍后重试", ShowAlert: true})
	}
	defer release()

	if s.queries != nil {
		if _, pendingErr := s.queries.GetActivePendingVerification(ctx, store.GetPendingVerificationParams{
			ChatID: chat.ID,
			UserID: sender.ID,
		}); pendingErr == nil {
			return c.Respond(&tele.CallbackResponse{Text: "您还有未完成的入群验证，请先完成入群验证", ShowAlert: true})
		} else if !errors.Is(pendingErr, pgx.ErrNoRows) {
			return c.Respond(&tele.CallbackResponse{Text: "暂时无法核查状态，请稍后重试", ShowAlert: true})
		}

		safe, safeErr := s.queries.DualCaptchaSafety(ctx, chat.ID, sender.ID)
		if safeErr != nil {
			return c.Respond(&tele.CallbackResponse{Text: "暂时无法核查状态，请稍后重试", ShowAlert: true})
		}
		if !safe {
			return c.Respond(&tele.CallbackResponse{Text: "您当前存在其他待处理的处罚，无法自助解禁", ShowAlert: true})
		}
		if _, banned, exErr := s.queries.GroupExemptionStatus(ctx, chat.ID, sender.ID); exErr != nil {
			return c.Respond(&tele.CallbackResponse{Text: "暂时无法核查状态，请稍后重试", ShowAlert: true})
		} else if banned {
			return c.Respond(&tele.CallbackResponse{Text: "您当前处于封禁状态，无法自助解禁", ShowAlert: true})
		}
	}

	policy, err := s.LoadGuardPolicy(ctx, chat.ID)
	if err != nil {
		policy = config.DefaultPolicy
	}

	rights := normalMemberRights()
	if s.queries != nil {
		trust, trustErr := s.queries.GetUserTrust(ctx, chat.ID, sender.ID)
		if trustErr != nil && !errors.Is(trustErr, pgx.ErrNoRows) {
			return c.Respond(&tele.CallbackResponse{Text: "暂时无法核查状态，请稍后重试", ShowAlert: true})
		}
		if trustErr == nil {
			if trust.Status == "banned" {
				return c.Respond(&tele.CallbackResponse{Text: "您当前处于封禁状态，无法自助解禁", ShowAlert: true})
			}
			if ungraduatedPermissionRestrictionEnabled(policy) && isUngraduatedTrustStatus(trust.Status) {
				rights = ungraduatedRestrictedRights(policy)
			}
			_, _ = s.queries.UpsertUserTrust(ctx, store.UpsertUserTrustParams{
				ChatID:          chat.ID,
				UserID:          sender.ID,
				Username:        stringPtr(username),
				FirstName:       userFieldPtr(sender.FirstName),
				LastName:        userFieldPtr(sender.LastName),
				JoinedAt:        trust.JoinedAt,
				Status:          trust.Status,
				Score:           trust.Score,
				MessagesChecked: trust.MessagesChecked,
				MessagesClean:   trust.MessagesClean,
				GraduatedAt:     trust.GraduatedAt,
				BannedAt:        trust.BannedAt,
				BannedReason:    trust.BannedReason,
				Notes:           trust.Notes,
				IsBot:           trust.IsBot,
			})
		}
	}

	member := &tele.ChatMember{
		User:   sender,
		Rights: rights,
	}
	if err := telegramConfirmedRestrict(s.bot, chat, member); err != nil {
		if s.logger != nil {
			s.logger.Warn("require_username self unmute restrict failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		}
		return c.Respond(&tele.CallbackResponse{Text: "解除禁言失败，请稍后重试", ShowAlert: true})
	}

	if callback.Message != nil {
		_ = normalizeTelegramActionError("delete", s.deleteDelayedMessage(callback.Message))
	}
	s.clearRequireUsernamePromptCooldown(ctx, chat.ID, sender.ID)

	if s.logger != nil {
		s.logger.Info("user self-unmuted after setting username",
			zap.Int64("chat_id", chat.ID),
			zap.Int64("user_id", sender.ID),
			zap.String("username", username),
		)
	}
	return c.Respond(&tele.CallbackResponse{Text: "✅ 已核实用户名，禁言已解除", ShowAlert: true})
}
