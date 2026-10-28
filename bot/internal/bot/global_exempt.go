package bot

import (
	"context"
	"errors"

	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

// Global exemption applies only to a real Telegram user, never a sender_chat
// persona. A lookup failure is not an exemption. Existing per-group bans win.
func (s *Service) globalExemption(ctx context.Context, chatID, userID int64) (exempt, listedBanned bool) {
	if s == nil || s.queries == nil || chatID == 0 || userID <= 0 {
		return false, false
	}
	active, banned, err := s.queries.GlobalExemptionStatus(ctx, chatID, userID)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("global exemption lookup failed; do not exempt", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int64("user_id", userID))
		}
		return false, false
	}
	return active && !banned, active && banned
}
func (s *Service) exemptUser(ctx context.Context, chat *tele.Chat, user *tele.User) bool {
	if chat == nil || user == nil {
		return false
	}
	yes, _ := s.globalExemption(ctx, chat.ID, user.ID)
	return yes
}
func (s *Service) exemptMessage(ctx context.Context, msg *tele.Message) bool {
	if msg == nil || msg.SenderChat != nil {
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
var errExemptPending = errors.New("global exemption active; verification requires administrator review")
