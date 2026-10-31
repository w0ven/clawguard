package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

type moderationHeldKey struct{}

// actionAppliedError records an externally confirmed effect despite a later failure.
type actionAppliedError struct {
	action string
	err    error
}

func (e *actionAppliedError) Error() string { return e.err.Error() }
func (e *actionAppliedError) Unwrap() error { return e.err }

// recordSubjectCase uses an explicitly identified human and a stable source identity.
func (s *Service) recordSubjectCase(ctx context.Context, chat *tele.Chat, user *tele.User, kind, id, action, result, explanation string, ownership map[string]any) error {
	if s.queries == nil || chat == nil || user == nil || user.IsBot || user.ID <= 0 || id == "" || (s.bot != nil && s.bot.Me != nil && s.bot.Me.ID == user.ID) {
		return nil
	}
	if ownership == nil {
		ownership = map[string]any{}
	}
	ownership["automatic_reversal"] = "manual_review"
	raw, err := json.Marshal(ownership)
	if err != nil {
		return err
	}
	_, err = s.queries.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: kind + ":" + id, SourceKind: kind, SourceID: id, ChatID: chat.ID, UserID: user.ID, Action: action, Result: result, Explanation: explanation, Ownership: raw})
	if err != nil && s.logger != nil {
		s.logger.Warn("moderation case write failed", zap.String("source_kind", kind), zap.Error(err))
	}
	return err
}

// A case must be anchored to a source identity produced by the actual action.
// Stable warning-event bindings supply an exact warning ID; other non-AI
// restrictions need manual review rather than a time-based ownership guess.
func (s *Service) recordMessageModerationCase(ctx context.Context, msg *tele.Message, sourceKind, sourceID, action, result, explanation string, decisionID int64, warningEvent ...string) error {
	if s.queries == nil || msg == nil || msg.Chat == nil || msg.Sender == nil || msg.Sender.IsBot || msg.Sender.ID <= 0 || sourceID == "" {
		return nil
	}
	if s.bot != nil && s.bot.Me != nil && msg.Sender.ID == s.bot.Me.ID {
		return nil
	}
	ownership := map[string]any{"source_kind": sourceKind, "source_id": sourceID, "automatic_reversal": "manual_review"}
	if decisionID > 0 {
		ownership["ai_decision_id"] = decisionID
	}
	if len(warningEvent) > 0 && warningEvent[0] != "" {
		id, err := s.queries.WarningForModerationEvent(ctx, warningEvent[0]+":warning", msg.Chat.ID, msg.Sender.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lookup exact warning ownership: %w", err)
		}
		if err == nil {
			ownership["warning_id"] = id
		}
	}
	raw, _ := json.Marshal(ownership)
	summary := collectMessageContent(msg)
	// A forwarded message or quote can contain another person's private text.
	if msg.Origin != nil || msg.OriginalSender != nil || msg.OriginalChat != nil || msg.OriginalSenderName != "" || msg.OriginalUnixtime != 0 || msg.ExternalReplyInfo != nil || msg.ReplyTo != nil {
		summary = ""
	}
	_, err := s.queries.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: fmt.Sprintf("%s:%s", sourceKind, sourceID), SourceKind: sourceKind, SourceID: sourceID, ChatID: msg.Chat.ID, UserID: msg.Sender.ID, Action: action, Result: result, Explanation: explanation, Summary: summary, Ownership: raw})
	if err != nil && s.logger != nil {
		s.logger.Warn("moderation case write failed", zap.String("source_kind", sourceKind), zap.Error(err))
	}
	return err
}

func (s *Service) recordVerificationCase(ctx context.Context, pending store.PendingVerification, user *tele.User, action, rule string, results ...string) error {
	if s.queries == nil || user == nil || user.IsBot || user.ID <= 0 || pending.ID <= 0 {
		return nil
	}
	if s.bot != nil && s.bot.Me != nil && s.bot.Me.ID == user.ID {
		return nil
	}
	id := fmt.Sprintf("pending:%d", pending.ID)
	raw, _ := json.Marshal(map[string]any{"pending_id": pending.ID, "rule": rule, "automatic_reversal": "manual_review"})
	result, explanation := "executed", "入群验证处理"
	if len(results) > 0 && results[0] == "partial" {
		result, explanation = "partial", "已移出群聊，重新入群权限尚待管理员确认"
	}
	_, err := s.queries.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: "verification_immediate:" + id, SourceKind: "verification_immediate", SourceID: id, ChatID: pending.ChatID, UserID: pending.UserID, Action: action, Result: result, Explanation: explanation, Ownership: raw})
	return err
}
