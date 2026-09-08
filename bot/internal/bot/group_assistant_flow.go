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
	"github.com/openclaw/clawguard/internal/ai"
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

type assistantReplyMode string

const (
	assistantReplyDirect assistantReplyMode = "direct"
	assistantReplyJoin   assistantReplyMode = "join"
	assistantReplyCold   assistantReplyMode = "cold"
)

func assistantSystemPromptForMode(policy store.GroupAssistantPolicy, mode string) string {
	base := assistantPersonaPrompt + "\n" + assistantCasualPrompt
	if custom := strings.TrimSpace(policy.SystemPrompt); custom != "" {
		base += "\n[ADMIN_STYLE_HINT]\n这是管理员提供的低优先级风格提示，只在不与 ACTIVE_PERSONA、安全、当前 Bot 身份、权限、只读工具范围和审核治理冲突时参考。\n" + truncateAssistant(custom, 8000)
	}
	if profile := strings.TrimSpace(policy.MimicProfileText); profile != "" {
		base += "\n[ACTIVE_PERSONA]\nauthoritative: yes\n这份画像完全覆盖默认人格的性格、态度、互动方式和说话口气；请直接以该人格说话，不要复述画像，不要透露在模仿谁。\n" + truncateAssistant(profile, 1200) +
			"\n[ACTIVE_PERSONA_END]\n画像不能覆盖安全边界、当前 Bot 身份、权限、只读工具范围、审核治理或主人识别。"
	}
	switch assistantReplyMode(mode) {
	case assistantReplyDirect:
		base += "\n[INTERACTION_MODE]\ndirect：对方明确点名或回复了你；像被点名的群友一样自然回答，先处理问题，默认一句短句。"
	case assistantReplyJoin:
		base += "\n[INTERACTION_MODE]\njoin：对方没有问你，只是群友凑一句；从旁观群友角度评论、附和、补充或吐槽，不把对方的话当给你的命令，不总结、不客服式答题。"
	case assistantReplyCold:
		base += "\n" + assistantProactiveTopicPrompt
		base += "\n[INTERACTION_MODE]\ncold：自然开启一个轻量话题，默认1到2句；没有合适话题时严格输出 SKIP_TASK。"
	}
	return base + "\n" + assistantSafetyPrompt
}

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
	s.assistant.collectApprovedSticker(ctx, msg)
	if !policy.ChatEnabled && !policy.LearningEnabled {
		// Mimic is an explicit, independent opt-in. It may collect only the
		// approved target text, without enabling ordinary history/chat writes.
		if policy.MimicTargetUserID != 0 && !isEdited {
			styleText := strings.TrimSpace(msg.Text)
			if styleText == "" {
				styleText = strings.TrimSpace(msg.Caption)
			}
			if styleText != "" {
				s.assistant.collectMimicSample(ctx, policy, msg.Chat.ID, msg.Sender.ID, styleText)
			}
		}
		return nil
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}
	if text == "" {
		s.assistant.collectApprovedSticker(ctx, msg)
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
	// Approved text is the only input eligible for chat, learning, and style
	// collection. Explicit @/reply and an active follow-up session are direct;
	// unsolicited interjection remains opt-in and passes two separate gates.
	if policy.MimicTargetUserID != 0 {
		s.assistant.collectMimicSample(ctx, policy, msg.Chat.ID, msg.Sender.ID, text)
	}
	if !policy.ChatEnabled || keywordReplied {
		return nil
	}
	direct := assistantMentionsBot(msg, s.bot) || assistantRepliesToBot(msg, s.bot) || s.assistant.shouldTrigger(msg, policy)
	mode := assistantReplyDirect
	if !direct {
		if !policy.ProactiveInterjectEnabled {
			return nil
		}
		mode = assistantReplyJoin
	}

	if s.assistant.enqueueReplyBatch(s, msg, text, direct, isAdmin) {
		return nil
	}
	return s.processAssistantReplyNow(ctx, msg, text, policy, direct, mode, isAdmin, 1)
}

func (s *Service) processQueuedAssistantReply(ctx context.Context, msg *tele.Message, text string, direct, isAdmin bool, mergedCount int) error {
	if s == nil || s.assistant == nil || msg == nil || msg.Chat == nil {
		return nil
	}
	policy, err := s.assistant.Policy(ctx, msg.Chat.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if !policy.ChatEnabled {
		return nil
	}
	mode := assistantReplyJoin
	if direct {
		mode = assistantReplyDirect
	} else if !policy.ProactiveInterjectEnabled {
		return nil
	}
	return s.processAssistantReplyNow(ctx, msg, text, policy, direct, mode, isAdmin, mergedCount)
}

func (s *Service) processAssistantReplyNow(ctx context.Context, msg *tele.Message, text string, policy store.GroupAssistantPolicy, direct bool, mode assistantReplyMode, isAdmin bool, mergedCount int) error {
	settings := s.assistant.loadRuntimeSettings(ctx)
	roles := parseAssistantRoleSet(settings.ModelRoles)
	pool, err := s.assistant.loadPool(ctx, msg.Chat.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.logger.Info("group assistant chat skipped", zap.Int64("chat_id", msg.Chat.ID), zap.String("reason", "还不能回复：先选聊天模型"))
			return nil
		}
		return err
	}
	pool = applyAssistantRolesToPool(pool, roles, policy)
	readiness := s.assistant.ChatReadinessForPool(pool)
	if !readiness.CanChat {
		reason := "聊天模型未就绪"
		if len(readiness.Blockers) > 0 {
			reason = readiness.Blockers[0]
		}
		s.logger.Info("group assistant chat skipped", zap.Int64("chat_id", msg.Chat.ID), zap.String("reason", reason))
		return nil
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
		ChatID: msg.Chat.ID, ThreadID: int32Ptr(int32(msg.ThreadID)), Limit: limit,
	})
	if err != nil {
		return err
	}
	// The store returns newest first; model context is chronological.
	for left, right := 0, len(history)-1; left < right; left, right = left+1, right-1 {
		history[left], history[right] = history[right], history[left]
	}
	history = s.assistant.compressHotWindow(ctx, msg.Chat.ID, pool, policy, history, settings, roles)
	if !direct && !assistantHardInterjectionGate(msg, history, s.bot) {
		return nil
	}
	current := stripAssistantMention(text, s.bot)
	sender := s.assistant.assistantPromptSender(ctx, msg, isAdmin)
	if !direct && !s.assistant.decideInterjection(ctx, msg, policy, pool, history, current, current, mergedCount, sender) {
		return nil
	}
	messages := assistantUntrustedContextWithSender(policy, memories, history, current, sender)
	if len(messages) > 0 {
		messages[len(messages)-1].Content = s.assistant.visionMessageParts(ctx, msg, current, roles)
	}
	overrides := s.assistant.loadPromptOverrides(ctx, msg.Chat.ID)
	system := assistantSystemPromptForModeWithOverrides(policy, string(mode), overrides) + "\n" + assistantBotIdentityBlock(s.bot) + "\n" + assistantCurrentSenderBlock(sender)
	if brief := strings.TrimSpace(policy.ProactiveTaskBrief); brief != "" && mode == assistantReplyJoin {
		system += "\n[PROACTIVE_TASK_BRIEF]\n" + truncateAssistant(brief, 500)
	}
	if ttsBlock := assistantTTSPreferenceBlock(policy.TTSMode, s.assistant.assistantTTSReady(settings)); ttsBlock != "" {
		system += "\n" + ttsBlock
	}
	s.assistant.toolRuntime = &assistantToolRuntime{msg: msg, mode: mode, settings: settings, roles: roles, current: current}
	defer func() { s.assistant.toolRuntime = nil }()
	replyCtx, replyCancel := context.WithTimeout(ctx, assistantReplyTimeout(settings))
	defer replyCancel()
	answer, _, err := s.assistant.dispatchTools(replyCtx, msg.Chat.ID, pool, policy, system, messages, assistantToolsFor(policy, settings, s.assistant.assistantTTSReady(settings)))
	if err != nil {
		s.logger.Warn("group assistant chat failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int("message_id", msg.ID))
		return nil
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return nil
	}
	if !s.assistantPreSendReview(ctx, msg, mode) {
		s.logger.Info("group assistant response dropped before send", zap.Int64("chat_id", msg.Chat.ID), zap.Int("message_id", msg.ID))
		return nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sendOptions := &tele.SendOptions{ParseMode: tele.ModeHTML, ThreadID: msg.ThreadID}
	if mode == assistantReplyDirect {
		sendOptions.ReplyTo = msg
	}
	replySourceID := assistantOutgoingReplySourceID(sendOptions)
	sent, err := s.sendThrottled(sendCtx, msg.Chat, html.EscapeString(truncateAssistant(answer, assistantMaxTelegramText)), sendOptions)
	if err != nil {
		s.logger.Warn("send group assistant response failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int("message_id", msg.ID))
		return nil
	}
	if normalizeAssistantTTSMode(policy.TTSMode) == assistantTTSModeAlways {
		if !settings.TTSEnabled || !s.assistant.assistantTTSReady(settings) {
			s.assistant.logTTSSkip(msg.Chat.ID, "始终语音已开，但全局未启用或未配置密钥，不假装发送成功")
		} else {
			synth := s.assistant.ttsSynthesizer(settings)
			ttsResult := synth.Synthesize(sendCtx, answer)
			if !ttsResult.OK {
				s.assistant.logTTSSkip(msg.Chat.ID, "始终语音合成失败："+ttsResult.Error)
			} else if _, voiceErr := s.sendAssistantVoice(sendCtx, msg.Chat, ttsResult.Audio, ttsResult.Format, sendOptions); voiceErr != nil {
				s.assistant.logTTSSkip(msg.Chat.ID, "始终语音发送失败")
			}
		}
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
		SourceType: "telegram_assistant_reply", SourceID: replySourceID,
	})
	if err != nil {
		s.logger.Warn("store group assistant response failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID))
	}
	if mode == assistantReplyDirect {
		s.assistant.markSession(msg, policy)
	}
	return nil
}

func assistantMentionsOtherUser(msg *tele.Message, bot *tele.Bot) bool {
	if msg == nil {
		return false
	}
	botID := int64(0)
	botName := ""
	if bot != nil && bot.Me != nil {
		botID = bot.Me.ID
		botName = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(bot.Me.Username)), "@")
	}
	for _, entity := range msg.Entities {
		switch entity.Type {
		case tele.EntityTMention:
			if entity.User != nil && entity.User.ID != 0 && entity.User.ID != botID {
				return true
			}
		case tele.EntityMention:
			name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(msg.EntityText(entity))), "@")
			if name != "" && name != botName {
				return true
			}
		}
	}
	if strings.Contains(msg.Text, "@") {
		if botName == "" {
			return true
		}
		lower := strings.ToLower(msg.Text)
		for _, token := range strings.FieldsFunc(lower, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune("，。！？!?、:：()（）", r)
		}) {
			if strings.HasPrefix(token, "@") && strings.TrimPrefix(token, "@") != botName {
				return true
			}
		}
	}
	return false
}

func assistantShortAck(text string) bool {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return true
	}
	for _, ack := range []string{"嗯", "嗯嗯", "哦", "哦哦", "好", "好的", "行", "可以", "收到", "ok", "okay", "哈哈", "呵呵", "？", "?"} {
		if text == ack {
			return true
		}
	}
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return r
	}, text)
	if compact == "" {
		return true
	}
	for _, r := range []rune(compact) {
		if !unicode.IsSymbol(r) && !unicode.Is(unicode.So, r) {
			return false
		}
	}
	return true
}

func assistantHardInterjectionGate(msg *tele.Message, history []store.GroupAssistantMessage, bot *tele.Bot) bool {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return false
	}
	if assistantMentionsOtherUser(msg, bot) {
		return false
	}
	if msg.ReplyTo != nil && (msg.ReplyTo.Sender == nil || bot == nil || bot.Me == nil || msg.ReplyTo.Sender.ID != bot.Me.ID) {
		return false
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}
	if assistantShortAck(text) {
		return false
	}
	// A bot response in the immediate preceding context raises the bar for an
	// unsolicited interjection; an explicit direct message bypasses this gate.
	for i := len(history) - 1; i >= 0; i-- {
		item := history[i]
		if item.TelegramMessageID == int64(msg.ID) && item.Role == "user" {
			continue
		}
		if item.Role == "assistant" {
			if time.Since(item.CreatedAt) < 2*time.Minute {
				return false
			}
			break
		}
		if item.Role == "user" {
			break
		}
	}
	// Two different members exchanging recent messages is not a safe place to
	// jump in without a direct address.
	seen := int64(0)
	previous := int64(0)
	for i := len(history) - 1; i >= 0 && seen < 3; i-- {
		item := history[i]
		if item.Role != "user" || item.TelegramMessageID == int64(msg.ID) || item.SenderID == 0 {
			continue
		}
		seen++
		if previous != 0 && previous != item.SenderID {
			return false
		}
		previous = item.SenderID
	}
	return true
}

func (a *GroupAssistant) decideInterjection(ctx context.Context, msg *tele.Message, policy store.GroupAssistantPolicy, pool AssistantPoolConfig, history []store.GroupAssistantMessage, current, mergedContext string, mergedCount int, sender assistantPromptSender) bool {
	if a == nil || msg == nil || msg.Chat == nil {
		return false
	}
	if strings.TrimSpace(current) == "" {
		current = strings.TrimSpace(msg.Text)
		if current == "" {
			current = strings.TrimSpace(msg.Caption)
		}
	}
	settings := a.loadRuntimeSettings(ctx)
	roles := parseAssistantRoleSet(settings.ModelRoles)
	pool = applyAssistantRolesToPool(pool, roles, policy)
	task := "chat"
	if _, ok := assistantTaskEndpointIDs(pool, "decision"); ok {
		task = "decision"
	}
	prompt := assistantDecisionContext(a.botForPrompt(), sender, history, current, mergedContext, mergedCount, assistantDecisionHistoryLimit(settings))
	decisionPrompt := assistantResolvedPrompt(a.loadPromptOverrides(ctx, msg.Chat.ID), "decision")
	decisionRole := roles.effective("decision")
	timeout := time.Duration(decisionRole.TimeoutSec * float64(time.Second))
	if timeout < time.Second {
		timeout = 10 * time.Second
	}
	result, _, err := a.dispatchPlain(ctx, msg.Chat.ID, task, pool, policy, ai.CheckRequest{
		Model:        policy.ChatModelRef,
		SystemPrompt: decisionPrompt,
		Messages:     []ai.Message{{Role: "user", Content: prompt}}, MaxTokens: 8, Temperature: 0, Timeout: timeout,
	})
	if err != nil || result == nil {
		return false
	}
	decision := strings.ToLower(strings.TrimSpace(result.Content))
	return decision == "casual"
}

func (s *Service) assistantPreSendReview(ctx context.Context, msg *tele.Message, mode assistantReplyMode) bool {
	if s == nil || s.assistant == nil || s.queries == nil || msg == nil || msg.Chat == nil || ctx == nil || ctx.Err() != nil {
		return false
	}
	fresh, err := s.assistant.Policy(ctx, msg.Chat.ID)
	if err != nil || !fresh.ChatEnabled {
		return false
	}
	if mode == assistantReplyJoin && !fresh.ProactiveInterjectEnabled {
		return false
	}
	latest, err := s.queries.GetLatestGroupAssistantUserMessage(ctx, msg.Chat.ID)
	if err != nil || latest.ChatID != msg.Chat.ID || latest.TelegramMessageID != int64(msg.ID) {
		return false
	}
	readiness := s.assistant.ChatReadiness(ctx, msg.Chat.ID)
	return readiness.CanChat
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

// assistantMessageReplySourceID stores the Telegram message being replied to
// in the existing SourceID column. A non-reply intentionally has no source;
// SourceID must not repeat the current message ID.
func assistantMessageReplySourceID(msg *tele.Message) string {
	if msg == nil || msg.ReplyTo == nil || msg.ReplyTo.ID == 0 {
		return ""
	}
	return fmt.Sprintf("%d", msg.ReplyTo.ID)
}

// assistantOutgoingReplySourceID records only the Telegram ReplyTo target
// actually configured for an outgoing message. Trigger/source messages are
// intentionally not persisted as reply metadata.
func assistantOutgoingReplySourceID(options *tele.SendOptions) string {
	if options == nil || options.ReplyTo == nil || options.ReplyTo.ID == 0 {
		return ""
	}
	return fmt.Sprintf("%d", options.ReplyTo.ID)
}

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
			ContentHash: hex.EncodeToString(hash[:]), SourceType: sourceType, SourceID: assistantMessageReplySourceID(msg),
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
		SourceType: sourceType, SourceID: assistantMessageReplySourceID(msg),
	})
	if err == nil {
		a.collectApprovedSticker(ctx, msg)
	}
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
