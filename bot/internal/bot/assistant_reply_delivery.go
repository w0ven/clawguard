package bot

// SGB group.py reply plans / delivery receipts and ReplyModeService adaptation.
// Source 82c3703daba218b36255132c9bf51ebc444c6480, MIT (assistant_prompts_sgb/LICENSE).
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

// SGB _build_reply_targets_context: candidates are this sender's batch and
// the messages its inputs reply to, NOT another copy of the whole hot window.
func assistantReplyTargets(ctx context.Context, msg *tele.Message, history []store.GroupAssistantMessage) (string, map[string]*tele.Message) {
	items, _ := ctx.Value(assistantBatchItemsKey{}).([]assistantReplyBatchItem)
	if len(items) == 0 {
		items = []assistantReplyBatchItem{{msg: msg, text: msg.Text}}
	}
	if len(items) > assistantReplyMergeMaxItems {
		items = items[len(items)-assistantReplyMergeMaxItems:]
	}
	approved := map[int]*tele.Message{}
	for _, m := range history {
		if m.ChatID == msg.Chat.ID && m.ThreadID == int32(msg.ThreadID) && m.TelegramMessageID > 0 {
			approved[int(m.TelegramMessageID)] = &tele.Message{ID: int(m.TelegramMessageID), Chat: msg.Chat, ThreadID: msg.ThreadID, Text: m.Text}
		}
	}
	for _, item := range items {
		if item.msg != nil && item.msg.Chat != nil && item.msg.Chat.ID == msg.Chat.ID && item.msg.ThreadID == msg.ThreadID {
			approved[item.msg.ID] = item.msg
		}
	}
	targets := map[string]*tele.Message{}
	aliases := []string{}
	add := func(alias string, target *tele.Message) {
		if target == nil || target.ID <= 0 || target.Chat == nil || target.Chat.ID != msg.Chat.ID || target.ThreadID != msg.ThreadID {
			return
		}
		if _, exists := targets[alias]; exists {
			return
		}
		targets[alias] = target
		targets[fmt.Sprintf("message_%d", target.ID)] = target // bounded legacy alias
		aliases = append(aliases, alias)
	}
	replyTarget := func(input *tele.Message) *tele.Message {
		if input == nil || input.ReplyTo == nil {
			return nil
		}
		if input.ReplyTo.Chat != nil && input.ReplyTo.Chat.ID != msg.Chat.ID {
			return nil
		}
		return approved[input.ReplyTo.ID] // no unapproved quoted text in the prompt
	}
	add("latest_input", msg)
	add("current_input", msg)
	add("first_input", items[0].msg)
	for i, item := range items {
		add(fmt.Sprintf("input_%d", i+1), item.msg)
		add(fmt.Sprintf("input_%d_reply_target", i+1), replyTarget(item.msg))
	}
	add("latest_reply_target", replyTarget(msg))
	add("reply_target", replyTarget(msg))
	// Previews are optional data. Keep every bounded alias even when its
	// preview needs shrinking; the final request has an additional total cap.
	render := func(preview int) string {
		lines := []string{"[REPLY_TARGET_CANDIDATES]", "Use these aliases for reply_to; default_reply_alias: latest_input"}
		for _, alias := range aliases {
			target := targets[alias]
			line := fmt.Sprintf("- alias=%s | message_id=%d", alias, target.ID)
			if preview > 0 {
				line += " | preview=" + assistantPromptLabel(target.Text, preview)
			}
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n")
	}
	for _, preview := range []int{80, 32, 0} {
		text := render(preview)
		if assistantEstimateTokens(text) <= 2048 {
			return text, targets
		}
	}
	return render(0), targets
}
func assistantReplyTarget(spec assistantReplySpec, msg *tele.Message, history []store.GroupAssistantMessage) *tele.Message {
	if spec.DeliveryMode != "reply" {
		return nil
	}
	if spec.ReplyTo == "auto" || spec.ReplyTo == "" || spec.ReplyTo == "latest_input" {
		return msg
	}
	for _, m := range history {
		if m.ChatID == msg.Chat.ID && m.ThreadID == int32(msg.ThreadID) && m.TelegramMessageID > 0 && spec.ReplyTo == fmt.Sprintf("message_%d", m.TelegramMessageID) {
			return &tele.Message{ID: int(m.TelegramMessageID), Chat: msg.Chat, ThreadID: msg.ThreadID}
		}
	}
	// An unknown target does not create a fabricated reply relationship.
	return nil
}
func (a *GroupAssistant) resolveAssistantReplyModes(ctx context.Context, msg *tele.Message, policy store.GroupAssistantPolicy, pool AssistantPoolConfig, specs []assistantReplySpec) {
	need := false
	drafts := []string{}
	for _, s := range specs {
		need = need || s.DeliveryMode == "auto"
		drafts = append(drafts, s.Text)
	}
	if !need {
		return
	}
	payload, _ := json.Marshal(drafts)
	bot := a.botForPrompt()
	mentioned, replyBot := assistantMentionsBot(msg, bot), assistantRepliesToBot(msg, bot)
	replyOther := msg.ReplyTo != nil && !replyBot
	if rt := assistantRuntime(ctx); rt != nil && rt.mode == assistantReplyDirect {
		mentioned = true // any direct input in a merged batch forces the whole batch
	}
	request := ai.CheckRequest{SystemPrompt: assistantReplyModePrompt + "\n" + assistantSafetyPrompt, Messages: []ai.Message{{Role: "user", Content: fmt.Sprintf("[IS_MENTIONED]\n%s\n[IS_REPLY_TO_BOT]\n%s\n[IS_REPLY_TO_OTHER]\n%s\n[CURRENT_MESSAGE]\n%s\n[ASSISTANT_DRAFT_REPLIES]\n%s", assistantYesNo(mentioned), assistantYesNo(replyBot), assistantYesNo(replyOther), msg.Text, payload)}}, MaxTokens: 200, Temperature: 0, Timeout: 6 * time.Second}
	result, _, err := a.dispatchPlain(ctx, msg.Chat.ID, "decision", pool, policy, request)
	var modes []string
	if err == nil && result != nil {
		modes = parseAssistantReplyModes(result.Content, len(specs))
	}
	for i := range specs {
		if specs[i].DeliveryMode != "auto" {
			continue
		}
		specs[i].DeliveryMode = "message"
		if replyBot || (mentioned && !replyOther) {
			specs[i].DeliveryMode = "reply"
		}
		if len(modes) == len(specs) {
			specs[i].DeliveryMode = modes[i]
		}
	}
}

// SGB ReplyModeService accepts JSON aliases, arrays and plain reply/message
// lines. A malformed decision uses the mention/reply fallback, not forced send.
func parseAssistantReplyModes(raw string, count int) []string {
	matches := regexp.MustCompile(`(?i)\b(reply|message)\b`).FindAllString(raw, -1)
	if len(matches) != count {
		return nil
	}
	for i := range matches {
		matches[i] = strings.ToLower(matches[i])
	}
	return matches
}

func (s *Service) deliverAssistantReply(ctx context.Context, msg *tele.Message, policy store.GroupAssistantPolicy, mode assistantReplyMode, settings store.AssistantGlobalSettings, pool AssistantPoolConfig, history []store.GroupAssistantMessage, answer string) error {
	rt := assistantRuntime(ctx)
	if rt != nil && (rt.voiceSent || rt.stickerSent || rt.deliveryUncertain) {
		return nil
	}
	specs := parseAssistantReplyOutput(answer)
	if len(specs) == 0 {
		return nil
	}
	s.assistant.resolveAssistantReplyModes(ctx, msg, policy, pool, specs)
	for _, spec := range specs {
		if !s.assistantPreSendReview(ctx, msg, mode) {
			return nil
		}
		opts := &tele.SendOptions{ParseMode: tele.ModeHTML, ThreadID: msg.ThreadID, ReplyTo: assistantReplyTarget(spec, msg, history)}
		if rt != nil && rt.replyTargets != nil && spec.DeliveryMode == "reply" && spec.ReplyTo != "" && spec.ReplyTo != "auto" {
			opts.ReplyTo = rt.replyTargets[spec.ReplyTo]
		}
		sent, err := s.sendAssistantReplyPlan(ctx, msg.Chat, spec.Text, opts, policy, settings)
		if err != nil {
			return err
		} // No replay of earlier plans after ambiguous delivery.
		if sent == nil || sent.ID == 0 {
			return fmt.Errorf("assistant delivery has no confirmed message id")
		}
		if err = s.storeAssistantDelivery(ctx, msg, policy, spec.Text, sent, opts); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) sendAssistantReplyPlan(ctx context.Context, chat *tele.Chat, text string, opts *tele.SendOptions, policy store.GroupAssistantPolicy, settings store.AssistantGlobalSettings) (*tele.Message, error) {
	if normalizeAssistantTTSMode(policy.TTSMode) == assistantTTSModeAlways {
		synth := s.assistant.ttsSynthesizer(settings)
		if synth.Available() {
			result := synth.Synthesize(ctx, text)
			if result.OK {
				sent, err := s.sendAssistantVoice(ctx, chat, result.Audio, result.Format, opts)
				if err == nil {
					return sent, nil
				}
				// Transport failures can be ambiguous: don't auto-send another copy.
				return sent, fmt.Errorf("语音发送未确认，未重试: %w", err)
			}
			s.assistant.logTTSSkip(chat.ID, "始终语音合成失败，回退文本："+result.Error)
		} else {
			s.assistant.logTTSSkip(chat.ID, "语音未配置或已关闭，回退文本")
		}
	}
	return s.sendThrottled(ctx, chat, html.EscapeString(text), opts)
}
func (s *Service) storeAssistantDelivery(ctx context.Context, msg *tele.Message, policy store.GroupAssistantPolicy, text string, sent *tele.Message, opts *tele.SendOptions) error {
	if s.queries == nil {
		return nil
	}
	days := policy.RetentionDays
	if days <= 0 {
		days = assistantDefaultRetentionDays
	}
	hash := sha256.Sum256([]byte(text))
	id := int64(0)
	name := ""
	if s.bot != nil && s.bot.Me != nil {
		id = s.bot.Me.ID
		name = s.bot.Me.Username
	}
	_, err := s.queries.UpsertGroupAssistantMessage(ctx, store.UpsertGroupAssistantMessageParams{ChatID: msg.Chat.ID, ThreadID: int32(msg.ThreadID), TelegramMessageID: int64(sent.ID), SenderID: id, SenderName: name, Role: "assistant", Text: text, Approved: true, Delivered: true, ContentHash: hex.EncodeToString(hash[:]), ExpiresAt: time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour), SourceType: "telegram_assistant_reply", SourceID: assistantOutgoingReplySourceID(opts)})
	return err
}

// A transport-attempt receipt is scoped to this turn, not a shared GroupAssistant.
// The model may observe an error but cannot cause the same media action to replay.
func assistantReserveMedia(ctx context.Context, key string) error {
	rt := assistantRuntime(ctx)
	if rt == nil {
		return nil
	}
	if rt.deliveryUncertain {
		return fmt.Errorf("上次媒体发送未确认，本轮禁止再次发送")
	}
	if rt.mediaAttempted == nil {
		rt.mediaAttempted = map[string]bool{}
	}
	if rt.mediaAttempted[key] {
		return fmt.Errorf("本轮媒体已尝试发送，结果不确定时不能重试")
	}
	rt.mediaAttempted[key] = true
	return nil
}
