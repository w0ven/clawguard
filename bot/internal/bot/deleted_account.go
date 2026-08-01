package bot

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"golang.org/x/text/unicode/norm"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

const (
	deletedAccountNeedle = "deletedaccount"
	deletedAccountRule   = "filter_deleted_account"
)

// compactDeletedAccountIdentity makes spacing, punctuation, underscores,
// zero-width characters, case, and Unicode width differences irrelevant.
// Matching remains deliberately scoped to the ASCII phrase
// "deletedaccount" after compaction.
func compactDeletedAccountIdentity(value string) string {
	value = norm.NFKC.String(value)
	var compact strings.Builder
	compact.Grow(len(value))
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		compact.WriteRune(unicode.ToLower(r))
	}
	return compact.String()
}

func deletedAccountIdentityMatch(user *tele.User) (string, bool) {
	if user == nil {
		return "", false
	}

	displayName := strings.TrimSpace(strings.Join([]string{
		strings.TrimSpace(user.FirstName),
		strings.TrimSpace(user.LastName),
	}, " "))
	username := strings.TrimPrefix(strings.TrimSpace(user.Username), "@")

	matched := make([]string, 0, 2)
	if strings.Contains(compactDeletedAccountIdentity(displayName), deletedAccountNeedle) {
		matched = append(matched, "display_name="+displayName)
	}
	if strings.Contains(compactDeletedAccountIdentity(username), deletedAccountNeedle) {
		matched = append(matched, "username=@"+username)
	}
	if len(matched) == 0 {
		return "", false
	}
	return strings.Join(matched, " | "), true
}

func deletedAccountFilterResult(user *tele.User, policy config.FilterDeletedAccountPolicy) (FilterResult, bool) {
	if !policy.Enabled {
		return FilterResult{}, false
	}
	matched, ok := deletedAccountIdentityMatch(user)
	if !ok {
		return FilterResult{}, false
	}
	return FilterResult{
		Hit:         true,
		Reason:      deletedAccountRule,
		MatchedRule: matched,
		Action:      "delete_ban",
	}, true
}

// applyDeletedAccountMessageFilter runs before administrator/trust exemptions.
// A user who deliberately adopts this identity is therefore handled exactly
// like a genuinely deleted Telegram account representation.
func (s *Service) applyDeletedAccountMessageFilter(
	ctx context.Context,
	msg *tele.Message,
	policy config.GuardPolicy,
	actionsPaused bool,
) (bool, error) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return false, nil
	}
	result, matched := deletedAccountFilterResult(msg.Sender, policy.Filter.DeletedAccount)
	if !matched {
		return false, nil
	}

	actionRollback := func() {}
	if !actionsPaused {
		var actionOK bool
		actionRollback, actionOK = s.acquireUserActionLock(msg.Chat.ID, msg.Sender.ID)
		if !actionOK {
			if s.logger != nil {
				s.logger.Info("skip duplicate deleted-account message action", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
			}
			return true, nil
		}
		s.reactivateArchivedDeletedAccountTrust(ctx, msg.Chat.ID, msg.Sender.ID)
	}
	if err := s.applyFilterResult(ctx, msg, policy, result, actionsPaused); err != nil {
		actionRollback()
		return true, err
	}
	return true, nil
}

// handleDeletedAccountJoin is intentionally evaluated before verification,
// CAS/profile checks, and join-flood accounting. The Telegram ban revokes the
// user's messages and prevents rejoining until an administrator unbans them.
func (s *Service) handleDeletedAccountJoin(
	ctx context.Context,
	chat *tele.Chat,
	user *tele.User,
	policy config.GuardPolicy,
	actionsPaused bool,
) (bool, error) {
	if chat == nil || user == nil {
		return false, nil
	}
	result, matched := deletedAccountFilterResult(user, policy.Filter.DeletedAccount)
	if !matched {
		return false, nil
	}

	if actionsPaused {
		s.recordDeletedAccountJoinViolation(ctx, chat, user, result.MatchedRule, "skipped_paused:ban")
		return true, nil
	}

	actionRollback, actionOK := s.acquireUserActionLock(chat.ID, user.ID)
	if !actionOK {
		if s.logger != nil {
			s.logger.Info("skip duplicate deleted-account join action", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		}
		return true, nil
	}

	if err := s.banUser(chat, user); err != nil {
		actionRollback()
		if errors.Is(err, errActionsPaused) {
			s.recordDeletedAccountJoinViolation(ctx, chat, user, result.MatchedRule, "skipped_paused:ban")
			return true, nil
		}
		s.recordDeletedAccountJoinViolation(ctx, chat, user, result.MatchedRule, "failed:ban")
		return true, err
	}

	if s.queries != nil {
		s.reactivateArchivedDeletedAccountTrust(ctx, chat.ID, user.ID)
		s.upsertJoinSideEffects(ctx, chat, user)
		fakeMessage := &tele.Message{Chat: chat, Sender: user}
		s.resetTrustAfterViolation(ctx, fakeMessage, "ban", stringPtr(deletedAccountRule+": "+result.MatchedRule))

		deleted, err := s.queries.DeletePendingVerification(ctx, store.DeletePendingVerificationParams{
			ChatID: chat.ID,
			UserID: user.ID,
		})
		if err != nil {
			s.logger.Warn("delete pending verification for deleted-account join failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		} else if deleted > 0 {
			s.ensureRuntimeGuards()
			s.joinProtector.ReleasePending(chat.ID)
		}
	}

	s.recordDeletedAccountJoinViolation(ctx, chat, user, result.MatchedRule, "ban")
	s.writeModerationAudit(ctx, "filter", chat, user, "deleted_account_ban", "显示名或用户名命中 Deleted Account", map[string]any{
		"scene":   "join",
		"matched": result.MatchedRule,
		"outcome": "success",
	})
	return true, nil
}

func (s *Service) reactivateArchivedDeletedAccountTrust(ctx context.Context, chatID, userID int64) {
	if s.queries == nil {
		return
	}
	if _, err := s.queries.ReactivateArchivedUserTrust(ctx, chatID, userID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Warn("reactivate archived trust before deleted-account ban failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int64("user_id", userID))
	}
}

func (s *Service) recordDeletedAccountJoinViolation(ctx context.Context, chat *tele.Chat, user *tele.User, matched, action string) {
	if s.queries == nil || chat == nil || user == nil {
		return
	}
	if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
		ChatID:   chat.ID,
		UserID:   user.ID,
		Username: stringPtr(user.Username),
		Rule:     deletedAccountRule,
		Matched:  stringPtr(matched),
		Action:   action,
	}); err != nil {
		s.logger.Warn("record deleted-account join violation failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID), zap.String("action", action))
	}
}
