package bot

import (
	"context"

	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

func newUserMediaRestrictionEnabled(policy config.GuardPolicy) bool {
	return policy.Filter.NewUser.Enabled && policy.Filter.NewUser.NoMedia
}

func ungraduatedMediaRestrictedRights() tele.Rights {
	rights := tele.NoRestrictions()
	rights.CanSendMedia = false
	rights.CanSendAudios = false
	rights.CanSendDocuments = false
	rights.CanSendPhotos = false
	rights.CanSendVideos = false
	rights.CanSendVideoNotes = false
	rights.CanSendVoiceNotes = false
	rights.CanSendPolls = false
	rights.CanSendOther = false
	rights.CanAddPreviews = false
	return rights
}

func (s *Service) applyUngraduatedMediaRestriction(chat *tele.Chat, user *tele.User, policy config.GuardPolicy, trust store.UserTrust, reason string) {
	if !newUserMediaRestrictionEnabled(policy) || !isUngraduatedTrustStatus(trust.Status) {
		return
	}
	if err := s.restrictUngraduatedMedia(chat, user, reason); err != nil {
		s.logTelegramPermissionWarning("restrict ungraduated media permissions failed", err, chat, user, reason)
	}
}

func (s *Service) restrictUngraduatedMedia(chat *tele.Chat, user *tele.User, reason string) error {
	if s == nil || s.bot == nil || chat == nil || user == nil {
		return nil
	}
	member := tele.ChatMember{
		User:   user,
		Rights: ungraduatedMediaRestrictedRights(),
	}
	if err := s.bot.Restrict(chat, &member); err != nil {
		return err
	}
	s.logTelegramPermissionInfo("restricted ungraduated media permissions", chat, user, reason)
	return nil
}

func (s *Service) applyVerificationPassPermissions(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) error {
	if s == nil || chat == nil || user == nil {
		return nil
	}
	if newUserMediaRestrictionEnabled(policy) && s.queries != nil {
		trust, err := s.queries.GetUserTrust(ctx, chat.ID, user.ID)
		if err != nil {
			s.logTelegramPermissionWarning("load user trust before verification permission sync failed", err, chat, user, "verification_pass")
		} else if isUngraduatedTrustStatus(trust.Status) {
			if err := s.restrictUngraduatedMedia(chat, user, "verification_pass"); err == nil {
				return nil
			} else {
				s.logTelegramPermissionWarning("restrict media after verification pass failed, falling back to normal permissions", err, chat, user, "verification_pass")
			}
		}
	}
	return s.restoreUserMessagePermissions(chat, user, "verification_pass")
}

func (s *Service) restoreTrustedUserPermissions(trust store.UserTrust, reason string) {
	if trust.Status != "trusted" {
		return
	}
	if err := s.restoreUserMessagePermissions(&tele.Chat{ID: trust.ChatID}, userFromTrust(trust), reason); err != nil {
		s.logTelegramPermissionWarning("restore trusted user permissions failed", err, &tele.Chat{ID: trust.ChatID}, userFromTrust(trust), reason)
	}
}

func (s *Service) RestoreTrustedChatPermissions(_ context.Context, trust store.UserTrust) error {
	if trust.Status != "trusted" {
		return nil
	}
	return s.restoreUserMessagePermissions(&tele.Chat{ID: trust.ChatID}, userFromTrust(trust), "trusted_status")
}

func (s *Service) restoreUserMessagePermissions(chat *tele.Chat, user *tele.User, reason string) error {
	if s == nil || s.bot == nil || chat == nil || user == nil {
		return nil
	}
	member := tele.ChatMember{
		User:   user,
		Rights: tele.NoRestrictions(),
	}
	if err := s.bot.Restrict(chat, &member); err != nil {
		return err
	}
	s.logTelegramPermissionInfo("restored user message permissions", chat, user, reason)
	return nil
}

func (s *Service) logTelegramPermissionWarning(message string, err error, chat *tele.Chat, user *tele.User, reason string) {
	if s == nil || s.logger == nil {
		return
	}
	fields := telegramPermissionLogFields(chat, user, reason)
	if err != nil {
		fields = append(fields, zap.String("error", redact.ErrorString(err)))
	}
	s.logger.Warn(message, fields...)
}

func (s *Service) logTelegramPermissionInfo(message string, chat *tele.Chat, user *tele.User, reason string) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Info(message, telegramPermissionLogFields(chat, user, reason)...)
}

func telegramPermissionLogFields(chat *tele.Chat, user *tele.User, reason string) []zap.Field {
	fields := []zap.Field{zap.String("reason", reason)}
	if chat != nil {
		fields = append(fields, zap.Int64("chat_id", chat.ID))
	}
	if user != nil {
		fields = append(fields, zap.Int64("user_id", user.ID), zap.String("username", user.Username))
	}
	return fields
}
