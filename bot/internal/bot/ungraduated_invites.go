package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

const ungraduatedInviteRule = "filter_newuser_no_invites"

func ungraduatedInviteRestrictionEnabled(policy config.GuardPolicy) bool {
	return policy.Filter.NewUser.Enabled && policy.Filter.NewUser.NoInvites
}

func (s *Service) handleUngraduatedInviteServiceMessage(ctx context.Context, msg *tele.Message, policy config.GuardPolicy) bool {
	if msg == nil || msg.Chat == nil || len(msg.UsersJoined) == 0 {
		return false
	}
	return s.handleUngraduatedInvite(ctx, msg.Chat, msg.Sender, msg.UsersJoined, msg, policy, "new_chat_members")
}

func (s *Service) handleUngraduatedInviteChatMember(ctx context.Context, update *tele.ChatMemberUpdate, target *tele.User, policy config.GuardPolicy) bool {
	if update == nil || update.Chat == nil || target == nil {
		return false
	}
	return s.handleUngraduatedInvite(ctx, update.Chat, update.Sender, []tele.User{*target}, nil, policy, "chat_member")
}

func (s *Service) handleUngraduatedInvite(ctx context.Context, chat *tele.Chat, inviter *tele.User, targets []tele.User, serviceMessage *tele.Message, policy config.GuardPolicy, source string) bool {
	if s == nil || s.queries == nil || chat == nil || inviter == nil || !ungraduatedInviteRestrictionEnabled(policy) {
		return false
	}
	if inviter.IsBot || (s.bot != nil && s.bot.Me != nil && inviter.ID == s.bot.Me.ID) {
		return false
	}

	blockedTargets := s.inviteTargetsForRestriction(inviter, targets)
	if len(blockedTargets) == 0 {
		return false
	}

	trust, err := s.ensureUserTrust(ctx, &tele.Message{Chat: chat, Sender: inviter})
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("load inviter trust for ungraduated invite restriction failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("inviter_user_id", inviter.ID))
		}
		return false
	}
	if !isUngraduatedTrustStatus(trust.Status) {
		return false
	}

	plannedAction := "kick"
	if serviceMessage != nil && serviceMessage.ID != 0 {
		plannedAction = "delete_kick"
	}
	paused, stateErr := s.actionsPaused(ctx)
	if stateErr != nil || paused {
		action := "skipped_paused:" + plannedAction
		if stateErr != nil {
			action = "skipped_state_unavailable:" + plannedAction
			if s.logger != nil {
				s.logger.Warn("load system state for ungraduated invite restriction failed", zap.Error(stateErr), zap.Int64("chat_id", chat.ID), zap.Int64("inviter_user_id", inviter.ID))
			}
		}
		s.recordUngraduatedInviteViolation(ctx, chat, inviter, blockedTargets, action, source, trust)
		return false
	}

	action := plannedAction
	serviceMessageDeleted := false
	if serviceMessage != nil && serviceMessage.ID != 0 {
		if err := s.deleteMessage(serviceMessage); err != nil {
			if errors.Is(err, errActionsPaused) {
				s.recordUngraduatedInviteViolation(ctx, chat, inviter, blockedTargets, "skipped_paused:"+plannedAction, source, trust)
				return false
			}
			action = "kick"
			if s.logger != nil {
				s.logger.Warn("delete ungraduated invite service message failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("inviter_user_id", inviter.ID), zap.Int("message_id", serviceMessage.ID))
			}
		} else {
			serviceMessageDeleted = true
		}
	}

	successfulKicks := 0
	pausedDuringAction := false
	for i := range blockedTargets {
		target := blockedTargets[i]
		if err := s.kickUser(chat, &target); err != nil {
			if errors.Is(err, errActionsPaused) {
				pausedDuringAction = true
			}
			if s.logger != nil {
				s.logger.Warn("kick user invited by ungraduated inviter failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("inviter_user_id", inviter.ID), zap.Int64("target_user_id", target.ID), zap.Bool("target_is_bot", target.IsBot))
			}
			if pausedDuringAction {
				break
			}
			continue
		}
		successfulKicks++
		if s.logger != nil {
			s.logger.Info("kicked user invited by ungraduated inviter", zap.Int64("chat_id", chat.ID), zap.Int64("inviter_user_id", inviter.ID), zap.Int64("target_user_id", target.ID), zap.Bool("target_is_bot", target.IsBot), zap.String("trust_status", trust.Status), zap.String("source", source))
		}
		if target.IsBot {
			reason := "auto-kicked: ungraduated inviter"
			if _, err := s.upsertBotTrust(ctx, chat, &target, "banned", 0, botTrustNotes(reason, inviter), stringPtr(reason)); err != nil && s.logger != nil {
				s.logger.Warn("record bot trust for ungraduated invite restriction failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("bot_user_id", target.ID), zap.Int64("inviter_user_id", inviter.ID))
			}
		}
	}

	if successfulKicks != len(blockedTargets) {
		prefix := "failed:"
		if successfulKicks > 0 || serviceMessageDeleted {
			prefix = "partial_failed:"
		}
		if pausedDuringAction {
			prefix = "skipped_paused:"
			if successfulKicks > 0 || serviceMessageDeleted {
				prefix = "partial_paused:"
			}
		}
		action = prefix + plannedAction
	}
	s.recordUngraduatedInviteViolation(ctx, chat, inviter, blockedTargets, action, source, trust)
	return successfulKicks == len(blockedTargets)
}

func (s *Service) inviteTargetsForRestriction(inviter *tele.User, targets []tele.User) []tele.User {
	if len(targets) == 0 {
		return nil
	}
	out := make([]tele.User, 0, len(targets))
	for _, target := range targets {
		if target.ID == 0 {
			continue
		}
		if inviter != nil && target.ID == inviter.ID {
			continue
		}
		if s != nil && s.bot != nil && s.bot.Me != nil && target.ID == s.bot.Me.ID {
			continue
		}
		out = append(out, target)
	}
	return out
}

func (s *Service) recordUngraduatedInviteViolation(ctx context.Context, chat *tele.Chat, inviter *tele.User, targets []tele.User, action string, source string, trust store.UserTrust) {
	if s == nil || s.queries == nil || chat == nil || inviter == nil || len(targets) == 0 {
		return
	}

	matched := truncateString(joinInviteTargetLabels(targets), 200)
	messageText := ""
	if raw, err := json.Marshal(map[string]any{
		"source":       source,
		"trust_status": trust.Status,
		"targets":      inviteAuditUsers(targets),
	}); err == nil {
		messageText = truncateString(string(raw), 1000)
	}

	if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
		ChatID:      chat.ID,
		UserID:      inviter.ID,
		Username:    stringPtr(inviter.Username),
		Rule:        ungraduatedInviteRule,
		Matched:     stringPtr(matched),
		Action:      action,
		MessageText: stringPtr(messageText),
	}); err != nil && s.logger != nil {
		s.logger.Warn("insert ungraduated invite violation failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("inviter_user_id", inviter.ID))
	}
}

func joinInviteTargetLabels(users []tele.User) string {
	labels := make([]string, 0, len(users))
	for _, user := range users {
		labels = append(labels, inviteUserLabel(user))
	}
	return strings.Join(labels, ", ")
}

func inviteAuditUsers(users []tele.User) []map[string]any {
	out := make([]map[string]any, 0, len(users))
	for _, user := range users {
		out = append(out, map[string]any{
			"user_id":    user.ID,
			"is_bot":     user.IsBot,
			"username":   user.Username,
			"first_name": user.FirstName,
			"last_name":  user.LastName,
			"display":    inviteUserLabel(user),
		})
	}
	return out
}

func inviteUserLabel(user tele.User) string {
	if username := strings.TrimSpace(user.Username); username != "" {
		return "@" + strings.TrimPrefix(username, "@")
	}
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name != "" {
		return name
	}
	return strconv.FormatInt(user.ID, 10)
}
