package bot

import (
	"context"
	"errors"

	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

const commandNonExemptKey = "clawguard_command_nonexempt_once"

type commandNonExempt struct{ chatID, userID int64 }

// Group exemption applies only to a real Telegram user, never a sender_chat
// persona. A lookup failure is not an exemption. Existing per-group bans win.
func (s *Service) groupExemption(ctx context.Context, chatID, userID int64) (exempt, listedBanned bool) {
	if s == nil || s.queries == nil || chatID == 0 || userID <= 0 || userID == 1087968824 || userID == 136817688 || userID == 777000 {
		return false, false
	}
	active, banned, err := s.queries.GroupExemptionStatus(ctx, chatID, userID)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("group exemption lookup failed; do not exempt", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int64("user_id", userID))
		}
		return false, false
	}
	return active && !banned, active && banned
}
func (s *Service) exemptUser(ctx context.Context, chat *tele.Chat, user *tele.User) bool {
	if chat == nil || !realExemptUser(user) {
		return false
	}
	yes, _ := s.groupExemption(ctx, chat.ID, user.ID)
	return yes
}
func (s *Service) exemptMessage(ctx context.Context, msg *tele.Message) bool {
	if msg == nil || msg.SenderChat != nil || !realExemptUser(msg.Sender) {
		return false
	}
	return s.exemptUser(ctx, msg.Chat, msg.Sender)
}

// Recheck authorization and freeze after external async work. A missing state
// cannot justify a new automatic punishment.
func (s *Service) automaticActionAllowed(ctx context.Context, chatID int64) bool {
	if s == nil || s.queries == nil || chatID == 0 {
		return false
	}
	authorized, err := s.IsAuthorizedGroup(ctx, chatID)
	if err != nil || !authorized {
		return false
	}
	state, err := s.GetSystemState(ctx)
	return err == nil && !state.Frozen && !state.ActionsPaused
}

// Returning an error preserves the pending record and avoids reporting a kick
// or verification pass which did not happen; an administrator can decide.
var errExemptPending = errors.New("group exemption active; verification requires administrator review")

// ExemptionTargetIsKnownBot checks durable observations and, if the user is
// currently visible to Telegram, its current identity. An absent/inaccessible
// future member stays unknown; runtime checks never exempt observed bots.
func (s *Service) ExemptionTargetIsKnownBot(ctx context.Context, chatID, userID int64) (bool, error) {
	known, err := s.queries.KnownBotUser(ctx, userID)
	if err != nil || known {
		return known, err
	}
	if s.bot == nil {
		return false, nil
	}
	member, err := s.bot.ChatMemberOf(&tele.Chat{ID: chatID}, &tele.User{ID: userID})
	if err == nil && member != nil && member.User != nil && member.User.ID == userID {
		return member.User.IsBot, nil
	}
	return false, nil
}

// deferredExemption revalidates an exemption candidate whose background task
// carries only a numeric ID. Unknown identity must pause, not imply human.
func (s *Service) deferredExemption(ctx context.Context, chat *tele.Chat, user *tele.User) (bool, string) {
	if chat == nil || user == nil {
		return false, ""
	}
	if user.ID <= 0 || user.ID == 1087968824 || user.ID == 136817688 || user.ID == 777000 {
		return false, ""
	}
	if s.queries == nil {
		return true, "exemption lookup unavailable; retry required"
	}
	active, banned, err := s.queries.GroupExemptionStatus(ctx, chat.ID, user.ID)
	if err != nil {
		return true, "exemption lookup unavailable; retry required"
	}
	if !active || banned {
		return false, ""
	}
	if s.bot == nil {
		return true, "exemption candidate identity unavailable; manual review required"
	}
	member, err := s.bot.ChatMemberOf(chat, user)
	if err != nil || member == nil || member.User == nil || member.User.ID != user.ID {
		return true, "exemption candidate identity unavailable; manual review required"
	}
	if member.User.IsBot {
		user.IsBot = true
		return false, ""
	}
	return true, "group exemption active; restriction requires manual review"
}
