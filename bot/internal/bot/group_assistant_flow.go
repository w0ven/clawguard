package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/store"
)

type assistantEligibility uint8

const (
	assistantEligibilityUnknown assistantEligibility = iota
	assistantEligibilityEligible
	assistantEligibilityBlocked
)

func assistantMessageKey(msg *tele.Message) string {
	if msg == nil || msg.Chat == nil || msg.ID == 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", msg.Chat.ID, msg.ID)
}

func (s *Service) setAssistantEligibility(msg *tele.Message, value assistantEligibility) {
	if s == nil {
		return
	}
	if key := assistantMessageKey(msg); key != "" {
		s.assistantEligibility.Store(key, value)
	}
}

func (s *Service) consumeAssistantEligibility(msg *tele.Message) assistantEligibility {
	if s == nil {
		return assistantEligibilityUnknown
	}
	key := assistantMessageKey(msg)
	if key == "" {
		return assistantEligibilityUnknown
	}
	value, ok := s.assistantEligibility.LoadAndDelete(key)
	if !ok {
		return assistantEligibilityUnknown
	}
	status, ok := value.(assistantEligibility)
	if !ok {
		return assistantEligibilityUnknown
	}
	return status
}

// handleApprovedAssistantMessage is called only after the moderation path has
// produced an explicit eligible result. It never infers safety from a nil error.
func (s *Service) handleApprovedAssistantMessage(ctx context.Context, msg *tele.Message, isEdited bool, eligibility assistantEligibility, keywordReplied bool, isAdmin bool) error {
	if s == nil || s.assistant == nil || msg == nil || msg.Chat == nil || msg.Sender == nil {
		return nil
	}
	policy, err := s.assistant.Policy(ctx, msg.Chat.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if eligibility != assistantEligibilityEligible {
		// An edited message that is blocked or unknown must revoke the prior
		// approved source and any facts derived from it. It must never create a
		// new approved row or feed chat/learning.
		if isEdited && s.queries != nil {
			return s.queries.InvalidateGroupAssistantSource(ctx, msg.Chat.ID, int64(msg.ID))
		}
		return nil
	}
	if !policy.ChatEnabled && !policy.LearningEnabled {
		return nil
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}
	if text == "" {
		return nil
	}
	if isEdited {
		// An eligible edit updates an existing approved source only. It never
		// inserts a new history row and never starts another chat/learning job.
		return s.assistant.storeIncoming(ctx, policy, msg, text, true)
	}

	if err := s.assistant.storeIncoming(ctx, policy, msg, text, false); err != nil {
		return err
	}
	if policy.LearningEnabled {
		authority := "learned_fact"
		sourceType := "telegram_approved_message"
		if isAdmin && looksLikeExplicitAssistantCorrection(text) {
			authority = "admin_explicit"
			sourceType = "telegram_admin_explicit_correction"
		}
		s.assistant.enqueueLearning(assistantLearningJob{policy: policy, chatID: msg.Chat.ID, threadID: msg.ThreadID,
			messageID: int64(msg.ID), senderID: msg.Sender.ID, senderName: displayName(msg.Sender), text: text,
			authority: authority, sourceType: sourceType, sourceChat: msg.Chat.ID, operatorID: msg.Sender.ID})
	}
	if !policy.ChatEnabled || keywordReplied || !s.assistant.shouldTrigger(msg, policy) {
		return nil
	}

	pool, err := s.assistant.loadPool(ctx, msg.Chat.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.logger.Info("group assistant chat skipped because model pool is not configured", zap.Int64("chat_id", msg.Chat.ID))
			return nil
		}
		return err
	}
	memories, err := s.assistant.loadMemories(ctx, msg.Chat.ID, text)
	if err != nil {
		return err
	}
	limit := policy.HistoryLimit
	if limit <= 0 || limit > assistantMaxHistoryMessages {
		limit = assistantMaxHistoryMessages
	}
	history, err := s.queries.ListGroupAssistantMessages(ctx, store.ListGroupAssistantMessagesParams{
		ChatID: msg.Chat.ID, ThreadID: int32Ptr(int32(msg.ThreadID)), SenderID: int64Ptr(msg.Sender.ID), Limit: limit,
	})
	if err != nil {
		return err
	}
	// The store returns newest first; model context is chronological.
	for left, right := 0, len(history)-1; left < right; left, right = left+1, right-1 {
		history[left], history[right] = history[right], history[left]
	}
	current := stripAssistantMention(text, s.bot)
	messages := assistantUntrustedContext(policy, memories, history, current)
	answer, _, err := s.assistant.dispatchTools(ctx, msg.Chat.ID, pool, policy, assistantSystemPrompt(policy), messages, assistantToolsFor(policy))
	if err != nil {
		s.logger.Warn("group assistant chat failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int("message_id", msg.ID))
		return nil
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sent, err := s.sendThrottled(sendCtx, msg.Chat, html.EscapeString(truncateAssistant(answer, assistantMaxTelegramText)), &tele.SendOptions{
		ParseMode: tele.ModeHTML, ReplyTo: msg, ThreadID: msg.ThreadID,
	})
	if err != nil {
		s.logger.Warn("send group assistant response failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int("message_id", msg.ID))
		return nil
	}
	assistantMessageID := int64(0)
	if sent != nil {
		assistantMessageID = int64(sent.ID)
	}
	if assistantMessageID == 0 {
		assistantMessageID = -time.Now().UnixNano()
	}
	retentionDays := policy.RetentionDays
	if retentionDays <= 0 {
		retentionDays = assistantDefaultRetentionDays
	}
	expiresAt := time.Now().UTC().Add(time.Duration(retentionDays) * 24 * time.Hour)
	hash := sha256.Sum256([]byte(answer))
	botID := int64(0)
	botName := ""
	if s.bot != nil && s.bot.Me != nil {
		botID = s.bot.Me.ID
		botName = s.bot.Me.Username
	}
	_, err = s.queries.UpsertGroupAssistantMessage(ctx, store.UpsertGroupAssistantMessageParams{
		ChatID: msg.Chat.ID, ThreadID: int32(msg.ThreadID), TelegramMessageID: assistantMessageID,
		SenderID: botID, SenderName: botName, Role: "assistant", Text: answer,
		Approved: true, Delivered: true, ContentHash: hex.EncodeToString(hash[:]), ExpiresAt: expiresAt,
		SourceType: "telegram_assistant_reply", SourceID: fmt.Sprintf("%d", msg.ID),
	})
	if err != nil {
		s.logger.Warn("store group assistant response failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID))
	}
	s.assistant.markSession(msg, policy)
	return nil
}

func looksLikeExplicitAssistantCorrection(text string) bool {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" || len([]rune(text)) > 2000 {
		return false
	}
	for _, marker := range []string{"更正", "纠正", "修正", "以此为准", "更新群规", "请记住", "记住"} {
		if !assistantCommandPrefix(text, marker) {
			continue
		}
		rest := strings.TrimPrefix(text, marker)
		if rest == "" || strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, "：") || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t") {
			return true
		}
	}
	return false
}

func assistantCommandPrefix(text, marker string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), marker)
}

func int32Ptr(v int32) *int32 { return &v }

func (a *GroupAssistant) storeIncoming(ctx context.Context, policy store.GroupAssistantPolicy, msg *tele.Message, text string, edited bool) error {
	if a.queries == nil || msg == nil || msg.Chat == nil || msg.Sender == nil {
		return nil
	}
	retentionDays := policy.RetentionDays
	if retentionDays <= 0 {
		retentionDays = assistantDefaultRetentionDays
	}
	expiresAt := time.Now().UTC().Add(time.Duration(retentionDays) * 24 * time.Hour)
	hash := sha256.Sum256([]byte(text))
	sourceType := "telegram_approved_message"
	if edited {
		sourceType = "telegram_edited_message"
	}
	if edited {
		_, err := a.queries.UpdateGroupAssistantMessage(ctx, store.UpdateGroupAssistantMessageParams{
			ChatID: msg.Chat.ID, ThreadID: int32(msg.ThreadID), TelegramMessageID: int64(msg.ID),
			SenderID: msg.Sender.ID, SenderName: displayName(msg.Sender), Text: text,
			ContentHash: hex.EncodeToString(hash[:]), SourceType: sourceType, SourceID: fmt.Sprintf("%d", msg.ID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// An eligible edit of a source that was never approved/stored is
			// intentionally a no-op, not an insert.
			return nil
		}
		return err
	}
	_, err := a.queries.UpsertGroupAssistantMessage(ctx, store.UpsertGroupAssistantMessageParams{
		ChatID: msg.Chat.ID, ThreadID: int32(msg.ThreadID), TelegramMessageID: int64(msg.ID),
		SenderID: msg.Sender.ID, SenderName: displayName(msg.Sender), Role: "user", Text: text,
		Approved: true, Delivered: true, ContentHash: hex.EncodeToString(hash[:]), ExpiresAt: expiresAt,
		SourceType: sourceType, SourceID: fmt.Sprintf("%d", msg.ID),
	})
	return err
}

func (a *GroupAssistant) shouldTrigger(msg *tele.Message, policy store.GroupAssistantPolicy) bool {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return false
	}
	explicit := assistantMentionsBot(msg, a.service.bot) || assistantRepliesToBot(msg, a.service.bot)
	mode := strings.TrimSpace(policy.TriggerMode)
	if mode == "" {
		mode = "mention_or_reply"
	}
	if mode == "mention_only" && !assistantMentionsBot(msg, a.service.bot) {
		return false
	}
	if explicit {
		return true
	}
	followupWindow := policy.FollowupWindowSec
	if followupWindow <= 0 {
		followupWindow = 300
	}
	maxTurns := policy.MaxFollowupTurns
	if maxTurns <= 0 {
		maxTurns = 5
	}
	key := fmt.Sprintf("%d:%d:%d", msg.Chat.ID, msg.ThreadID, msg.Sender.ID)
	a.sessionsMu.Lock()
	defer a.sessionsMu.Unlock()
	session, ok := a.sessions[key]
	if !ok || time.Since(session.LastAt) > time.Duration(followupWindow)*time.Second || session.Turns >= int(maxTurns) {
		delete(a.sessions, key)
		return false
	}
	return mode != "mention_only"
}

func (a *GroupAssistant) markSession(msg *tele.Message, policy store.GroupAssistantPolicy) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return
	}
	key := fmt.Sprintf("%d:%d:%d", msg.Chat.ID, msg.ThreadID, msg.Sender.ID)
	a.sessionsMu.Lock()
	defer a.sessionsMu.Unlock()
	session := a.sessions[key]
	maxTurns := policy.MaxFollowupTurns
	if maxTurns <= 0 {
		maxTurns = 5
	}
	session.LastAt = time.Now()
	session.Turns++
	if session.Turns >= int(maxTurns) {
		delete(a.sessions, key)
		return
	}
	a.sessions[key] = session
}

func assistantMentionsBot(msg *tele.Message, bot *tele.Bot) bool {
	if msg == nil || bot == nil || bot.Me == nil {
		return false
	}
	username := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(bot.Me.Username)), "@")
	if username == "" {
		return false
	}
	if len(msg.Entities) > 0 {
		for _, entity := range msg.Entities {
			switch entity.Type {
			case tele.EntityMention:
				if strings.TrimPrefix(strings.ToLower(strings.TrimSpace(msg.EntityText(entity))), "@") == username {
					return true
				}
			case tele.EntityTMention:
				if entity.User != nil && entity.User.ID == bot.Me.ID {
					return true
				}
			case tele.EntityCommand:
				command := strings.ToLower(strings.TrimSpace(msg.EntityText(entity)))
				if at := strings.LastIndex(command, "@"); at >= 0 && command[at+1:] == username {
					return true
				}
			}
		}
		return false
	}
	_, _, ok := assistantMentionRange(msg.Text, username)
	return ok
}

func assistantMentionRange(text, username string) (int, int, bool) {
	textRunes := []rune(strings.ToLower(text))
	needle := []rune("@" + strings.ToLower(strings.TrimPrefix(username, "@")))
	if len(needle) <= 1 {
		return 0, 0, false
	}
	for start := 0; start+len(needle) <= len(textRunes); start++ {
		matched := true
		for i := range needle {
			if textRunes[start+i] != needle[i] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		beforeOK := start == 0 || !assistantMentionWordRune(textRunes[start-1])
		end := start + len(needle)
		afterOK := end == len(textRunes) || !assistantMentionWordRune(textRunes[end])
		if beforeOK && afterOK {
			return start, end, true
		}
	}
	return 0, 0, false
}

func assistantMentionWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func assistantRepliesToBot(msg *tele.Message, bot *tele.Bot) bool {
	if msg == nil || msg.ReplyTo == nil || bot == nil || bot.Me == nil {
		return false
	}
	return msg.ReplyTo.Sender != nil && msg.ReplyTo.Sender.ID == bot.Me.ID
}

func stripAssistantMention(text string, bot *tele.Bot) string {
	text = strings.TrimSpace(text)
	if bot == nil || bot.Me == nil || bot.Me.Username == "" {
		return text
	}
	start, end, ok := assistantMentionRange(text, bot.Me.Username)
	if !ok {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:start]) + " " + string(runes[end:]))
}
