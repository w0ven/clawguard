package bot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

const redisCompareAndDeleteScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`

const (
	userActionLockTTL    = 60 * time.Second
	messageDeleteLockTTL = 30 * time.Second
)

func buildUserTrustBanReason(rule, matched, source string) []byte {
	return mustJSONBytes(map[string]any{
		"rule":    strings.TrimSpace(rule),
		"matched": strings.TrimSpace(matched),
		"source":  strings.TrimSpace(source),
	})
}

func parseUserTrustBanMeta(notes *string, fallbackSource string) (string, string, string) {
	if notes == nil {
		return "manual_ban", "", fallbackSource
	}
	raw := strings.TrimSpace(*notes)
	if raw == "" {
		return "manual_ban", "", fallbackSource
	}

	for _, prefix := range []struct {
		rule   string
		source string
	}{
		{rule: "profile_match_on_message", source: "bio_on_message"},
		{rule: "profile_match", source: "profile_match"},
		{rule: "cas_banned", source: "cas"},
		{rule: "warnings_threshold", source: "warnings_threshold"},
	} {
		if strings.HasPrefix(raw, prefix.rule+": ") {
			return prefix.rule, strings.TrimSpace(strings.TrimPrefix(raw, prefix.rule+": ")), prefix.source
		}
		if raw == prefix.rule {
			return prefix.rule, "", prefix.source
		}
	}

	if strings.HasPrefix(raw, "filter_") {
		return raw, "", "filter_rule"
	}

	return "ai_ban", raw, fallbackSource
}

type reviewableContent struct {
	Text        string
	Kind        string
	Skip        bool
	HasImage    bool
	ViaBotHint  bool
	VideoFrames [][]byte
	VideoMeta   videoMeta
}

type videoMeta struct {
	DurationSec  int
	Width        int
	Height       int
	ByteSize     int64
	Source       string // "video" | "animation" | "video_note"
	FileID       string
	FileUniqueID string
}

func aiModerationText(content reviewableContent) string {
	text := strings.TrimSpace(content.Text)
	if content.ViaBotHint {
		text = appendViaBotAIModerationHint(text)
	}
	if len(content.VideoFrames) == 0 {
		return text
	}

	instruction := fmt.Sprintf("视频关键帧审核：已随请求附带 %d 张按时间顺序抽取的关键帧，请以画面内容为准判断。", len(content.VideoFrames))
	caption := meaningfulVideoCaption(text)
	if caption == "" {
		return instruction
	}
	return instruction + "\n视频文字说明：" + caption
}

func appendViaBotAIModerationHint(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return viaBotAIModerationHint
	}
	return text + "\n" + viaBotAIModerationHint
}

const viaBotAIModerationHint = "【审核提示】此消息通过 inline bot 发送，富媒体内容（卡片/链接预览/图片）可能不在 bot 可见字段中，请基于 bot 用户名、文本和上下文严判，疑似引流/广告/色情应判违规。"

func meaningfulVideoCaption(text string) string {
	caption := strings.TrimSpace(text)
	for {
		trimmed := strings.TrimSpace(caption)
		switch {
		case strings.HasPrefix(trimmed, "[视频]"):
			caption = strings.TrimSpace(strings.TrimPrefix(trimmed, "[视频]"))
		case strings.HasPrefix(trimmed, "[GIF]"):
			caption = strings.TrimSpace(strings.TrimPrefix(trimmed, "[GIF]"))
		default:
			return trimmed
		}
	}
}

func shouldRunVideoModeration(content reviewableContent, policy config.AIPolicy) bool {
	if !policy.VideoModerationEnabled {
		return false
	}
	switch content.Kind {
	case "video", "animation":
		return true
	case "video_note":
		return policy.IncludeVideoNote
	default:
		return false
	}
}

func (s *Service) handleIncomingMessage(c tele.Context) error {
	return s.handleIncomingMessageWithOptions(c, false)
}

func (s *Service) handleEditedMessage(c tele.Context) error {
	return s.handleIncomingMessageWithOptions(c, true)
}

func (s *Service) handleIncomingMessageWithOptions(c tele.Context, isEdited bool) error {
	s.MarkUpdateSeen()
	msg := c.Message()
	if msg == nil || msg.Chat == nil || (msg.Sender == nil && msg.SenderChat == nil) || msg.Private() {
		return nil
	}

	ctx := context.Background()
	authorized, err := s.IsAuthorizedGroup(ctx, msg.Chat.ID)
	if err != nil {
		s.logger.Warn("authorized group check failed, allow message", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID))
	} else if !authorized {
		return nil
	}

	state, err := s.GetSystemState(ctx)
	if err != nil {
		s.logger.Warn("load system state failed, allow message", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID))
		state = store.SystemState{}
	}
	if state.Frozen {
		s.logger.Info("message ignored because system is frozen", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("sender_id", messageSenderID(msg)), zap.Int("message_id", msg.ID))
		return nil
	}

	policy, err := config.LoadPolicy(ctx, s.queries, msg.Chat.ID)
	if err != nil {
		s.logger.Warn("load guard policy failed for message filter, using defaults", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID))
		policy = config.DefaultPolicy
	}

	if handled, err := s.handleSenderChatMessage(ctx, msg, policy); err != nil {
		return err
	} else if handled {
		return nil
	}
	if msg.Sender == nil {
		return nil
	}
	if isSenderChatPersona(msg) {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, computeAIModerationTimeout(policy.AI))
	defer cancel()

	isAdmin := false
	adminStatus, err := s.isChatAdmin(ctx, msg.Chat.ID, msg.Sender.ID)
	if err != nil {
		s.logger.Warn("check chat admin failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID))
	} else {
		isAdmin = adminStatus
	}

	handled, err := s.applyFilterChecks(ctx, msg, policy, isAdmin)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}

	matchedReply, err := s.tryKeywordReply(ctx, msg, policy)
	if err != nil {
		return err
	}
	if matchedReply {
		return nil
	}

	content := s.buildReviewableContent(ctx, msg)
	if content.Skip {
		return nil
	}
	if content.Kind != "text" {
		recordNonTextViolation := func(action string) {
			caption := truncateString(strings.TrimSpace(collectMessageContent(msg)), 1000)
			if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
				ChatID:      msg.Chat.ID,
				UserID:      msg.Sender.ID,
				Username:    stringPtr(msg.Sender.Username),
				Rule:        "filter_non_text_message",
				Matched:     stringPtr(caption),
				Action:      action,
				MessageText: stringPtr(caption),
			}); err != nil {
				s.logger.Warn("insert non-text violation failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.String("action", action))
			}
		}
		switch strings.TrimSpace(strings.ToLower(policy.Filter.NonTextMessages)) {
		case "", "ai_review":
		case "off":
			return nil
		case "delete":
			if err := s.deleteMessage(msg); err != nil {
				s.logger.Warn("delete non-text message failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int("message_id", msg.ID))
			}
			recordNonTextViolation("delete")
			return nil
		case "delete_warn":
			if err := s.deleteMessage(msg); err != nil {
				s.logger.Warn("delete non-text message failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int("message_id", msg.ID))
				return nil
			}
			if _, _, err := s.IncrWarning(ctx, msg.Chat, msg.Sender, "filter_non_text_message", policy, true, false); err != nil {
				s.logger.Warn("warn non-text message failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID))
			}
			recordNonTextViolation("delete_warn")
			return nil
		default:
			s.logger.Warn("unknown non_text_messages policy, fallback to ai_review", zap.String("action", policy.Filter.NonTextMessages), zap.Int64("chat_id", msg.Chat.ID))
		}
	}

	if state.AIPaused {
		return nil
	}

	return s.applyAIModeration(ctx, msg, policy, isAdmin, content, isEdited)
}

func (s *Service) handleSenderChatMessage(ctx context.Context, msg *tele.Message, policy config.GuardPolicy) (bool, error) {
	if msg == nil || msg.Chat == nil || msg.SenderChat == nil || !policy.Filter.BanSenderChats {
		return false, nil
	}
	// Telegram 在「以频道身份发言」时仍会塞 fake Sender（GroupAnonymousBot/ChannelBot），
	// 所以不能再用 Sender==nil 当守卫。这里排除：本群自己的 sender_chat、linked channel
	// 自动转发、以及非 Channel 类型的 sender_chat（讨论组联动等）。
	if msg.SenderChat.ID == msg.Chat.ID {
		return false, nil
	}
	if msg.AutomaticForward {
		return false, nil
	}
	if msg.SenderChat.Type != tele.ChatChannel {
		return false, nil
	}

	if err := s.deleteMessage(msg); err != nil {
		s.logger.Warn("delete sender_chat message failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("sender_chat_id", msg.SenderChat.ID), zap.Int("message_id", msg.ID))
	}
	if err := s.banSenderChat(msg.Chat, msg.SenderChat); err != nil {
		s.logger.Warn("ban sender_chat failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("sender_chat_id", msg.SenderChat.ID))
	}

	messageText := truncateString(collectMessageContent(msg), 1000)
	matched := senderChatLabel(msg.SenderChat)
	if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
		ChatID:      msg.Chat.ID,
		UserID:      msg.SenderChat.ID,
		Username:    stringPtr(strings.TrimPrefix(msg.SenderChat.Username, "@")),
		Rule:        "filter_sender_chat",
		Matched:     stringPtr(matched),
		Action:      "delete_ban",
		MessageText: stringPtr(messageText),
	}); err != nil {
		return true, fmt.Errorf("insert sender_chat violation: %w", err)
	}

	return true, nil
}

func senderChatLabel(chat *tele.Chat) string {
	if chat == nil {
		return ""
	}
	if strings.TrimSpace(chat.Username) != "" {
		return "@" + strings.TrimPrefix(strings.TrimSpace(chat.Username), "@")
	}
	if strings.TrimSpace(chat.Title) != "" {
		return strings.TrimSpace(chat.Title)
	}
	return strconv.FormatInt(chat.ID, 10)
}

// isSenderChatPersona 用于在开关关闭时也跳过频道身份消息的后续 AI/警告流程：这些消息的
// Sender 是 fake，user_trust/warn 没有意义。
func isSenderChatPersona(msg *tele.Message) bool {
	if msg == nil || msg.SenderChat == nil {
		return false
	}
	if msg.Chat != nil && msg.SenderChat.ID == msg.Chat.ID {
		return false
	}
	if msg.AutomaticForward {
		return false
	}
	return msg.SenderChat.Type == tele.ChatChannel
}

func messageSenderID(msg *tele.Message) int64 {
	if msg == nil {
		return 0
	}
	if msg.Sender != nil {
		return msg.Sender.ID
	}
	if msg.SenderChat != nil {
		return msg.SenderChat.ID
	}
	return 0
}

func (s *Service) applyAIModeration(ctx context.Context, msg *tele.Message, policy config.GuardPolicy, isAdmin bool, content reviewableContent, isEdited bool) error {
	if msg == nil || msg.Chat == nil || msg.Sender == nil || !policy.AI.Enabled || s.aiModerator == nil {
		return nil
	}
	if isAdmin {
		return nil
	}

	trust, err := s.ensureUserTrust(ctx, msg)
	if err != nil {
		s.logger.Warn("ensure user trust failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID))
		return nil
	}

	nonWhitelistedBot := isOtherBot(msg.Sender, s.bot) && !s.isBotWhitelistedForChat(ctx, msg.Chat.ID, msg.Sender)
	switch trust.Status {
	case "trusted":
		if !nonWhitelistedBot {
			return nil
		}
	case "banned":
		deleteRelease, deleteOK := s.acquireMessageDeleteLock(msg.Chat.ID, msg.ID)
		if deleteOK {
			defer deleteRelease()
			if err := s.deleteMessage(msg); err != nil {
				s.logger.Warn("delete banned user message failed", zap.Error(err))
			}
		} else {
			s.logger.Info("skip duplicate banned user message delete", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
		}
		actionRelease, actionOK := s.acquireUserActionLock(msg.Chat.ID, msg.Sender.ID)
		if !actionOK {
			s.logger.Info("skip duplicate banned user action", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
			return nil
		}
		defer actionRelease()
		if err := s.banUser(msg.Chat, msg.Sender); err != nil {
			return err
		}
		s.sendActionFeedback(msg.Chat, nil, policy.Feedback.Ban, map[string]string{
			"user":         feedbackUserLabel(msg.Sender, policy.Feedback.Ban.ParseMode),
			"user_mention": feedbackUserMention(msg.Sender, policy.Feedback.Ban.ParseMode),
			"reason":       "已在封禁名单内",
		})
		return nil
	}

	if msg.Sticker != nil {
		content.HasImage = true
	}
	// 未毕业用户（new/suspicious）发言前 bio 审核（开关打开时才做）
	// 防止用户入群时 bio 干净、之后偷偷改 bio 加广告
	if policy.AI.CheckProfileOnMessage {
		s.asyncProfileCheck(msg, policy)
	}

	imageBase64 := ""
	imageHash := ""
	if content.HasImage && policy.AI.ImageModerationEnabled {
		imageCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()

		loadedBase64, loadedHash, err := s.loadVisualForModeration(imageCtx, msg)
		if err != nil {
			s.logger.Warn("download image for moderation failed, fallback to text", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int("message_id", msg.ID))
		} else {
			imageBase64 = loadedBase64
			imageHash = loadedHash
		}
	}

	if shouldRunVideoModeration(content, policy.AI) {
		vCtx, vCancel := context.WithTimeout(ctx, 30*time.Second)
		res, err := s.fetchVideoFrames(vCtx, msg, content.VideoMeta, policy.AI)
		vCancel()
		if err != nil || res.Truncated || len(res.Frames) == 0 {
			s.logger.Warn("video frame extract failed, fallback to text", zap.Error(err), zap.Bool("truncated", res.Truncated), zap.Int64("chat_id", msg.Chat.ID), zap.Int("message_id", msg.ID), zap.String("source", content.VideoMeta.Source), zap.Int64("byte_size", content.VideoMeta.ByteSize), zap.Int("duration_sec", content.VideoMeta.DurationSec))
		} else {
			content.VideoFrames = res.Frames
			content.VideoMeta = res.Meta
		}
	}

	forwardFrom := extractForwardSource(msg)
	scene := "message"
	imagesBase64 := make([]string, 0, len(content.VideoFrames))
	imagesHash := ""
	videoFileUniqueID := ""
	if len(content.VideoFrames) > 0 {
		for _, frame := range content.VideoFrames {
			imagesBase64 = append(imagesBase64, base64.StdEncoding.EncodeToString(frame))
		}
		joined := strings.Join(imagesBase64, "")
		sum := sha256.Sum256([]byte(joined))
		imagesHash = hex.EncodeToString(sum[:8])
		videoFileUniqueID = strings.TrimSpace(content.VideoMeta.FileUniqueID)
		scene = "video"
	}

	output, err := s.aiModerator.CheckMessage(ctx, ai.CheckInput{
		ChatID:            msg.Chat.ID,
		UserID:            msg.Sender.ID,
		Text:              aiModerationText(content),
		Scene:             scene,
		SenderName:        displayName(msg.Sender),
		ForwardFrom:       forwardFrom,
		ImageBase64:       imageBase64,
		ImageHash:         imageHash,
		ImagesBase64:      imagesBase64,
		ImagesHash:        imagesHash,
		VideoFileUniqueID: videoFileUniqueID,
		Policy:            policy.AI,
		IsUngraduated:     trust.Status != "trusted",
	})
	if err != nil {
		s.logger.Warn("ai moderation failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.String("trust_status", trust.Status))
		recordCtx, recordCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if recordErr := s.recordAIDecisionError(recordCtx, msg, content.Text, err); recordErr != nil {
			s.logger.Warn("record ai decision error failed", zap.Error(recordErr))
		}
		recordCancel()
		fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer fallbackCancel()
		return s.applyAIModerationErrorFallback(fallbackCtx, msg, policy, trust, err)
	}
	if output.Skipped {
		return s.maybeGraduateUser(ctx, trust, policy.AI)
	}

	action := s.decideAIAction(policy.AI, output)
	if output.FlagOnly && action != "none" {
		action = "flag"
	}
	var aiDecisionMetadata []byte
	md := map[string]any{}
	if scene == "video" && len(content.VideoFrames) > 0 {
		md["frame_count"] = len(content.VideoFrames)
		md["video_duration_sec"] = content.VideoMeta.DurationSec
		md["video_byte_size"] = content.VideoMeta.ByteSize
		md["file_unique_id"] = content.VideoMeta.FileUniqueID
		md["source_kind"] = content.VideoMeta.Source
	}
	if msg.Via != nil {
		md["via_bot_username"] = strings.TrimSpace(msg.Via.Username)
		md["via_bot_id"] = msg.Via.ID
		md["via_bot_first_name"] = strings.TrimSpace(msg.Via.FirstName)
	}
	if len(md) > 0 {
		if raw, mErr := json.Marshal(md); mErr == nil {
			aiDecisionMetadata = raw
		}
	}
	if err := s.recordAIDecision(ctx, msg, content.Text, output, action, scene, aiDecisionMetadata); err != nil {
		s.logger.Warn("record ai decision failed", zap.Error(err))
	}
	if err := s.applyAIAction(ctx, msg, policy, trust, output, action, isEdited); err != nil {
		return err
	}
	return nil
}

func isUngraduatedTrustStatus(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "new", "suspicious":
		return true
	default:
		return false
	}
}

func (s *Service) applyAIModerationErrorFallback(ctx context.Context, msg *tele.Message, policy config.GuardPolicy, trust store.UserTrust, callErr error) error {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return nil
	}
	if !isUngraduatedTrustStatus(trust.Status) {
		s.logger.Warn("ai moderation failed for graduated user, no fallback action", zap.Error(callErr), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.String("trust_status", trust.Status))
		return nil
	}

	reason := "AI 审核暂时不可用，已删除未毕业用户消息等待管理员复核"
	auditOutcome := "success"
	auditError := ""
	defer func() {
		s.writeModerationAudit(ctx, "ai", msg.Chat, msg.Sender, "ai_error_delete", reason, map[string]any{
			"message_id":   msg.ID,
			"trust_status": trust.Status,
			"outcome":      auditOutcome,
			"error":        auditError,
		})
	}()

	s.logger.Warn("ai moderation failed for ungraduated user, applying delete fallback", zap.Error(callErr), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.String("trust_status", trust.Status))
	deleteRelease, deleteOK := s.acquireMessageDeleteLock(msg.Chat.ID, msg.ID)
	if !deleteOK {
		auditOutcome = "deduped"
		s.logger.Info("skip duplicate ai error fallback message delete", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
		return nil
	}
	defer deleteRelease()
	if err := s.deleteMessage(msg); err != nil {
		auditOutcome = "failed"
		auditError = redact.ErrorString(err)
		return err
	}

	s.dispatchAIActionFeedback(msg, policy, "delete", ai.CheckOutput{
		Verdict: ai.Verdict{
			Verdict:  "error",
			Category: "system",
			Reason:   reason,
		},
	})
	return nil
}

func (s *Service) applyFilterChecks(ctx context.Context, msg *tele.Message, policy config.GuardPolicy, isAdmin bool) (bool, error) {
	result := checkMessage(ctx, msg, policy.Filter, isAdmin)
	if !result.Hit {
		var err error
		result, err = s.checkStatefulFilter(ctx, msg, policy, isAdmin)
		if err != nil {
			return false, err
		}
	}
	if !result.Hit {
		return false, nil
	}
	if err := s.applyFilterAction(ctx, msg, policy, result); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) checkStatefulFilter(ctx context.Context, msg *tele.Message, policy config.GuardPolicy, isAdmin bool) (FilterResult, error) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return FilterResult{}, nil
	}
	if s.queries == nil {
		return FilterResult{}, nil
	}

	if !isAdmin {
		trust, err := s.ensureUserTrust(ctx, msg)
		if err != nil {
			return FilterResult{}, fmt.Errorf("load user trust for filter: %w", err)
		}

		if result, err := s.checkNewUserFilter(ctx, msg, trust, policy.Filter.NewUser); err != nil {
			return FilterResult{}, err
		} else if result.Hit {
			s.applyUngraduatedPermissionRestriction(msg.Chat, msg.Sender, policy, trust, result.Reason)
			return result, nil
		}
	}
	if result, err := s.checkRateLimitFilter(ctx, msg, policy.AntiSpam.RateLimit); err != nil {
		return FilterResult{}, err
	} else if result.Hit {
		return result, nil
	}
	return FilterResult{}, nil
}

func (s *Service) checkNewUserFilter(ctx context.Context, msg *tele.Message, trust store.UserTrust, policy config.FilterNewUserPolicy) (FilterResult, error) {
	if !policy.Enabled || !isRestrictedNewUser(trust, policy.DurationHours) {
		return FilterResult{}, nil
	}

	if policy.NoLinks {
		if links := collectMessageLinks(msg); len(links) > 0 {
			return FilterResult{
				Hit:         true,
				Reason:      "filter_newuser_no_links",
				MatchedRule: links[0],
				Action:      "delete",
			}, nil
		}
	}
	if policy.NoForwards && extractForwardSource(msg) != "" {
		return FilterResult{
			Hit:         true,
			Reason:      "filter_newuser_no_forwards",
			MatchedRule: extractForwardSource(msg),
			Action:      "delete",
		}, nil
	}
	if policy.NoMedia && messageHasRestrictedMedia(msg) {
		return FilterResult{
			Hit:         true,
			Reason:      "filter_newuser_no_media",
			MatchedRule: messageMediaKind(msg),
			Action:      "delete",
		}, nil
	}
	if policy.MaxMessagesPerMinute > 0 {
		count, err := s.bumpWindowCounter(ctx, fmt.Sprintf("newuser:ratelimit:%d:%d", msg.Chat.ID, msg.Sender.ID), time.Minute)
		if err != nil {
			return FilterResult{}, fmt.Errorf("new user rate limit counter: %w", err)
		}
		if count > int64(policy.MaxMessagesPerMinute) {
			return FilterResult{
				Hit:         true,
				Reason:      "filter_newuser_rate_limit",
				MatchedRule: strconv.FormatInt(count, 10),
				Action:      "delete",
			}, nil
		}
	}
	return FilterResult{}, nil
}

func (s *Service) checkRateLimitFilter(ctx context.Context, msg *tele.Message, policy config.AntiSpamRatePolicy) (FilterResult, error) {
	if !policy.Enabled || policy.MessagesPer10s <= 0 || msg == nil || msg.Chat == nil || msg.Sender == nil {
		return FilterResult{}, nil
	}
	count, err := s.bumpWindowCounter(ctx, fmt.Sprintf("ratelimit:%d:%d", msg.Chat.ID, msg.Sender.ID), 10*time.Second)
	if err != nil {
		return FilterResult{}, fmt.Errorf("rate limit counter: %w", err)
	}
	if count <= int64(policy.MessagesPer10s) {
		return FilterResult{}, nil
	}
	return FilterResult{
		Hit:         true,
		Reason:      "filter_rate_limit",
		MatchedRule: strconv.FormatInt(count, 10),
		Action:      policy.Action,
	}, nil
}

func (s *Service) bumpWindowCounter(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	if s.redis == nil {
		return 0, nil
	}
	pipe := s.redis.TxPipeline()
	countCmd := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return countCmd.Val(), nil
}

func isRestrictedNewUser(trust store.UserTrust, _ int) bool {
	// The config key is still new_user for API compatibility, but the
	// restriction now follows the trust state machine: all ungraduated users
	// are restricted until they become trusted.
	return isUngraduatedTrustStatus(trust.Status)
}

func messageHasRestrictedMedia(msg *tele.Message) bool {
	if msg == nil {
		return false
	}
	return msg.Photo != nil ||
		msg.Video != nil ||
		msg.Document != nil ||
		msg.Animation != nil ||
		msg.Audio != nil ||
		msg.Voice != nil ||
		msg.VideoNote != nil ||
		msg.Sticker != nil ||
		msg.Contact != nil ||
		msg.Poll != nil ||
		msg.Dice != nil ||
		msg.Location != nil ||
		msg.Venue != nil ||
		msg.Game != nil ||
		msg.Invoice != nil ||
		msg.Story != nil ||
		msg.ReplyToStory != nil ||
		msg.Giveaway != nil ||
		msg.GiveawayCreated != nil ||
		msg.GiveawayWinners != nil ||
		msg.GiveawayCompleted != nil
}

func messageMediaKind(msg *tele.Message) string {
	if msg == nil {
		return ""
	}
	switch {
	case msg.Photo != nil:
		return "photo"
	case msg.Video != nil:
		return "video"
	case msg.Document != nil:
		return "document"
	case msg.Animation != nil:
		return "animation"
	case msg.Audio != nil:
		return "audio"
	case msg.Voice != nil:
		return "voice"
	case msg.VideoNote != nil:
		return "video_note"
	case msg.Sticker != nil:
		return "sticker"
	case msg.Contact != nil:
		return "contact"
	case msg.Poll != nil:
		return "poll"
	case msg.Dice != nil:
		return "dice"
	case msg.Location != nil:
		return "location"
	case msg.Venue != nil:
		return "venue"
	case msg.Game != nil:
		return "game"
	case msg.Invoice != nil:
		return "invoice"
	case msg.Story != nil || msg.ReplyToStory != nil:
		return "story"
	case msg.Giveaway != nil:
		return "giveaway"
	case msg.GiveawayCreated != nil:
		return "giveaway_created"
	case msg.GiveawayWinners != nil:
		return "giveaway_winners"
	case msg.GiveawayCompleted != nil:
		return "giveaway_completed"
	default:
		return "media"
	}
}

func (s *Service) applyFilterAction(ctx context.Context, msg *tele.Message, policy config.GuardPolicy, result FilterResult) error {
	action := result.Action
	if action == "" {
		action = config.DefaultPolicy.Filter.Keywords.Action
	}

	actionForViolation := action
	messageText := truncateString(collectMessageContent(msg), 1000)
	matched := truncateString(result.MatchedRule, 200)

	switch action {
	case "delete":
		if err := s.deleteMessage(msg); err != nil {
			return err
		}
	case "delete_warn":
		actionForViolation = "delete_warn"
		if err := s.deleteMessage(msg); err != nil {
			return err
		}
		if _, _, err := s.IncrWarning(ctx, msg.Chat, msg.Sender, result.Reason, policy, true, false); err != nil {
			return err
		}
	case "delete_mute":
		actionForViolation = "delete_mute"
		if err := s.deleteMessage(msg); err != nil {
			return err
		}
		if err := s.muteUser(msg.Chat, msg.Sender, 600); err != nil {
			return err
		}
	case "delete_ban":
		actionForViolation = "delete_ban"
		if err := s.deleteMessage(msg); err != nil {
			return err
		}
		if err := s.banUser(msg.Chat, msg.Sender); err != nil {
			return err
		}
	case "warn":
		actionForViolation = "warn"
		if _, _, err := s.IncrWarning(ctx, msg.Chat, msg.Sender, result.Reason, policy, true, false); err != nil {
			return err
		}
	case "delete_and_warn":
		actionForViolation = "delete_warn"
		if err := s.deleteMessage(msg); err != nil {
			return err
		}
		if _, _, err := s.IncrWarning(ctx, msg.Chat, msg.Sender, result.Reason, policy, true, false); err != nil {
			return err
		}
	case "mute":
		actionForViolation = "mute"
		if err := s.deleteMessage(msg); err != nil {
			return err
		}
		if err := s.muteUser(msg.Chat, msg.Sender, 600); err != nil {
			return err
		}
	default:
		s.logger.Warn("unknown filter action, fallback to delete_warn", zap.String("action", action), zap.Int64("chat_id", msg.Chat.ID))
		actionForViolation = "delete_warn"
		if err := s.deleteMessage(msg); err != nil {
			return err
		}
		if _, _, err := s.IncrWarning(ctx, msg.Chat, msg.Sender, result.Reason, policy, true, false); err != nil {
			return err
		}
	}

	if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
		ChatID:      msg.Chat.ID,
		UserID:      msg.Sender.ID,
		Username:    stringPtr(msg.Sender.Username),
		Rule:        result.Reason,
		Matched:     stringPtr(matched),
		Action:      actionForViolation,
		MessageText: stringPtr(messageText),
	}); err != nil {
		return fmt.Errorf("insert filter violation: %w", err)
	}

	s.maybeBanBotInviterAfterViolation(ctx, msg, policy, actionForViolation, result.Reason)
	s.resetTrustAfterViolation(ctx, msg, actionForViolation, stringPtr(result.Reason))

	// 动作反馈（根据 action 派发到不同模板）
	s.dispatchActionFeedback(msg, policy, actionForViolation, humanReason(result.Reason, matched), matched)

	return nil
}

// humanReason 把 system 级 reason 翻译成人话
func humanReason(rawReason, matched string) string {
	if matched != "" {
		switch rawReason {
		case "filter_keyword":
			return "触发关键词 " + matched
		case "filter_regex":
			return "触发正则 " + matched
		case "filter_link":
			return "发了不允许的链接 " + matched
		case "filter_username":
			return "用户名触发黑名单 " + matched
		case "filter_rate_limit":
			return "发言过快"
		case "filter_newuser_no_links":
			return "未毕业用户不能发链接"
		case "filter_newuser_no_forwards":
			return "未毕业用户不能转发"
		case "filter_newuser_no_media":
			return "未毕业用户不能发媒体"
		case "filter_newuser_rate_limit":
			return "未毕业用户发言过快"
		case "profile_match":
			return "资料命中黑名单 " + matched
		}
	}
	switch rawReason {
	case "filter_keyword":
		return "触发关键词过滤"
	case "filter_regex":
		return "触发正则过滤"
	case "filter_link":
		return "链接过滤"
	case "filter_username":
		return "用户名过滤"
	case "filter_rate_limit":
		return "发言频率限制"
	case "filter_non_text_message":
		return "未毕业用户不能发此类消息"
	case "filter_newuser_no_links", "filter_newuser_no_forwards",
		"filter_newuser_no_media", "filter_newuser_rate_limit":
		return "未毕业用户限制"
	case "profile_match":
		return "资料黑名单"
	case "cas_banned":
		return "CAS 黑名单"
	case "verify_timeout":
		return "验证超时"
	case "ai_banned":
		return "AI 审核命中（封禁）"
	case "ai_suspicious":
		return "AI 审核可疑"
	case "ai_error":
		return "AI 审核异常"
	case "ai_clean":
		return "AI 审核通过"
	}
	if rawReason == "" {
		return "违规"
	}
	return rawReason
}

// dispatchActionFeedback 根据 action 名称选择对应的反馈模板并发送
func (s *Service) dispatchActionFeedback(
	msg *tele.Message,
	policy config.GuardPolicy,
	action string,
	reason string,
	matched string,
) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return
	}
	fb := policy.Feedback
	vars := map[string]string{
		"user":         feedbackUserLabel(msg.Sender, fb.DeleteMsg.ParseMode),
		"user_mention": feedbackUserMention(msg.Sender, fb.DeleteMsg.ParseMode),
		"group":        msg.Chat.Title,
		"reason":       reason,
		"rule":         reason,
		"matched":      matched,
		"keyword":      matched,
	}

	switch action {
	case "delete":
		s.sendActionFeedback(msg.Chat, nil, fb.DeleteMsg, vars)
	case "delete_warn", "warn", "delete_and_warn":
		// warn 部分在 IncrWarning 内另走 Warn feedback；这里只触发 delete 反馈
		if action != "warn" {
			s.sendActionFeedback(msg.Chat, nil, fb.DeleteMsg, vars)
		}
	case "delete_mute", "mute":
		vars["user"] = feedbackUserLabel(msg.Sender, fb.Mute.ParseMode)
		vars["user_mention"] = feedbackUserMention(msg.Sender, fb.Mute.ParseMode)
		vars["duration"] = "10分钟"
		s.sendActionFeedback(msg.Chat, nil, fb.Mute, vars)
	case "delete_ban", "ban":
		vars["user"] = feedbackUserLabel(msg.Sender, fb.Ban.ParseMode)
		vars["user_mention"] = feedbackUserMention(msg.Sender, fb.Ban.ParseMode)
		s.sendActionFeedback(msg.Chat, nil, fb.Ban, vars)
	case "kick":
		vars["user"] = feedbackUserLabel(msg.Sender, fb.Kick.ParseMode)
		vars["user_mention"] = feedbackUserMention(msg.Sender, fb.Kick.ParseMode)
		s.sendActionFeedback(msg.Chat, nil, fb.Kick, vars)
	}
}

func (s *Service) ensureUserTrust(ctx context.Context, msg *tele.Message) (store.UserTrust, error) {
	trust, err := s.queries.GetUserTrust(ctx, msg.Chat.ID, msg.Sender.ID)
	if err == nil {
		if trust.Status == "archived" {
			reactivated, reactivateErr := s.queries.ReactivateArchivedUserTrust(ctx, msg.Chat.ID, msg.Sender.ID)
			if reactivateErr != nil {
				return store.UserTrust{}, reactivateErr
			}
			s.logger.Info("archived user reactivated", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID))
			return reactivated, nil
		}
		if isOtherBot(msg.Sender, s.bot) {
			if !trust.IsBot {
				updated, updateErr := s.queries.UpdateUserTrustIsBot(ctx, store.UpdateUserTrustIsBotParams{
					ChatID: msg.Chat.ID,
					UserID: msg.Sender.ID,
					IsBot:  true,
				})
				if updateErr != nil {
					return store.UserTrust{}, updateErr
				}
				trust = updated
			}
			if trust.Status == "trusted" && !s.isBotWhitelistedForChat(ctx, msg.Chat.ID, msg.Sender) {
				updated, updateErr := s.queries.UpdateUserTrustStatus(ctx, store.UpdateUserTrustStatusParams{
					ChatID: msg.Chat.ID,
					UserID: msg.Sender.ID,
					Status: "new",
					Score:  0.5,
					Notes:  stringPtr("bot whitelist revoked"),
				})
				if updateErr != nil {
					return store.UserTrust{}, updateErr
				}
				trust = updated
			}
		}
		return trust, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.UserTrust{}, err
	}
	if isOtherBot(msg.Sender, s.bot) {
		trust, err := s.queries.UpsertUserTrust(ctx, store.UpsertUserTrustParams{
			ChatID:          msg.Chat.ID,
			UserID:          msg.Sender.ID,
			Username:        userFieldPtr(msg.Sender.Username),
			FirstName:       userFieldPtr(msg.Sender.FirstName),
			LastName:        userFieldPtr(msg.Sender.LastName),
			JoinedAt:        time.Now(),
			Status:          "new",
			Score:           0.5,
			MessagesChecked: 0,
			MessagesClean:   0,
			Notes:           stringPtr("unknown bot message"),
			IsBot:           true,
		})
		if err != nil {
			return store.UserTrust{}, err
		}
		s.notifyOwnersOtherBot(ctx, msg.Chat, msg.Sender, "unknown_bot_message", "未知 bot 在群里发言")
		return trust, nil
	}
	// 没有 trust 记录的用户说明是 bot 部署前就在群里的老成员，直接标记 trusted
	return s.queries.UpsertUserTrust(ctx, store.UpsertUserTrustParams{
		ChatID:          msg.Chat.ID,
		UserID:          msg.Sender.ID,
		Username:        userFieldPtr(msg.Sender.Username),
		FirstName:       userFieldPtr(msg.Sender.FirstName),
		LastName:        userFieldPtr(msg.Sender.LastName),
		JoinedAt:        time.Now(),
		Status:          "trusted",
		Score:           0.5,
		MessagesChecked: 0,
		MessagesClean:   0,
		IsBot:           false,
	})
}

func isOtherBot(user *tele.User, bot *tele.Bot) bool {
	if user == nil || !user.IsBot {
		return false
	}
	return bot == nil || bot.Me == nil || user.ID != bot.Me.ID
}

func (s *Service) isBotWhitelistedForChat(ctx context.Context, chatID int64, user *tele.User) bool {
	policy, err := config.LoadPolicy(ctx, s.queries, chatID)
	if err != nil {
		s.logger.Warn("load policy for bot whitelist failed", zap.Error(err), zap.Int64("chat_id", chatID))
		policy = config.DefaultPolicy
	}
	return isBotWhitelisted(user, policy.Filter.BotWhitelist)
}

func isBotWhitelisted(user *tele.User, whitelist []string) bool {
	if user == nil {
		return false
	}
	username := strings.TrimPrefix(strings.TrimSpace(strings.ToLower(user.Username)), "@")
	if username == "" {
		return false
	}
	for _, allowed := range whitelist {
		if username == strings.TrimPrefix(strings.TrimSpace(strings.ToLower(allowed)), "@") {
			return true
		}
	}
	return false
}

func (s *Service) decideAIAction(policy config.AIPolicy, output ai.CheckOutput) string {
	// 优先按 verdict（粗分：ad/scam/harass/spam）查映射，再回退 category（细分：引流/刷单/...）
	verdict := strings.TrimSpace(strings.ToLower(output.Verdict.Verdict))
	ceiling := verdictActionCeiling(verdict)
	floor := verdictActionFloor(verdict)
	if ceiling == "none" {
		return "none"
	}

	apply := func(action string) string {
		return raiseAIActionByFloor(clampAIActionByCeiling(action, ceiling), floor)
	}

	if verdict != "" {
		if action, ok := policy.ActionsByCategory[verdict]; ok && strings.TrimSpace(action) != "" {
			return apply(normalizeAIAction(action))
		}
	}
	category := strings.TrimSpace(output.Verdict.Category)
	if category != "" {
		if action, ok := policy.ActionsByCategory[category]; ok && strings.TrimSpace(action) != "" {
			return apply(normalizeAIAction(action))
		}
	}

	confidence := output.Verdict.Confidence
	action := "none"
	switch {
	case confidence >= policy.Thresholds.Ban:
		action = "ban"
	case confidence >= policy.Thresholds.Mute:
		action = "mute"
	case confidence >= policy.Thresholds.Warn:
		action = "warn"
	case confidence >= policy.Thresholds.Flag:
		action = "flag"
	}
	return apply(action)
}

// verdictActionCeiling 给定 verdict 返回允许的最强动作
// normal → "none"（无视 category）
// 硬 verdict（ad/scam/spam/harass/porn/violence）→ "ban"（不限制）
// 未知 verdict → "none"（解析层应已降级，动作层继续 fail-safe）
func verdictActionCeiling(verdict string) string {
	switch strings.TrimSpace(strings.ToLower(verdict)) {
	case "ad", "scam", "spam", "harass", "porn", "violence":
		return "ban"
	default:
		return "none"
	}
}

// verdictActionFloor 给定 verdict 返回允许的最弱动作（地板）。
// normal/空/未知 → "none"（放行，无地板）
// 硬 verdict（ad/scam/spam/harass/porn/violence）→ "warn"
// 说明：硬罪名最低也得 warn，绝不能被 category="正常" 之类洗成 none
func verdictActionFloor(verdict string) string {
	switch strings.TrimSpace(strings.ToLower(verdict)) {
	case "ad", "scam", "spam", "harass", "porn", "violence":
		return "warn"
	default:
		return "none"
	}
}

// clampAIActionByCeiling 按 ceiling 夹住 action，返回最终动作
// 动作强度排序：none < flag < warn < delete < mute < ban
func clampAIActionByCeiling(action, ceiling string) string {
	rank := map[string]int{
		"none": 0, "flag": 1, "warn": 2, "delete": 3, "mute": 4, "ban": 5,
	}
	actionRank, okAction := rank[action]
	ceilingRank, okCeiling := rank[ceiling]
	if !okAction || !okCeiling {
		return action
	}
	if actionRank <= ceilingRank {
		return action
	}
	return ceiling
}

// raiseAIActionByFloor 按 floor 抬高 action。
// 动作强度排序：none < flag < warn < delete < mute < ban
func raiseAIActionByFloor(action, floor string) string {
	rank := map[string]int{
		"none": 0, "flag": 1, "warn": 2, "delete": 3, "mute": 4, "ban": 5,
	}
	actionRank, okAction := rank[action]
	floorRank, okFloor := rank[floor]
	if !okAction || !okFloor {
		return action
	}
	if actionRank >= floorRank {
		return action
	}
	return floor
}

func normalizeAIAction(action string) string {
	switch strings.TrimSpace(strings.ToLower(action)) {
	case "delete_and_warn", "delete_warn":
		return "warn"
	case "delete_ban", "ban":
		return "ban"
	case "delete_mute", "mute":
		return "mute"
	case "delete":
		return "delete"
	case "flag":
		return "flag"
	case "warn":
		return "warn"
	default:
		return "none"
	}
}

func (s *Service) recordAIDecision(ctx context.Context, msg *tele.Message, messageText string, output ai.CheckOutput, action string, scene string, metadata []byte) error {
	if strings.TrimSpace(scene) == "" {
		scene = "message"
	}
	_, err := s.queries.InsertAIDecision(ctx, store.InsertAIDecisionParams{
		ChatID:        msg.Chat.ID,
		UserID:        msg.Sender.ID,
		MessageID:     int64(msg.ID),
		MessageText:   stringPtr(truncateString(messageText, 2000)),
		ProviderID:    int64PtrIfPositive(output.ProviderID),
		ModelID:       int64PtrIfPositive(output.ModelID),
		Model:         output.Model,
		PromptVersion: output.PromptVersion,
		Verdict:       output.Verdict.Verdict,
		Confidence:    output.Verdict.Confidence,
		Category:      output.Verdict.Category,
		Reason:        stringPtr(output.Verdict.Reason),
		ActionTaken:   normalizeAIAction(action),
		LatencyMs:     int32(output.LatencyMs),
		Scene:         scene,
		Metadata:      metadata,
	})
	return err
}

func (s *Service) recordProfileViolationDecision(
	ctx context.Context,
	chat *tele.Chat,
	user *tele.User,
	triggerMsg *tele.Message,
	matched string,
	mode string,
	aiOutput *ai.CheckOutput,
) error {
	if chat == nil || user == nil {
		return nil
	}

	verdict := "normal"
	category := "bio_match"
	confidence := 1.0
	reason := "profile check hit: " + matched
	model := "profile_check:" + mode
	promptVersion := ""
	var providerID *int64
	var modelID *int64
	var latencyMs int32

	if aiOutput != nil {
		if value := strings.TrimSpace(strings.ToLower(aiOutput.Verdict.Verdict)); value != "" {
			verdict = value
		}
		if value := strings.TrimSpace(aiOutput.Verdict.Category); value != "" || verdict != "profile_violation" {
			category = normalizeBioAICategory(verdict, aiOutput.Verdict.Category)
		}
		if aiOutput.Verdict.Confidence > 0 {
			confidence = aiOutput.Verdict.Confidence
		}
		if value := strings.TrimSpace(aiOutput.Verdict.Reason); value != "" {
			reason = value
		}
		providerID = int64PtrIfPositive(aiOutput.ProviderID)
		modelID = int64PtrIfPositive(aiOutput.ModelID)
		if value := strings.TrimSpace(aiOutput.Model); value != "" {
			model = value
		}
		promptVersion = aiOutput.PromptVersion
		latencyMs = int32(aiOutput.LatencyMs)
	}

	messageID := int64(0)
	var messageText *string
	if triggerMsg != nil {
		messageID = int64(triggerMsg.ID)
		if value := strings.TrimSpace(triggerMsg.Text); value != "" {
			messageText = stringPtr(truncateString(value, 2000))
		}
	}

	_, err := s.queries.InsertAIDecision(ctx, store.InsertAIDecisionParams{
		ChatID:        chat.ID,
		UserID:        user.ID,
		MessageID:     messageID,
		MessageText:   messageText,
		ProviderID:    providerID,
		ModelID:       modelID,
		Model:         model,
		PromptVersion: promptVersion,
		Verdict:       verdict,
		Confidence:    confidence,
		Category:      category,
		Reason:        stringPtr(reason),
		ActionTaken:   "ban",
		LatencyMs:     latencyMs,
		Scene:         "bio",
	})
	return err
}

func (s *Service) recordAIDecisionError(ctx context.Context, msg *tele.Message, messageText string, callErr error) error {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return nil
	}
	reason := ""
	if callErr != nil {
		reason = truncateString(redact.ErrorString(callErr), 2000)
	}
	_, err := s.queries.InsertAIDecision(ctx, store.InsertAIDecisionParams{
		ChatID:        msg.Chat.ID,
		UserID:        msg.Sender.ID,
		MessageID:     int64(msg.ID),
		MessageText:   stringPtr(truncateString(messageText, 2000)),
		ProviderID:    nil,
		ModelID:       nil,
		Model:         "",
		PromptVersion: "",
		Verdict:       "error",
		Confidence:    0,
		Category:      "system",
		Reason:        stringPtr(reason),
		ActionTaken:   "error",
		LatencyMs:     0,
		Scene:         "message",
	})
	return err
}

func int64PtrIfPositive(v int64) *int64 {
	if v <= 0 {
		return nil
	}
	return &v
}

func buildReviewableContent(msg *tele.Message) reviewableContent {
	return buildReviewableContentWithOptions(context.Background(), msg, nil, fetchTmeLinkPreviewForUser)
}

func (s *Service) buildReviewableContent(ctx context.Context, msg *tele.Message) reviewableContent {
	var redisClient *redis.Client
	if typed, ok := s.redis.(*redis.Client); ok {
		redisClient = typed
	}
	current := extractReviewableContent(msg)
	content := buildReviewableContentWithOptions(ctx, msg, redisClient, fetchTmeLinkPreviewForUser)
	if shouldLogReplyPreviewNotExpanded(msg, current, content) {
		s.logger.Debug("reply preview not expanded", replyPreviewLogFields(msg, current, content)...)
	}
	return content
}

func buildReviewableContentWithOptions(
	ctx context.Context,
	msg *tele.Message,
	redisClient *redis.Client,
	fetchPreview func(context.Context, string, *redis.Client, int64) (*LinkPreview, error),
) reviewableContent {
	current := extractReviewableContent(msg)
	if msg == nil {
		return current
	}

	blocks := make([]string, 0, 5)
	combined := current
	if !current.Skip {
		blocks = append(blocks, reviewBlock("本次消息", current.Text))
	}

	if msg.ReplyTo != nil {
		reply := extractReviewableContent(msg.ReplyTo)
		combined = mergeReviewableContent(combined, reply)
		if !reply.Skip {
			source := quotedMessageSource(msg.ReplyTo)
			blocks = append(blocks, reviewBlock("引用回复", fmt.Sprintf("原消息(来自 %s): %s", source, reply.Text)))
		}
	}

	if msg.Quote != nil && strings.TrimSpace(msg.Quote.Text) != "" {
		quote := reviewableContent{Text: strings.TrimSpace(msg.Quote.Text), Kind: "quote"}
		combined = mergeReviewableContent(combined, quote)
		blocks = append(blocks, reviewBlock("引用片段", quote.Text))
	}

	if msg.ExternalReplyInfo != nil {
		external := buildExternalReplyReviewable(msg, current)
		combined = mergeReviewableContent(combined, external)
		if !external.Skip {
			blocks = append(blocks, reviewBlock("跨聊天引用", external.Text))
		}
	}

	if previewed, ok := buildTmePreviewReviewable(ctx, msg, current, redisClient, fetchPreview); ok {
		combined = mergeReviewableContent(combined, previewed)
		if previewText := linkPreviewBlockText(previewed.Text, current.Text); previewText != "" {
			blocks = append(blocks, reviewBlock("链接预览", previewText))
		}
	}

	if len(blocks) == 0 {
		return reviewableContent{Skip: true}
	}
	combined.Text = strings.Join(blocks, "\n")
	combined.Skip = false
	return combined
}

func buildTmePreviewReviewable(
	ctx context.Context,
	msg *tele.Message,
	current reviewableContent,
	redisClient *redis.Client,
	fetchPreview func(context.Context, string, *redis.Client, int64) (*LinkPreview, error),
) (reviewableContent, bool) {
	urls := extractTmeURLs(msg)
	if len(urls) == 0 {
		return reviewableContent{}, false
	}
	if len(urls) > maxTmePreviewFetch {
		urls = urls[:maxTmePreviewFetch]
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, tmePreviewTimeout)
	defer cancel()

	previews := make([]*LinkPreview, len(urls))
	group, groupCtx := errgroup.WithContext(timeoutCtx)
	userID := int64(0)
	if msg != nil && msg.Sender != nil {
		userID = msg.Sender.ID
	}
	for index, rawURL := range urls {
		index := index
		rawURL := rawURL
		group.Go(func() error {
			preview, err := fetchPreview(groupCtx, rawURL, redisClient, userID)
			if err != nil {
				return err
			}
			previews[index] = preview
			return nil
		})
	}
	_ = group.Wait()

	blocks := make([]string, 0, len(previews))
	for _, preview := range previews {
		if preview == nil {
			continue
		}
		lines := []string{
			fmt.Sprintf("【链接预览 %s】", preview.Source),
		}
		if preview.Title != "" {
			lines = append(lines, "标题: "+preview.Title)
		}
		if preview.Description != "" {
			lines = append(lines, "描述: "+preview.Description)
		}
		if preview.ImageURL != "" {
			lines = append(lines, "[含预览图]")
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}

	currentText := strings.TrimSpace(current.Text)
	if currentText == "" {
		currentText = strings.TrimSpace(collectMessageContent(msg))
	}

	if len(blocks) == 0 {
		return reviewableContent{
			Text:       fmt.Sprintf("【无法展开的 Telegram 链接】%s\n【本次消息】%s", strings.Join(urls, ", "), currentText),
			Kind:       current.Kind,
			Skip:       false,
			HasImage:   current.HasImage,
			ViaBotHint: current.ViaBotHint,
		}, true
	}

	return reviewableContent{
		Text:       strings.Join(blocks, tmePreviewSeparator) + fmt.Sprintf("\n\n【本次消息】%s", currentText),
		Kind:       current.Kind,
		Skip:       false,
		HasImage:   current.HasImage,
		ViaBotHint: current.ViaBotHint,
	}, true
}

// buildExternalReplyReviewable 把 Telegram 的“跨聊天引用回复”（ExternalReplyInfo）
// 和用户引用的片段（Quote）组合成可送 AI 审核的内容。
// 即便用户本次消息为空/单字（current.Skip=true），也强制返回 Skip=false，
// 把引用内容作为审核主体。
func buildExternalReplyReviewable(msg *tele.Message, current reviewableContent) reviewableContent {
	ext := msg.ExternalReplyInfo
	source := externalReplySource(ext)
	kind, hasImage := externalReplyKind(ext)
	mediaText := externalReplyMediaText(ext)

	quoteText := ""
	if msg.Quote != nil {
		quoteText = strings.TrimSpace(msg.Quote.Text)
	}
	parts := []string{fmt.Sprintf("原消息(来自 %s, 类型: %s)", source, kind)}
	if quoteText != "" {
		parts = append(parts, "引用片段: "+quoteText)
	}
	if mediaText != "" {
		parts = append(parts, "媒体: "+mediaText)
	}
	if ext != nil && ext.HasMediaSpoiler {
		parts = append(parts, "[媒体剧透]")
	}

	combinedHasImage := current.HasImage || hasImage
	finalKind := current.Kind
	if finalKind == "" {
		finalKind = "external_reply"
	}

	return reviewableContent{
		Text:       strings.Join(parts, "\n"),
		Kind:       finalKind,
		Skip:       false,
		HasImage:   combinedHasImage,
		ViaBotHint: current.ViaBotHint,
	}
}

// externalReplySource 从 ExternalReplyInfo.Origin / Chat 里挑个最可读的来源名
func externalReplySource(ext *tele.ExternalReplyInfo) string {
	if ext == nil {
		return "unknown"
	}
	if ext.Chat != nil {
		if v := strings.TrimSpace(ext.Chat.Title); v != "" {
			if u := strings.TrimSpace(ext.Chat.Username); u != "" {
				return fmt.Sprintf("%s (@%s)", v, u)
			}
			return v
		}
		if v := strings.TrimSpace(ext.Chat.Username); v != "" {
			return "@" + v
		}
	}
	if ext.Origin != nil {
		o := ext.Origin
		if o.SenderChat != nil {
			if v := strings.TrimSpace(o.SenderChat.Title); v != "" {
				if u := strings.TrimSpace(o.SenderChat.Username); u != "" {
					return fmt.Sprintf("%s (@%s)", v, u)
				}
				return v
			}
			if v := strings.TrimSpace(o.SenderChat.Username); v != "" {
				return "@" + v
			}
		}
		if o.Chat != nil {
			if v := strings.TrimSpace(o.Chat.Title); v != "" {
				return v
			}
			if v := strings.TrimSpace(o.Chat.Username); v != "" {
				return "@" + v
			}
		}
		if o.Sender != nil {
			if v := strings.TrimSpace(o.Sender.Username); v != "" {
				return "@" + v
			}
			name := strings.TrimSpace(o.Sender.FirstName + " " + o.Sender.LastName)
			if name != "" {
				return name
			}
		}
		if v := strings.TrimSpace(o.SenderUsername); v != "" {
			return v
		}
	}
	return "unknown"
}

// externalReplyKind 返回被引用消息的媒体类型标签 + 是否含图
func externalReplyKind(ext *tele.ExternalReplyInfo) (string, bool) {
	if ext == nil {
		return "text", false
	}
	switch {
	case len(ext.Photo) > 0:
		return "photo", true
	case ext.Video != nil:
		return "video", false
	case ext.Animation != nil:
		return "gif", false
	case ext.Document != nil:
		return "document", false
	case ext.Sticker != nil:
		return "sticker", false
	case ext.Voice != nil:
		return "voice", false
	case ext.Audio != nil:
		return "audio", false
	case ext.Note != nil:
		return "video_note", false
	case ext.Contact != nil:
		return "contact", false
	case ext.Story != nil:
		return "story", false
	case ext.Poll != nil:
		return "poll", false
	case ext.Location != nil:
		return "location", false
	case ext.Venue != nil:
		return "venue", false
	case ext.Game != nil:
		return "game", false
	case ext.Dice != nil:
		return "dice", false
	}
	return "text", false
}

func externalReplyMediaText(ext *tele.ExternalReplyInfo) string {
	if ext == nil {
		return ""
	}
	switch {
	case len(ext.Photo) > 0:
		return "[图片]"
	case ext.Video != nil:
		return videoReviewText(ext.Video)
	case ext.Animation != nil:
		return animationReviewText(ext.Animation)
	case ext.Document != nil:
		return documentReviewText(ext.Document)
	case ext.Sticker != nil:
		return stickerReviewText(ext.Sticker)
	case ext.Voice != nil:
		return voiceReviewText(ext.Voice)
	case ext.Audio != nil:
		return audioReviewText(ext.Audio)
	case ext.Note != nil:
		return "[视频圆片]"
	case ext.Contact != nil:
		return contactReviewText(ext.Contact)
	case ext.Story != nil:
		return storyReviewText(ext.Story)
	case ext.Poll != nil:
		return pollReviewText(ext.Poll)
	case ext.Location != nil:
		return locationReviewText(ext.Location)
	case ext.Venue != nil:
		return venueReviewText(ext.Venue)
	case ext.Game != nil:
		return gameReviewText(ext.Game)
	case ext.Dice != nil:
		return diceReviewText(ext.Dice)
	case ext.Invoice != nil:
		return invoiceReviewText(ext.Invoice)
	case ext.Giveaway != nil:
		return giveawayReviewText(ext.Giveaway)
	case ext.GiveawayWinners != nil:
		return giveawayWinnersReviewText(ext.GiveawayWinners)
	default:
		return ""
	}
}

func mergeReviewableContent(base, extra reviewableContent) reviewableContent {
	if extra.Skip {
		return base
	}
	if base.Skip {
		base = reviewableContent{}
	}
	if base.Kind == "" {
		base.Kind = extra.Kind
	}
	base.HasImage = base.HasImage || extra.HasImage
	base.ViaBotHint = base.ViaBotHint || extra.ViaBotHint
	if base.VideoMeta.Source == "" && extra.VideoMeta.Source != "" {
		base.VideoMeta = extra.VideoMeta
	}
	return base
}

func reviewBlock(label, text string) string {
	return fmt.Sprintf("【%s】%s", label, strings.TrimSpace(text))
}

func linkPreviewBlockText(previewText, currentText string) string {
	text := strings.TrimSpace(previewText)
	currentText = strings.TrimSpace(currentText)
	if currentText != "" {
		text = strings.TrimSpace(strings.TrimSuffix(text, fmt.Sprintf("\n\n【本次消息】%s", currentText)))
	}
	return text
}

func shouldLogReplyPreviewNotExpanded(msg *tele.Message, current, content reviewableContent) bool {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return false
	}
	if !hasReplyPreviewContext(msg) {
		return false
	}
	if !isShortBody(current.Text) {
		return false
	}
	return strings.TrimSpace(current.Text) == strings.TrimSpace(content.Text)
}

func hasReplyPreviewContext(msg *tele.Message) bool {
	if msg == nil {
		return false
	}
	if msg.ReplyTo != nil || msg.ExternalReplyInfo != nil {
		return true
	}
	if msg.Quote != nil && strings.TrimSpace(msg.Quote.Text) != "" {
		return true
	}
	return len(extractTmeURLs(msg)) > 0
}

func isShortBody(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	return utf8.RuneCountInString(text) <= 2
}

func replyPreviewLogFields(msg *tele.Message, current, content reviewableContent) []zap.Field {
	fields := []zap.Field{
		zap.Int64("chat_id", msg.Chat.ID),
		zap.Int("message_id", msg.ID),
		zap.String("current_text", strings.TrimSpace(current.Text)),
		zap.String("review_text", strings.TrimSpace(content.Text)),
		zap.Bool("has_reply_to", msg.ReplyTo != nil),
		zap.Bool("has_external_reply", msg.ExternalReplyInfo != nil),
		zap.Bool("has_quote", msg.Quote != nil && strings.TrimSpace(msg.Quote.Text) != ""),
		zap.Bool("has_link_preview", len(extractTmeURLs(msg)) > 0),
		zap.Bool("has_preview_options", msg.PreviewOptions != nil),
	}
	if msg.ReplyTo != nil {
		fields = append(fields,
			zap.String("reply_to_kind", replyPreviewKind(msg.ReplyTo)),
			zap.Bool("reply_to_has_caption", messageCaption(msg.ReplyTo) != ""),
		)
	}
	if msg.ExternalReplyInfo != nil {
		fields = append(fields,
			zap.String("external_reply_kind", externalReplyKindString(msg.ExternalReplyInfo)),
			zap.Bool("external_reply_has_preview_options", msg.ExternalReplyInfo.PreviewOptions != nil),
		)
	}
	return fields
}

func replyPreviewKind(msg *tele.Message) string {
	if msg == nil {
		return "unknown"
	}
	switch {
	case msg.Photo != nil:
		return "photo"
	case msg.Video != nil:
		return "video"
	case msg.Animation != nil:
		return "animation"
	case msg.Document != nil:
		return "document"
	case msg.Sticker != nil:
		return "sticker"
	case msg.Voice != nil:
		return "voice"
	case msg.Audio != nil:
		return "audio"
	case msg.VideoNote != nil:
		return "video_note"
	case msg.Contact != nil:
		return "contact"
	case msg.Poll != nil:
		return "poll"
	case msg.Location != nil:
		return "location"
	case msg.Venue != nil:
		return "venue"
	case msg.Game != nil:
		return "game"
	case msg.Dice != nil:
		return "dice"
	case strings.TrimSpace(msg.Text) != "":
		return "text"
	case messageCaption(msg) != "":
		return "caption"
	default:
		return "unknown"
	}
}

func externalReplyKindString(ext *tele.ExternalReplyInfo) string {
	if ext == nil {
		return "unknown"
	}
	switch {
	case len(ext.Photo) > 0:
		return "photo"
	case ext.Video != nil:
		return "video"
	case ext.Animation != nil:
		return "animation"
	case ext.Document != nil:
		return "document"
	case ext.Sticker != nil:
		return "sticker"
	case ext.Voice != nil:
		return "voice"
	case ext.Audio != nil:
		return "audio"
	case ext.Note != nil:
		return "video_note"
	case ext.Contact != nil:
		return "contact"
	case ext.Story != nil:
		return "story"
	case ext.Poll != nil:
		return "poll"
	case ext.Location != nil:
		return "location"
	case ext.Venue != nil:
		return "venue"
	case ext.Game != nil:
		return "game"
	case ext.Dice != nil:
		return "dice"
	case ext.Invoice != nil:
		return "invoice"
	case ext.Giveaway != nil:
		return "giveaway"
	case ext.GiveawayWinners != nil:
		return "giveaway_winners"
	default:
		return "text"
	}
}

func extractReviewableContent(msg *tele.Message) reviewableContent {
	if msg == nil {
		return reviewableContent{Skip: true}
	}
	viaPrefix := ""
	viaBotHint := false
	if via := extractViaBot(msg); via != "" {
		viaPrefix = "[via inline bot: " + via + "] "
		viaBotHint = true
	}
	// 转发消息：拼上转发来源信息
	forwardPrefix := ""
	if fwd := extractForwardSource(msg); fwd != "" {
		forwardPrefix = "[转发自: " + fwd + "] "
	}
	prefix := viaPrefix + forwardPrefix
	if strings.TrimSpace(msg.Text) != "" {
		return reviewableContent{Text: prefix + strings.TrimSpace(msg.Text), Kind: "text", ViaBotHint: viaBotHint}
	}
	if messageCaption(msg) != "" {
		caption := messageCaption(msg)
		switch {
		case msg.Photo != nil || msg.Video != nil || msg.Document != nil:
			placeholder := "[媒体]"
			if msg.Photo != nil {
				placeholder = "[图片]"
			}
			if msg.Video != nil {
				placeholder = videoReviewText(msg.Video)
			}
			if msg.Document != nil {
				placeholder = documentReviewText(msg.Document)
			}
			content := reviewableContent{Text: prefix + placeholder + " " + caption, Kind: "media_caption", HasImage: msg.Photo != nil, ViaBotHint: viaBotHint}
			if msg.Video != nil {
				content.VideoMeta = videoMetaFromVideo(msg.Video)
			}
			return content
		case msg.Animation != nil:
			return reviewableContent{Text: prefix + animationReviewText(msg.Animation) + " " + caption, Kind: "animation", ViaBotHint: viaBotHint, VideoMeta: videoMetaFromAnimation(msg.Animation)}
		case msg.Voice != nil:
			return reviewableContent{Text: prefix + voiceReviewText(msg.Voice) + " " + caption, Kind: "voice", ViaBotHint: viaBotHint}
		case msg.Audio != nil:
			return reviewableContent{Text: prefix + audioReviewText(msg.Audio) + " " + caption, Kind: "audio", ViaBotHint: viaBotHint}
		case msg.VideoNote != nil:
			return reviewableContent{Text: prefix + "[视频圆片] " + caption, Kind: "video_note", ViaBotHint: viaBotHint, VideoMeta: videoMetaFromVideoNote(msg.VideoNote)}
		}
	}
	switch {
	case msg.Contact != nil:
		return reviewableContent{Text: prefix + contactReviewText(msg.Contact), Kind: "contact", ViaBotHint: viaBotHint}
	case msg.Poll != nil:
		return reviewableContent{Text: prefix + pollReviewText(msg.Poll), Kind: "poll", ViaBotHint: viaBotHint}
	case msg.Dice != nil:
		return reviewableContent{Text: prefix + diceReviewText(msg.Dice), Kind: "dice", ViaBotHint: viaBotHint}
	case msg.Location != nil:
		return reviewableContent{Text: prefix + locationReviewText(msg.Location), Kind: "location", ViaBotHint: viaBotHint}
	case msg.Venue != nil:
		return reviewableContent{Text: prefix + venueReviewText(msg.Venue), Kind: "venue", ViaBotHint: viaBotHint}
	case msg.Game != nil:
		return reviewableContent{Text: prefix + gameReviewText(msg.Game), Kind: "game", ViaBotHint: viaBotHint}
	case msg.Invoice != nil:
		return reviewableContent{Text: prefix + invoiceReviewText(msg.Invoice), Kind: "invoice", ViaBotHint: viaBotHint}
	case msg.Story != nil || msg.ReplyToStory != nil:
		story := msg.Story
		if story == nil {
			story = msg.ReplyToStory
		}
		return reviewableContent{Text: prefix + storyReviewText(story), Kind: "story", ViaBotHint: viaBotHint}
	case msg.Giveaway != nil:
		return reviewableContent{Text: prefix + giveawayReviewText(msg.Giveaway), Kind: "giveaway", ViaBotHint: viaBotHint}
	case msg.GiveawayCreated != nil:
		return reviewableContent{Text: prefix + "[赠品] 描述=created 数量=0 截止=", Kind: "giveaway_created", ViaBotHint: viaBotHint}
	case msg.GiveawayWinners != nil:
		return reviewableContent{Text: prefix + giveawayWinnersReviewText(msg.GiveawayWinners), Kind: "giveaway_winners", ViaBotHint: viaBotHint}
	case msg.GiveawayCompleted != nil:
		return reviewableContent{Text: prefix + fmt.Sprintf("[赠品] 描述=completed 数量=%d 截止=", msg.GiveawayCompleted.WinnerCount), Kind: "giveaway_completed", ViaBotHint: viaBotHint}
	case msg.Sticker != nil:
		return reviewableContent{Text: prefix + stickerReviewText(msg.Sticker), Kind: "sticker", ViaBotHint: viaBotHint}
	case msg.Animation != nil:
		return reviewableContent{Text: prefix + animationReviewText(msg.Animation), Kind: "animation", ViaBotHint: viaBotHint, VideoMeta: videoMetaFromAnimation(msg.Animation)}
	case msg.Voice != nil:
		return reviewableContent{Text: prefix + voiceReviewText(msg.Voice), Kind: "voice", ViaBotHint: viaBotHint}
	case msg.Audio != nil:
		return reviewableContent{Text: prefix + audioReviewText(msg.Audio), Kind: "audio", ViaBotHint: viaBotHint}
	case msg.VideoNote != nil:
		return reviewableContent{Text: prefix + "[视频圆片]", Kind: "video_note", ViaBotHint: viaBotHint, VideoMeta: videoMetaFromVideoNote(msg.VideoNote)}
	case msg.Photo != nil || msg.Video != nil:
		if msg.Photo != nil {
			return reviewableContent{Text: prefix + "[图片]", Kind: "photo", HasImage: true, ViaBotHint: viaBotHint}
		}
		return reviewableContent{Text: prefix + videoReviewText(msg.Video), Kind: "video", ViaBotHint: viaBotHint, VideoMeta: videoMetaFromVideo(msg.Video)}
	case msg.Document != nil:
		return reviewableContent{Text: prefix + documentReviewText(msg.Document), Kind: "document", ViaBotHint: viaBotHint}
	case viaBotHint:
		return reviewableContent{Text: prefix + "[无文字内容]", Kind: "text", ViaBotHint: true}
	default:
		return reviewableContent{Skip: true}
	}
}

func extractViaBot(msg *tele.Message) string {
	if msg == nil || msg.Via == nil {
		return ""
	}
	if username := strings.TrimSpace(msg.Via.Username); username != "" {
		return "@" + username
	}
	return strings.TrimSpace(msg.Via.FirstName)
}

func messageCaption(msg *tele.Message) string {
	if msg == nil {
		return ""
	}
	if value := strings.TrimSpace(msg.Caption); value != "" {
		return value
	}
	switch {
	case msg.Photo != nil:
		return strings.TrimSpace(msg.Photo.Caption)
	case msg.Video != nil:
		return strings.TrimSpace(msg.Video.Caption)
	case msg.Animation != nil:
		return strings.TrimSpace(msg.Animation.Caption)
	case msg.Document != nil:
		return strings.TrimSpace(msg.Document.Caption)
	case msg.Voice != nil:
		return strings.TrimSpace(msg.Voice.Caption)
	case msg.Audio != nil:
		return strings.TrimSpace(msg.Audio.Caption)
	default:
		return ""
	}
}

func videoMetaFromVideo(video *tele.Video) videoMeta {
	if video == nil {
		return videoMeta{}
	}
	return videoMeta{DurationSec: video.Duration, Width: video.Width, Height: video.Height, ByteSize: video.FileSize, Source: "video", FileID: video.FileID, FileUniqueID: video.UniqueID}
}

func videoMetaFromAnimation(animation *tele.Animation) videoMeta {
	if animation == nil {
		return videoMeta{}
	}
	return videoMeta{DurationSec: animation.Duration, Width: animation.Width, Height: animation.Height, ByteSize: animation.FileSize, Source: "animation", FileID: animation.FileID, FileUniqueID: animation.UniqueID}
}

func videoMetaFromVideoNote(videoNote *tele.VideoNote) videoMeta {
	if videoNote == nil {
		return videoMeta{}
	}
	return videoMeta{DurationSec: videoNote.Duration, Width: videoNote.Length, Height: videoNote.Length, ByteSize: videoNote.FileSize, Source: "video_note", FileID: videoNote.FileID, FileUniqueID: videoNote.UniqueID}
}

func storyReviewText(story *tele.Story) string {
	if story == nil {
		return "[Story 转发] 来源=unknown/0"
	}
	source := "unknown"
	if story.Poster != nil {
		source = strings.TrimSpace(story.Poster.Title)
		if source == "" && strings.TrimSpace(story.Poster.Username) != "" {
			source = "@" + strings.TrimPrefix(strings.TrimSpace(story.Poster.Username), "@")
		}
		if source == "" {
			source = strconv.FormatInt(story.Poster.ID, 10)
		}
	}
	return fmt.Sprintf("[Story 转发] 来源=%s/%d", source, story.ID)
}

func giveawayReviewText(giveaway *tele.Giveaway) string {
	if giveaway == nil {
		return "[赠品] 描述= 数量=0 截止="
	}
	deadline := ""
	if giveaway.SelectionUnixtime > 0 {
		deadline = time.Unix(giveaway.SelectionUnixtime, 0).UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("[赠品] 描述=%s 数量=%d 截止=%s", strings.TrimSpace(giveaway.PrizeDescription), giveaway.WinnerCount, deadline)
}

func giveawayWinnersReviewText(winners *tele.GiveawayWinners) string {
	if winners == nil {
		return "[赠品] 描述=winners 数量=0 截止="
	}
	deadline := ""
	if winners.SelectionUnixtime > 0 {
		deadline = time.Unix(winners.SelectionUnixtime, 0).UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("[赠品] 描述=%s 数量=%d 截止=%s", strings.TrimSpace(winners.PrizeDescription), winners.WinnerCount, deadline)
}

func contactTelegramIdentity(contact *tele.Contact) string {
	if contact == nil {
		return ""
	}
	if contact.UserID != 0 {
		return strconv.FormatInt(contact.UserID, 10)
	}
	return ""
}

func documentReviewText(document *tele.Document) string {
	parts := []string{"[文件]"}
	if document == nil {
		return strings.Join(parts, " ")
	}
	if value := strings.TrimSpace(document.FileName); value != "" {
		parts = append(parts, "filename="+value)
	}
	if value := strings.TrimSpace(document.MIME); value != "" {
		parts = append(parts, "mime="+value)
	}
	return strings.Join(parts, " ")
}

func videoReviewText(video *tele.Video) string {
	parts := []string{"[视频]"}
	if video == nil {
		return strings.Join(parts, " ")
	}
	if value := strings.TrimSpace(video.FileName); value != "" {
		parts = append(parts, "filename="+value)
	}
	if value := strings.TrimSpace(video.MIME); value != "" {
		parts = append(parts, "mime="+value)
	}
	return strings.Join(parts, " ")
}

func animationReviewText(animation *tele.Animation) string {
	parts := []string{"[动图]"}
	if animation == nil {
		return strings.Join(parts, " ")
	}
	if value := strings.TrimSpace(animation.FileName); value != "" {
		parts = append(parts, "filename="+value)
	}
	if value := strings.TrimSpace(animation.MIME); value != "" {
		parts = append(parts, "mime="+value)
	}
	return strings.Join(parts, " ")
}

func voiceReviewText(voice *tele.Voice) string {
	parts := []string{"[语音]"}
	if voice == nil {
		return strings.Join(parts, " ")
	}
	if value := strings.TrimSpace(voice.MIME); value != "" {
		parts = append(parts, "mime="+value)
	}
	return strings.Join(parts, " ")
}

func audioReviewText(audio *tele.Audio) string {
	parts := []string{"[音频]"}
	if audio == nil {
		return strings.Join(parts, " ")
	}
	if value := strings.TrimSpace(audio.FileName); value != "" {
		parts = append(parts, "filename="+value)
	}
	if value := strings.TrimSpace(audio.MIME); value != "" {
		parts = append(parts, "mime="+value)
	}
	if value := strings.TrimSpace(audio.Title); value != "" {
		parts = append(parts, "title="+value)
	}
	if value := strings.TrimSpace(audio.Performer); value != "" {
		parts = append(parts, "performer="+value)
	}
	return strings.Join(parts, " ")
}

func stickerReviewText(sticker *tele.Sticker) string {
	parts := []string{"[贴纸]"}
	if sticker == nil {
		return strings.Join(parts, " ")
	}
	if value := strings.TrimSpace(sticker.Emoji); value != "" {
		parts = append(parts, "emoji="+value)
	}
	if value := strings.TrimSpace(sticker.SetName); value != "" {
		parts = append(parts, "set="+value)
	}
	if value := strings.TrimSpace(sticker.CustomEmoji); value != "" {
		parts = append(parts, "custom_emoji="+value)
	}
	return strings.Join(parts, " ")
}

func contactReviewText(contact *tele.Contact) string {
	parts := []string{"[联系人]"}
	if contact == nil {
		return strings.Join(parts, " ")
	}
	name := strings.TrimSpace(contact.FirstName + " " + contact.LastName)
	if name != "" {
		parts = append(parts, "name="+name)
	}
	if value := strings.TrimSpace(contact.PhoneNumber); value != "" {
		parts = append(parts, "phone="+value)
	}
	if value := contactTelegramIdentity(contact); value != "" {
		parts = append(parts, "telegram_user="+value)
	}
	return strings.Join(parts, " ")
}

func pollReviewText(poll *tele.Poll) string {
	parts := []string{"[投票]"}
	if poll == nil {
		return strings.Join(parts, " ")
	}
	if value := strings.TrimSpace(poll.Question); value != "" {
		parts = append(parts, "question="+value)
	}
	for idx, option := range poll.Options {
		if value := strings.TrimSpace(option.Text); value != "" {
			parts = append(parts, fmt.Sprintf("option%d=%s", idx+1, value))
		}
	}
	if value := strings.TrimSpace(poll.Explanation); value != "" {
		parts = append(parts, "explanation="+value)
	}
	return strings.Join(parts, " ")
}

func diceReviewText(dice *tele.Dice) string {
	if dice == nil {
		return "[骰子]"
	}
	return fmt.Sprintf("[骰子] emoji=%s value=%d", dice.Type, dice.Value)
}

func locationReviewText(location *tele.Location) string {
	if location == nil {
		return "[位置]"
	}
	return fmt.Sprintf("[位置] lat=%.6g lng=%.6g", location.Lat, location.Lng)
}

func venueReviewText(venue *tele.Venue) string {
	parts := []string{"[地点]"}
	if venue == nil {
		return strings.Join(parts, " ")
	}
	if value := strings.TrimSpace(venue.Title); value != "" {
		parts = append(parts, "title="+value)
	}
	if value := strings.TrimSpace(venue.Address); value != "" {
		parts = append(parts, "address="+value)
	}
	parts = append(parts, fmt.Sprintf("lat=%.6g lng=%.6g", venue.Location.Lat, venue.Location.Lng))
	return strings.Join(parts, " ")
}

func gameReviewText(game *tele.Game) string {
	parts := []string{"[游戏]"}
	if game == nil {
		return strings.Join(parts, " ")
	}
	if value := strings.TrimSpace(game.Title); value != "" {
		parts = append(parts, "title="+value)
	}
	if value := strings.TrimSpace(game.Description); value != "" {
		parts = append(parts, "description="+value)
	}
	return strings.Join(parts, " ")
}

func invoiceReviewText(invoice *tele.Invoice) string {
	parts := []string{"[Invoice]"}
	if invoice == nil {
		return strings.Join(parts, " ")
	}
	if value := strings.TrimSpace(invoice.Title); value != "" {
		parts = append(parts, "title="+value)
	}
	if value := strings.TrimSpace(invoice.Description); value != "" {
		parts = append(parts, "description="+value)
	}
	if invoice.Total != 0 {
		parts = append(parts, fmt.Sprintf("total=%d", invoice.Total))
	}
	if value := strings.TrimSpace(invoice.Currency); value != "" {
		parts = append(parts, "currency="+value)
	}
	return strings.Join(parts, " ")
}

func quotedMessageSource(msg *tele.Message) string {
	if msg == nil || msg.Sender == nil {
		return "unknown"
	}
	if value := strings.TrimSpace(msg.Sender.Username); value != "" {
		return "@" + value
	}
	return strconv.FormatInt(msg.Sender.ID, 10)
}

func (s *Service) applyAIAction(ctx context.Context, msg *tele.Message, policy config.GuardPolicy, trust store.UserTrust, output ai.CheckOutput, action string, isEdited bool) error {
	auditOutcome := "success"
	auditError := ""
	defer func() {
		if action == "none" {
			return
		}
		s.writeModerationAudit(ctx, "ai", msg.Chat, msg.Sender, "ai_"+action, output.Verdict.Reason, map[string]any{
			"message_id": msg.ID,
			"verdict":    output.Verdict.Verdict,
			"category":   output.Verdict.Category,
			"confidence": output.Verdict.Confidence,
			"model":      output.Model,
			"outcome":    auditOutcome,
			"error":      auditError,
			"edited":     isEdited,
		})
	}()

	checkedDelta := int32(1)
	cleanDelta := int32(0)
	nextStatus := trust.Status
	score := trust.Score
	shouldSendActionFeedback := true

	switch action {
	case "ban":
		deleteRelease, deleteOK := s.acquireMessageDeleteLock(msg.Chat.ID, msg.ID)
		if deleteOK {
			defer deleteRelease()
			if err := s.deleteMessage(msg); err != nil {
				return err
			}
		} else {
			s.logger.Info("skip duplicate ai ban message delete", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
		}
		actionRelease, actionOK := s.acquireUserActionLock(msg.Chat.ID, msg.Sender.ID)
		if actionOK {
			defer actionRelease()
			if err := s.banUser(msg.Chat, msg.Sender); err != nil {
				auditOutcome = "failed"
				auditError = redact.ErrorString(err)
				return err
			}
		} else {
			s.logger.Info("skip duplicate ai ban user action", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
			auditOutcome = "deduped"
			return nil
		}
		nextStatus = "banned"
		score = 0
	case "mute":
		deleteRelease, deleteOK := s.acquireMessageDeleteLock(msg.Chat.ID, msg.ID)
		if deleteOK {
			defer deleteRelease()
			if err := s.deleteMessage(msg); err != nil {
				return err
			}
		} else {
			s.logger.Info("skip duplicate ai mute message delete", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
		}
		actionRelease, actionOK := s.acquireUserActionLock(msg.Chat.ID, msg.Sender.ID)
		if actionOK {
			defer actionRelease()
			if err := s.muteUser(msg.Chat, msg.Sender, 600); err != nil {
				auditOutcome = "failed"
				auditError = redact.ErrorString(err)
				return err
			}
		} else {
			s.logger.Info("skip duplicate ai mute user action", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
			auditOutcome = "deduped"
			return nil
		}
		nextStatus = "suspicious"
		score = 0.2
	case "warn":
		deleteRelease, deleteOK := s.acquireMessageDeleteLock(msg.Chat.ID, msg.ID)
		if deleteOK {
			defer deleteRelease()
			if err := s.deleteMessage(msg); err != nil {
				s.logger.Warn("delete message on warn failed", zap.Error(err))
			}
		} else {
			s.logger.Info("skip duplicate ai warn message delete", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
		}
		actionRelease, actionOK := s.acquireUserActionLock(msg.Chat.ID, msg.Sender.ID)
		if actionOK {
			defer actionRelease()
			if _, _, err := s.IncrWarning(ctx, msg.Chat, msg.Sender, "ai_"+output.Verdict.Verdict, policy, true, true); err != nil {
				auditOutcome = "failed"
				auditError = redact.ErrorString(err)
				return err
			}
		} else {
			s.logger.Info("skip duplicate ai warn user action", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
			auditOutcome = "deduped"
			return nil
		}
		nextStatus = "suspicious"
		score = 0.3
	case "flag":
		nextStatus = "suspicious"
		score = 0.4
	case "delete":
		deleteRelease, deleteOK := s.acquireMessageDeleteLock(msg.Chat.ID, msg.ID)
		if deleteOK {
			defer deleteRelease()
			if err := s.deleteMessage(msg); err != nil {
				return err
			}
		} else {
			s.logger.Info("skip duplicate ai delete message delete", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
		}
		actionRelease, actionOK := s.acquireUserActionLock(msg.Chat.ID, msg.Sender.ID)
		if actionOK {
			defer actionRelease()
		} else {
			s.logger.Info("skip duplicate ai delete user action", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
			auditOutcome = "deduped"
			return nil
		}
		nextStatus = "suspicious"
		score = 0.3
	default:
		cleanDelta = 1
		score = minFloat64(1, trust.Score+0.05)
	}

	s.maybeBanBotInviterAfterViolation(ctx, msg, policy, action, output.Verdict.Reason)

	updated := trust
	if !isEdited {
		var err error
		updated, err = s.queries.IncrementUserTrustCounters(ctx, store.IncrementUserTrustCountersParams{
			ChatID:       msg.Chat.ID,
			UserID:       msg.Sender.ID,
			CheckedDelta: checkedDelta,
			CleanDelta:   cleanDelta,
			Score:        score,
		})
		if err != nil {
			return err
		}
	}

	if nextStatus != "" && nextStatus != updated.Status {
		var err error
		now := time.Now()
		var graduatedAt *time.Time
		var bannedAt *time.Time
		var bannedReason []byte
		if nextStatus == "trusted" {
			graduatedAt = &now
		}
		if nextStatus == "banned" {
			bannedAt = &now
			bannedReason = buildUserTrustBanReason(output.Verdict.Verdict, output.Verdict.Reason, "ai")
		}
		updated, err = s.queries.UpdateUserTrustStatus(ctx, store.UpdateUserTrustStatusParams{
			ChatID:       msg.Chat.ID,
			UserID:       msg.Sender.ID,
			Status:       nextStatus,
			Score:        score,
			GraduatedAt:  graduatedAt,
			BannedAt:     bannedAt,
			BannedReason: bannedReason,
			Notes:        stringPtr(output.Verdict.Reason),
		})
		if err != nil {
			return err
		}
		if updated.Status == "trusted" {
			s.restoreTrustedUserPermissions(updated, "ai_status_update")
		} else if isUngraduatedTrustStatus(updated.Status) {
			s.applyUngraduatedPermissionRestriction(msg.Chat, msg.Sender, policy, updated, "ai_status_update")
		}
	}

	if action == "none" {
		if isEdited {
			return nil
		}
		return s.maybeGraduateUser(ctx, updated, policy.AI)
	}

	// AI 动作反馈
	if shouldSendActionFeedback {
		s.dispatchAIActionFeedback(msg, policy, action, output)
	}

	s.resetTrustAfterViolation(ctx, msg, action, stringPtr(output.Verdict.Reason))
	return nil
}

func (s *Service) maybeBanBotInviterAfterViolation(ctx context.Context, msg *tele.Message, policy config.GuardPolicy, action string, reason string) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil || !isOtherBot(msg.Sender, s.bot) {
		return
	}
	if !policy.Filter.BanBotInviterOnViolation {
		return
	}
	normalized := normalizeBotInviterPenaltyAction(action)
	if normalized == "" {
		return
	}

	outcome := "skipped"
	auditReason := reason
	inviter, trust, err := s.botInviterFromTrust(ctx, msg.Chat.ID, msg.Sender.ID)
	if err != nil {
		outcome = "failed"
		auditReason = redact.ErrorString(err)
		s.logger.Warn("load bot inviter failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("bot_user_id", msg.Sender.ID))
		s.writeBotInviterAudit(ctx, msg, nil, action, outcome, auditReason)
		return
	}
	if trust != nil && !trust.IsBot {
		outcome = "skipped"
		auditReason = "target is not tracked as bot"
		s.writeBotInviterAudit(ctx, msg, nil, action, outcome, auditReason)
		return
	}
	if inviter == nil || !s.validBotInviter(inviter, msg.Sender) {
		if auditReason == "" {
			auditReason = "missing valid inviter"
		}
		s.writeBotInviterAudit(ctx, msg, inviter, action, outcome, auditReason)
		return
	}

	if err := s.banUser(msg.Chat, inviter); err != nil {
		outcome = "failed"
		auditReason = redact.ErrorString(err)
		s.logger.Warn("ban bot inviter failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("bot_user_id", msg.Sender.ID), zap.Int64("inviter_user_id", inviter.ID))
		s.writeBotInviterAudit(ctx, msg, inviter, action, outcome, auditReason)
		return
	}

	outcome = "success"
	if auditReason == "" {
		auditReason = "邀请违规 Bot 连坐"
	}
	s.announceBotInviterBan(ctx, msg.Chat, inviter, msg.Sender)
	s.writeBotInviterAudit(ctx, msg, inviter, action, outcome, auditReason)
	_ = normalized
}

func normalizeBotInviterPenaltyAction(action string) string {
	switch strings.TrimSpace(strings.ToLower(action)) {
	case "ban", "delete_ban":
		return "ban"
	case "kick":
		return "kick"
	default:
		return ""
	}
}

func (s *Service) botInviterFromTrust(ctx context.Context, chatID int64, botUserID int64) (*tele.User, *store.UserTrust, error) {
	trust, err := s.queries.GetUserTrust(ctx, chatID, botUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	inviter := parseBotInviterFromNotes(trust.Notes)
	return inviter, &trust, nil
}

func parseBotInviterFromNotes(notes *string) *tele.User {
	if notes == nil || strings.TrimSpace(*notes) == "" {
		return nil
	}
	var parsed botInviteTrustNotes
	if err := json.Unmarshal([]byte(*notes), &parsed); err != nil || parsed.Inviter == nil || parsed.Inviter.UserID == 0 {
		return nil
	}
	return &tele.User{
		ID:        parsed.Inviter.UserID,
		IsBot:     parsed.Inviter.IsBot,
		Username:  parsed.Inviter.Username,
		FirstName: parsed.Inviter.FirstName,
		LastName:  parsed.Inviter.LastName,
	}
}

func (s *Service) validBotInviter(inviter *tele.User, botUser *tele.User) bool {
	if inviter == nil || botUser == nil {
		return false
	}
	if inviter.IsBot || inviter.ID == botUser.ID {
		return false
	}
	return s.bot == nil || s.bot.Me == nil || inviter.ID != s.bot.Me.ID
}

func (s *Service) announceBotInviterBan(ctx context.Context, chat *tele.Chat, inviter *tele.User, botUser *tele.User) {
	if chat == nil || inviter == nil || botUser == nil {
		return
	}
	text := fmt.Sprintf("🚫 %s 已被封禁，原因：邀请违规 Bot 连坐（违规 Bot：%s）", htmlEscape(userDisplayForOwner(inviter)), htmlEscape(userDisplayForOwner(botUser)))
	if _, err := s.sendThrottled(ctx, chat, text, &tele.SendOptions{ParseMode: tele.ModeHTML}); err != nil {
		s.logger.Warn("send bot inviter ban announcement failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("inviter_user_id", inviter.ID), zap.Int64("bot_user_id", botUser.ID))
	}
}

func (s *Service) writeBotInviterAudit(ctx context.Context, msg *tele.Message, inviter *tele.User, originalAction string, outcome string, reason string) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return
	}
	chatID := msg.Chat.ID
	before := map[string]any{
		"source": "system:bot_inviter_liability",
		"target": botInviterAuditUser(inviter),
		"bot":    botInviterAuditUser(msg.Sender),
	}
	after := map[string]any{
		"source":          "system:bot_inviter_liability",
		"action":          "ban_bot_inviter",
		"reason":          reason,
		"message_id":      msg.ID,
		"original_action": originalAction,
		"outcome":         outcome,
		"target":          botInviterAuditUser(inviter),
		"bot":             botInviterAuditUser(msg.Sender),
	}
	s.WriteRuntimeAudit(ctx, "moderation", &chatID, "ban_bot_inviter", before, after)
}

func botInviterAuditUser(user *tele.User) map[string]any {
	if user == nil {
		return nil
	}
	return map[string]any{
		"user_id":    user.ID,
		"username":   user.Username,
		"first_name": user.FirstName,
		"last_name":  user.LastName,
		"display":    displayName(user),
	}
}

func (s *Service) writeModerationAudit(ctx context.Context, source string, chat *tele.Chat, target *tele.User, action string, reason string, extra map[string]any) {
	if chat == nil || target == nil {
		return
	}
	chatID := chat.ID
	before := map[string]any{
		"source": source,
		"chat": map[string]any{
			"id":       chat.ID,
			"title":    chat.Title,
			"username": chat.Username,
			"type":     chat.Type,
		},
		"target": map[string]any{
			"user_id":    target.ID,
			"username":   target.Username,
			"first_name": target.FirstName,
			"last_name":  target.LastName,
			"display":    displayName(target),
		},
	}
	after := map[string]any{
		"source": source,
		"action": action,
		"reason": reason,
	}
	for key, value := range extra {
		after[key] = value
	}
	s.WriteRuntimeAudit(ctx, "moderation", &chatID, action, before, after)
}

// dispatchAIActionFeedback 为 AI 触发的动作发送群内反馈
func (s *Service) dispatchAIActionFeedback(
	msg *tele.Message,
	policy config.GuardPolicy,
	action string,
	output ai.CheckOutput,
) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return
	}
	category := output.Verdict.Verdict
	reason := output.Verdict.Reason
	if reason == "" {
		reason = "AI 判定为 " + category
	}

	fb := policy.Feedback
	vars := map[string]string{
		"user":         feedbackUserLabel(msg.Sender, fb.DeleteMsg.ParseMode),
		"user_mention": feedbackUserMention(msg.Sender, fb.DeleteMsg.ParseMode),
		"group":        msg.Chat.Title,
		"reason":       reason,
		"category":     category,
		"matched":      category,
		"keyword":      category,
	}
	switch action {
	case "delete":
		s.sendActionFeedback(msg.Chat, nil, fb.DeleteMsg, vars)
	case "mute":
		vars["user"] = feedbackUserLabel(msg.Sender, fb.Mute.ParseMode)
		vars["user_mention"] = feedbackUserMention(msg.Sender, fb.Mute.ParseMode)
		vars["duration"] = "10分钟"
		s.sendActionFeedback(msg.Chat, nil, fb.Mute, vars)
	case "ban":
		vars["user"] = feedbackUserLabel(msg.Sender, fb.Ban.ParseMode)
		vars["user_mention"] = feedbackUserMention(msg.Sender, fb.Ban.ParseMode)
		s.sendActionFeedback(msg.Chat, nil, fb.Ban, vars)
		// warn 走 IncrWarning，里面已经发了 Warn feedback
	}
}

func (s *Service) resetTrustAfterViolation(ctx context.Context, msg *tele.Message, action string, notes *string) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return
	}
	if !isViolationAction(action) {
		return
	}

	trust, err := s.queries.GetUserTrust(ctx, msg.Chat.ID, msg.Sender.ID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn("load user trust before reset failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID))
			return
		}
		trust, err = s.queries.UpsertUserTrust(ctx, store.UpsertUserTrustParams{
			ChatID:          msg.Chat.ID,
			UserID:          msg.Sender.ID,
			Username:        userFieldPtr(msg.Sender.Username),
			FirstName:       userFieldPtr(msg.Sender.FirstName),
			LastName:        userFieldPtr(msg.Sender.LastName),
			JoinedAt:        time.Now(),
			Status:          "new",
			Score:           0.5,
			MessagesChecked: 0,
			MessagesClean:   0,
			IsBot:           isOtherBot(msg.Sender, s.bot),
		})
		if err != nil {
			s.logger.Warn("create user trust before reset failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID))
			return
		}
	}

	score := trust.Score
	nextStatus := trust.Status
	switch normalizeTrustPenaltyAction(action) {
	case "ban":
		nextStatus = "banned"
		score = 0
	case "warn", "mute":
		nextStatus = "suspicious"
		score = minFloat64(score, 0.3)
	case "delete":
		score = minFloat64(score, 0.3)
	}

	if _, err := s.queries.ResetUserTrustClean(ctx, store.ResetUserTrustCleanParams{
		ChatID: msg.Chat.ID,
		UserID: msg.Sender.ID,
		Score:  score,
	}); err != nil {
		s.logger.Warn("reset user trust clean count failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.String("action", action))
	}

	if nextStatus != trust.Status || score != trust.Score {
		var graduatedAt *time.Time
		var bannedAt *time.Time
		var bannedReason []byte
		if nextStatus == "trusted" {
			now := time.Now()
			graduatedAt = &now
		}
		if nextStatus == "banned" {
			now := time.Now()
			bannedAt = &now
			rule, matched, source := parseUserTrustBanMeta(notes, "ai")
			bannedReason = buildUserTrustBanReason(rule, matched, source)
		}
		if _, err := s.queries.UpdateUserTrustStatus(ctx, store.UpdateUserTrustStatusParams{
			ChatID:       msg.Chat.ID,
			UserID:       msg.Sender.ID,
			Status:       nextStatus,
			Score:        score,
			GraduatedAt:  graduatedAt,
			BannedAt:     bannedAt,
			BannedReason: bannedReason,
			Notes:        notes,
		}); err != nil {
			s.logger.Warn("update user trust after violation failed", zap.Error(err), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.String("action", action))
		}
	}
}

func isViolationAction(action string) bool {
	switch normalizeTrustPenaltyAction(action) {
	case "delete", "warn", "mute", "ban":
		return true
	default:
		return false
	}
}

func normalizeTrustPenaltyAction(action string) string {
	switch strings.TrimSpace(strings.ToLower(action)) {
	case "delete_warn", "delete_and_warn", "warn":
		return "warn"
	case "delete_mute", "mute":
		return "mute"
	case "delete_ban", "ban":
		return "ban"
	case "delete":
		return "delete"
	default:
		return strings.TrimSpace(strings.ToLower(action))
	}
}

func (s *Service) maybeGraduateUser(ctx context.Context, trust store.UserTrust, policy config.AIPolicy) error {
	if trust.Status == "trusted" || trust.Status == "banned" {
		return nil
	}
	if trust.IsBot {
		return nil
	}
	if int(trust.MessagesClean) < policy.GraduateAfterMessages {
		return nil
	}
	now := time.Now()
	updated, err := s.queries.UpdateUserTrustStatus(ctx, store.UpdateUserTrustStatusParams{
		ChatID:      trust.ChatID,
		UserID:      trust.UserID,
		Status:      "trusted",
		Score:       maxFloat64(trust.Score, 0.95),
		GraduatedAt: &now,
		Notes:       stringPtr("graduated by ai trust policy"),
	})
	if err == nil {
		s.restoreTrustedUserPermissions(updated, "ai_graduation")
		if pol, e := config.LoadPolicy(ctx, s.queries, trust.ChatID); e == nil {
			chat := &tele.Chat{ID: trust.ChatID}
			user := userFromTrust(trust)
			days := int(time.Since(trust.JoinedAt).Hours() / 24)
			s.sendActionFeedback(chat, nil, pol.Feedback.TrustGraduated, map[string]string{
				"user":         feedbackUserLabel(user, pol.Feedback.TrustGraduated.ParseMode),
				"user_mention": feedbackUserMention(user, pol.Feedback.TrustGraduated.ParseMode),
				"days":         strFormatInt(int64(days)),
				"messages":     strFormatInt(int64(trust.MessagesClean)),
			})
		}
	}
	return err
}

// userFromTrust 把 store.UserTrust 还原成 *tele.User，带上 Username/FirstName/LastName，
// 便于下游 displayName / mention / feedback 变量渲染。用于只有 trust 记录的路径（比如毕业通知）。
func userFromTrust(trust store.UserTrust) *tele.User {
	u := &tele.User{ID: trust.UserID}
	if trust.Username != nil {
		u.Username = *trust.Username
	}
	if trust.FirstName != nil {
		u.FirstName = *trust.FirstName
	}
	if trust.LastName != nil {
		u.LastName = *trust.LastName
	}
	return u
}

func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func (s *Service) IncrWarning(ctx context.Context, chat *tele.Chat, user *tele.User, reason string, policy config.GuardPolicy, sendFeedback bool, userActionLocked bool) (int, bool, error) {
	if chat == nil || user == nil {
		return 0, false, nil
	}
	if paused, err := s.actionsPaused(ctx); err == nil && paused {
		s.logger.Info("skip warning because actions are paused", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID), zap.String("reason", reason))
		return 0, false, nil
	} else if err != nil {
		s.logger.Warn("load system state failed before warning", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
	}

	if _, err := s.queries.InsertWarning(ctx, store.InsertWarningParams{
		ChatID: chat.ID,
		UserID: user.ID,
		Reason: stringPtr(truncateString(reason, 200)),
	}); err != nil {
		return 0, false, fmt.Errorf("insert warning: %w", err)
	}

	activeWarnings, err := s.queries.GetActiveWarnings(ctx, store.GetActiveWarningsParams{
		ChatID:       chat.ID,
		UserID:       user.ID,
		DecaySeconds: int64(policy.Warnings.DecayDays * 86400),
	})
	if err != nil {
		return 0, false, fmt.Errorf("list active warnings: %w", err)
	}

	count := len(activeWarnings)

	if sendFeedback {
		s.sendActionFeedback(chat, nil, policy.Feedback.Warn, map[string]string{
			"user":         feedbackUserLabel(user, policy.Feedback.Warn.ParseMode),
			"user_mention": feedbackUserMention(user, policy.Feedback.Warn.ParseMode),
			"reason":       humanReason(reason, ""),
			"current":      strFormatInt(int64(count)),
			"limit":        strFormatInt(int64(policy.Warnings.MaxWarns)),
		})
	}

	if !policy.Warnings.Enabled || count < policy.Warnings.MaxWarns {
		return count, false, nil
	}

	if err := s.escalateWarnings(ctx, chat, user, policy, userActionLocked, !userActionLocked); err != nil {
		return count, false, err
	}

	if err := s.queries.MarkWarningsConsumed(ctx, store.MarkWarningsConsumedParams{
		ChatID:       chat.ID,
		UserID:       user.ID,
		DecaySeconds: int64(policy.Warnings.DecayDays * 86400),
	}); err != nil {
		return count, false, fmt.Errorf("consume warnings: %w", err)
	}

	return count, true, nil
}

func (s *Service) escalateWarnings(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy, userActionLocked bool, sendEscalationFeedback bool) error {
	auditOutcome := "success"
	auditError := ""
	defer func() {
		action := policy.Warnings.ActionAtMax
		if action == "" {
			action = config.DefaultPolicy.Warnings.ActionAtMax
		}
		s.writeModerationAudit(ctx, "warning_escalation", chat, user, "warning_"+action, "达到警告上限", map[string]any{
			"warnings": policy.Warnings.MaxWarns,
			"outcome":  auditOutcome,
			"error":    auditError,
		})
	}()
	if !userActionLocked {
		actionRelease, actionOK := s.acquireUserActionLock(chat.ID, user.ID)
		if !actionOK {
			s.logger.Info("skip duplicate warnings escalation user action", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
			auditOutcome = "skipped"
			return nil
		}
		defer actionRelease()
	}

	action := policy.Warnings.ActionAtMax
	if action == "" {
		action = config.DefaultPolicy.Warnings.ActionAtMax
	}

	switch action {
	case "mute":
		if err := s.muteUser(chat, user, 3600); err != nil {
			auditOutcome = "failed"
			auditError = redact.ErrorString(err)
			return fmt.Errorf("mute warned user: %w", err)
		}
	case "mute_5m":
		if err := s.muteUser(chat, user, 300); err != nil {
			auditOutcome = "failed"
			auditError = redact.ErrorString(err)
			return fmt.Errorf("mute warned user: %w", err)
		}
	case "mute_1h":
		if err := s.muteUser(chat, user, 3600); err != nil {
			auditOutcome = "failed"
			auditError = redact.ErrorString(err)
			return fmt.Errorf("mute warned user: %w", err)
		}
	case "kick":
		if err := s.kickUser(chat, user); err != nil {
			auditOutcome = "failed"
			auditError = redact.ErrorString(err)
			return fmt.Errorf("kick warned user: %w", err)
		}
	case "ban":
		if err := s.banUser(chat, user); err != nil {
			auditOutcome = "failed"
			auditError = redact.ErrorString(err)
			return fmt.Errorf("ban warned user: %w", err)
		}
	default:
		auditOutcome = "failed"
		auditError = fmt.Sprintf("unknown warnings escalate action %q", action)
		return fmt.Errorf("unknown warnings escalate action %q", action)
	}

	if sendEscalationFeedback {
		// 升级动作反馈
		vars := map[string]string{
			"user":         feedbackUserLabel(user, policy.Feedback.Mute.ParseMode),
			"user_mention": feedbackUserMention(user, policy.Feedback.Mute.ParseMode),
			"reason":       "达到警告上限",
		}
		switch action {
		case "mute", "mute_1h":
			vars["duration"] = "1小时"
			s.sendActionFeedback(chat, nil, policy.Feedback.Mute, vars)
		case "mute_5m":
			vars["duration"] = "5分钟"
			s.sendActionFeedback(chat, nil, policy.Feedback.Mute, vars)
		case "kick":
			vars["user"] = feedbackUserLabel(user, policy.Feedback.Kick.ParseMode)
			vars["user_mention"] = feedbackUserMention(user, policy.Feedback.Kick.ParseMode)
			s.sendActionFeedback(chat, nil, policy.Feedback.Kick, vars)
		case "ban":
			vars["user"] = feedbackUserLabel(user, policy.Feedback.Ban.ParseMode)
			vars["user_mention"] = feedbackUserMention(user, policy.Feedback.Ban.ParseMode)
			s.sendActionFeedback(chat, nil, policy.Feedback.Ban, vars)
		}
	}

	payload, _ := json.Marshal(map[string]any{
		"warnings": policy.Warnings.MaxWarns,
		"action":   action,
	})

	if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
		ChatID:      chat.ID,
		UserID:      user.ID,
		Username:    stringPtr(user.Username),
		Rule:        "warnings_threshold",
		Action:      action,
		MessageText: stringPtr(string(payload)),
	}); err != nil {
		return fmt.Errorf("insert warnings threshold violation: %w", err)
	}

	return nil
}

func (s *Service) isChatAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	cacheKey := "chat_admin:" + strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(userID, 10)
	if s.redis != nil {
		cached, err := s.redis.Get(ctx, cacheKey).Result()
		if err == nil && cached == "0" {
			return false, nil
		}
		if err == nil && cached == "1" {
			_ = s.redis.Del(ctx, cacheKey).Err()
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			s.logger.Debug("load admin cache failed", zap.Error(err), zap.String("key", cacheKey))
		}
	}

	member, err := s.bot.ChatMemberOf(&tele.Chat{ID: chatID}, &tele.User{ID: userID})
	if err != nil {
		return false, err
	}

	isAdmin := member != nil && (member.Role == tele.Creator || member.Role == tele.Administrator)
	if s.redis != nil {
		if isAdmin {
			_ = s.redis.Del(ctx, cacheKey).Err()
		} else {
			_ = s.redis.Set(ctx, cacheKey, "0", 10*time.Second).Err()
		}
	}

	return isAdmin, nil
}

func (s *Service) deleteMessage(msg *tele.Message) error {
	if msg == nil {
		return nil
	}
	if paused, err := s.actionsPaused(context.Background()); err == nil && paused {
		s.logger.Info("skip delete because actions are paused", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("sender_id", messageSenderID(msg)), zap.Int("message_id", msg.ID))
		return nil
	} else if err != nil {
		s.logger.Warn("load system state failed before delete", zap.Error(err))
	}
	if err := s.bot.Delete(msg); err != nil {
		return normalizeTelegramActionError("delete", err)
	}
	return nil
}

func (s *Service) muteUser(chat *tele.Chat, user *tele.User, seconds int) error {
	if chat == nil || user == nil {
		return nil
	}
	if paused, err := s.actionsPaused(context.Background()); err == nil && paused {
		s.logger.Info("skip mute because actions are paused", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return nil
	} else if err != nil {
		s.logger.Warn("load system state failed before mute", zap.Error(err))
	}
	if seconds <= 0 {
		seconds = 600
	}
	member := &tele.ChatMember{
		User:            user,
		Rights:          tele.NoRights(),
		RestrictedUntil: time.Now().Add(time.Duration(seconds) * time.Second).Unix(),
	}
	if err := s.bot.Restrict(chat, member); err != nil {
		return normalizeTelegramActionError("restrict", err)
	}
	return nil
}

func (s *Service) kickUser(chat *tele.Chat, user *tele.User) error {
	if chat == nil || user == nil {
		return nil
	}
	if paused, err := s.actionsPaused(context.Background()); err == nil && paused {
		s.logger.Info("skip kick because actions are paused", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return nil
	} else if err != nil {
		s.logger.Warn("load system state failed before kick", zap.Error(err))
	}
	member := &tele.ChatMember{User: user}
	return runVerificationKick(
		func() error {
			if err := s.bot.Ban(chat, member); err != nil {
				return normalizeTelegramActionError("ban", err)
			}
			return nil
		},
		func() error {
			if err := s.bot.Unban(chat, user); err != nil {
				return normalizeTelegramActionError("unban", err)
			}
			return nil
		},
	)
}

func (s *Service) banUser(chat *tele.Chat, user *tele.User) error {
	if chat == nil || user == nil {
		return nil
	}
	if paused, err := s.actionsPaused(context.Background()); err == nil && paused {
		s.logger.Info("skip ban because actions are paused", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return nil
	} else if err != nil {
		s.logger.Warn("load system state failed before ban", zap.Error(err))
	}
	// Telegram Bot API does not expose an official "report spam" method to bots.
	// We can only ban and ask Telegram to revoke the user's recent chat messages.
	if err := s.bot.Ban(chat, &tele.ChatMember{User: user}, true); err != nil {
		return normalizeTelegramActionError("ban", err)
	}
	return nil
}

func (s *Service) banSenderChat(chat *tele.Chat, senderChat *tele.Chat) error {
	if chat == nil || senderChat == nil {
		return nil
	}
	if paused, err := s.actionsPaused(context.Background()); err == nil && paused {
		s.logger.Info("skip sender_chat ban because actions are paused", zap.Int64("chat_id", chat.ID), zap.Int64("sender_chat_id", senderChat.ID))
		return nil
	} else if err != nil {
		s.logger.Warn("load system state failed before sender_chat ban", zap.Error(err))
	}
	if err := s.bot.BanSenderChat(chat, senderChat); err != nil {
		return normalizeTelegramActionError("ban_sender_chat", err)
	}
	return nil
}

func normalizeTelegramActionError(action string, err error) error {
	if err == nil {
		return nil
	}
	safeErr := redact.Error(err)
	message := strings.ToLower(redact.ErrorString(err))
	var tgErr *tele.Error
	if errors.As(err, &tgErr) {
		message = strings.ToLower(tgErr.Description + " " + tgErr.Message)
	}
	if strings.Contains(message, "not enough rights") || strings.Contains(message, "administrator rights") || strings.Contains(message, "can't restrict") || strings.Contains(message, "can't remove") || strings.Contains(message, "have no rights") {
		switch action {
		case "delete":
			return fmt.Errorf("bot 缺删除消息权限: %w", safeErr)
		case "ban", "unban", "restrict":
			return fmt.Errorf("bot 缺 ban/禁言权限: %w", safeErr)
		}
	}
	return fmt.Errorf("telegram %s failed: %w", action, safeErr)
}

func (s *Service) actionsPaused(ctx context.Context) (bool, error) {
	state, err := s.GetSystemState(ctx)
	if err != nil {
		return false, err
	}
	return state.ActionsPaused, nil
}

const maxStoredBioLength = 500

func (s *Service) enqueueProfileCheckLog(chatID int64, user *tele.User, bio string, mode string, result string, matchedRule *string, aiConfidence *float32, aiVerdict *string) {
	if s == nil || s.queries == nil || chatID == 0 || user == nil {
		return
	}

	displayName := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	var userName *string
	if displayName != "" {
		userName = &displayName
	}

	username := strings.TrimSpace(user.Username)
	var usernamePtr *string
	if username != "" {
		if !strings.HasPrefix(username, "@") {
			username = "@" + username
		}
		usernamePtr = &username
	}

	bio = truncateString(strings.TrimSpace(bio), maxStoredBioLength)
	var bioPtr *string
	if bio != "" {
		bioPtr = &bio
	}

	params := store.InsertProfileCheckLogParams{
		ChatID:       chatID,
		UserID:       user.ID,
		UserName:     userName,
		Username:     usernamePtr,
		Bio:          bioPtr,
		CheckMode:    mode,
		Result:       result,
		MatchedRule:  matchedRule,
		AiConfidence: aiConfidence,
		AiVerdict:    aiVerdict,
	}

	go func(arg store.InsertProfileCheckLogParams) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if _, err := s.queries.InsertProfileCheckLog(ctx, arg); err != nil {
			s.logger.Warn("write profile check log failed", zap.Error(err), zap.Int64("chat_id", arg.ChatID), zap.Int64("user_id", arg.UserID), zap.String("check_mode", arg.CheckMode), zap.String("result", arg.Result))
		}
	}(params)
}

func normalizeBioAICategory(verdict, category string) string {
	category = strings.TrimSpace(category)
	if category == "" {
		return verdict
	}

	switch strings.ToLower(category) {
	case "正常", "normal", "clean", "safe", "无", "none", "n/a", "null":
		return verdict
	default:
		return category
	}
}

func (s *Service) checkProfile(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) (string, *ai.CheckOutput, error) {
	if user == nil || !policy.Verify.CheckProfile {
		return "", nil, nil
	}
	mode := strings.ToLower(strings.TrimSpace(policy.Verify.ProfileCheckMode))
	if mode == "" {
		mode = "keyword"
	}

	// off: skip checking
	if mode == "off" {
		return "", nil, nil
	}

	logMode := mode
	if logMode != "ai" {
		logMode = "keyword"
	}
	chatID := int64(0)
	if chat != nil {
		chatID = chat.ID
	}

	// gather profile text — only check bio
	privateChat, err := s.bot.ChatByID(user.ID)
	if err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "user not found") {
			s.logger.Warn("load user chat for profile check failed", zap.Error(err), zap.Int64("user_id", user.ID))
		}
		// cannot fetch bio, skip profile check
		s.enqueueProfileCheckLog(chatID, user, "", logMode, "skip", nil, nil, nil)
		return "", nil, nil
	}
	var candidates []string
	if privateChat != nil && strings.TrimSpace(privateChat.Bio) != "" {
		candidates = []string{privateChat.Bio}
	} else {
		// no bio, nothing to check
		s.enqueueProfileCheckLog(chatID, user, "", logMode, "skip", nil, nil, nil)
		return "", nil, nil
	}
	bio := strings.TrimSpace(privateChat.Bio)

	// keyword mode: existing behavior
	if logMode == "keyword" {
		for _, blacklisted := range policy.Verify.ProfileBlacklist {
			needle := strings.TrimSpace(blacklisted)
			if needle == "" {
				continue
			}
			lowerNeedle := strings.ToLower(needle)
			for _, candidate := range candidates {
				if strings.Contains(strings.ToLower(candidate), lowerNeedle) {
					s.enqueueProfileCheckLog(chatID, user, bio, "keyword", "hit", &needle, nil, nil)
					return needle, nil, nil
				}
			}
		}
		s.enqueueProfileCheckLog(chatID, user, bio, "keyword", "pass", nil, nil, nil)
		return "", nil, nil
	}

	// ai mode: call ai moderator to check a constructed profile text
	if mode == "ai" {
		if s.aiModerator == nil {
			s.logger.Warn("ai moderator not available, allow profile", zap.Int64("user_id", user.ID))
			s.enqueueProfileCheckLog(chatID, user, bio, "ai", "error", nil, nil, nil)
			return "", nil, nil
		}

		text := "[新用户简介审核] 简介=" + strings.TrimSpace(privateChat.Bio)

		output, err := s.aiModerator.CheckMessage(ctx, ai.CheckInput{
			ChatID:     chatID,
			UserID:     user.ID,
			Text:       text,
			Scene:      "bio",
			SenderName: displayName(user),
			Policy:     policy.AI,
			SkipCache:  true,
		})
		if err != nil {
			s.logger.Warn("ai profile check failed, allow profile", zap.Error(err), zap.Int64("user_id", user.ID))
			s.enqueueProfileCheckLog(chatID, user, bio, "ai", "error", nil, nil, nil)
			return "", nil, nil
		}
		if output.Skipped {
			s.enqueueProfileCheckLog(chatID, user, bio, "ai", "skip", nil, nil, nil)
			return "", nil, nil
		}
		verdict := strings.TrimSpace(strings.ToLower(output.Verdict.Verdict))
		conf := output.Verdict.Confidence
		aiConfidence := float32(conf)
		aiVerdict := output.Verdict.Verdict
		// if not clean and confidence >= warn threshold => matched
		// 只接受已知危险 verdict，避免模型返回奇怪值（如 suspicious/other）误伤用户
		validVerdicts := map[string]bool{"ad": true, "scam": true, "spam": true, "harass": true, "porn": true, "violence": true}
		if validVerdicts[verdict] && conf >= policy.AI.Thresholds.Warn {
			category := normalizeBioAICategory(verdict, output.Verdict.Category)
			aiCategory := category
			matched := verdict + "@" + aiCategory
			s.enqueueProfileCheckLog(chatID, user, bio, "ai", "hit", &matched, &aiConfidence, &aiVerdict)
			return matched, &output, nil
		}
		s.enqueueProfileCheckLog(chatID, user, bio, "ai", "pass", nil, &aiConfidence, &aiVerdict)
		return "", &output, nil
	}

	// unknown mode: fallback to keyword
	s.logger.Warn("unknown profile_check_mode, fallback to keyword", zap.String("mode", mode), zap.Int64("user_id", user.ID))
	for _, blacklisted := range policy.Verify.ProfileBlacklist {
		needle := strings.TrimSpace(blacklisted)
		if needle == "" {
			continue
		}
		lowerNeedle := strings.ToLower(needle)
		for _, candidate := range candidates {
			if strings.Contains(strings.ToLower(candidate), lowerNeedle) {
				s.enqueueProfileCheckLog(chatID, user, bio, "keyword", "hit", &needle, nil, nil)
				return needle, nil, nil
			}
		}
	}
	s.enqueueProfileCheckLog(chatID, user, bio, "keyword", "pass", nil, nil, nil)
	return "", nil, nil
}

// checkProfileOnMessage 供未毕业用户每条消息前复用的 bio 审核。
// 带 Redis 缓存（bio_cache_ttl_minutes 控制），节流 Telegram getChat 调用。
// 返回 matched=="" 表示干净；matched!="" 表示命中（关键词或 AI 判定违规）。
func (s *Service) checkProfileOnMessage(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) (string, *ai.CheckOutput, error) {
	if user == nil || chat == nil {
		return "", nil, nil
	}

	mode := strings.ToLower(strings.TrimSpace(policy.AI.ProfileOnMessageMode))
	if mode != "keyword" && mode != "ai" {
		mode = "ai"
	}

	chatID := chat.ID

	// 读 bio（带缓存）
	bio, fromCache, err := s.fetchUserBioCached(ctx, user.ID, policy.AI.BioCacheTTLMinutes)
	if err != nil {
		return "", nil, nil
	}
	if strings.TrimSpace(bio) == "" {
		s.enqueueProfileCheckLog(chatID, user, "", "on_message_"+mode, "skip", nil, nil, nil)
		return "", nil, nil
	}
	_ = fromCache

	// 关键词模式：复用 Verify.ProfileBlacklist
	if mode == "keyword" {
		for _, blacklisted := range policy.Verify.ProfileBlacklist {
			needle := strings.TrimSpace(blacklisted)
			if needle == "" {
				continue
			}
			if strings.Contains(strings.ToLower(bio), strings.ToLower(needle)) {
				s.enqueueProfileCheckLog(chatID, user, bio, "on_message_keyword", "hit", &needle, nil, nil)
				return needle, nil, nil
			}
		}
		s.enqueueProfileCheckLog(chatID, user, bio, "on_message_keyword", "pass", nil, nil, nil)
		return "", nil, nil
	}

	// AI 模式
	if s.aiModerator == nil {
		s.enqueueProfileCheckLog(chatID, user, bio, "on_message_ai", "error", nil, nil, nil)
		return "", nil, nil
	}
	text := "[未毕业用户发言前 bio 审核] 简介=" + bio
	output, err := s.aiModerator.CheckMessage(ctx, ai.CheckInput{
		ChatID:     chatID,
		UserID:     user.ID,
		Text:       text,
		Scene:      "bio",
		SenderName: displayName(user),
		Policy:     policy.AI,
	})
	if err != nil {
		s.enqueueProfileCheckLog(chatID, user, bio, "on_message_ai", "error", nil, nil, nil)
		return "", nil, nil
	}
	if output.Skipped {
		s.enqueueProfileCheckLog(chatID, user, bio, "on_message_ai", "skip", nil, nil, nil)
		return "", nil, nil
	}
	verdict := strings.TrimSpace(strings.ToLower(output.Verdict.Verdict))
	conf := output.Verdict.Confidence
	aiConf := float32(conf)
	aiVerdict := output.Verdict.Verdict
	validVerdicts := map[string]bool{"ad": true, "scam": true, "spam": true, "harass": true, "porn": true, "violence": true}
	if validVerdicts[verdict] && conf >= policy.AI.Thresholds.Warn {
		category := normalizeBioAICategory(verdict, output.Verdict.Category)
		s.enqueueProfileCheckLog(chatID, user, bio, "on_message_ai", "hit", &category, &aiConf, &aiVerdict)
		return "bio违规:" + category, &output, nil
	}
	s.enqueueProfileCheckLog(chatID, user, bio, "on_message_ai", "pass", nil, &aiConf, &aiVerdict)
	return "", &output, nil
}

func (s *Service) asyncProfileCheck(msg *tele.Message, policy config.GuardPolicy) {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return
	}

	release, ok := s.acquireProfileCheckInFlight(msg.Sender.ID)
	if !ok {
		return
	}

	go func() {
		defer release()
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("async bio check panic", zap.Any("panic", r), zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID))
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		matched, output, err := s.checkProfileOnMessage(ctx, msg.Chat, msg.Sender, policy)
		if err != nil {
			s.logger.Warn("async on-message profile check failed",
				zap.Error(err),
				zap.Int64("chat_id", msg.Chat.ID),
				zap.Int64("user_id", msg.Sender.ID),
				zap.Int("message_id", msg.ID))
			return
		}
		if matched == "" {
			return
		}

		if err := s.handleProfileOnMessageViolation(ctx, msg, policy, matched, output); err != nil {
			s.logger.Error("handle async on-message profile violation failed",
				zap.Error(err),
				zap.Int64("chat_id", msg.Chat.ID),
				zap.Int64("user_id", msg.Sender.ID),
				zap.Int("message_id", msg.ID))
		}
	}()
}

func (s *Service) acquireProfileCheckInFlight(userID int64) (func(), bool) {
	if userID <= 0 {
		return func() {}, false
	}

	if s.redis != nil {
		inflightKey := "bio_check_inflight:" + strconv.FormatInt(userID, 10)
		token, tokenErr := generateShortNonce()
		if tokenErr != nil {
			s.logger.Warn("generate bio check inflight token failed", zap.Error(tokenErr), zap.Int64("user_id", userID))
		} else {
			ok, err := s.redis.SetNX(context.Background(), inflightKey, token, 60*time.Second).Result()
			if err == nil {
				if !ok {
					return func() {}, false
				}
				return func() {
					_ = s.redis.Eval(context.Background(), redisCompareAndDeleteScript, []string{inflightKey}, token).Err()
				}, true
			}
			s.logger.Warn("acquire bio check inflight via redis failed", zap.Error(err), zap.Int64("user_id", userID))
		}
	}

	timer := time.AfterFunc(bioCheckInFlightLocalTTL, func() {
		s.bioCheckInFlight.Delete(userID)
	})
	if _, loaded := s.bioCheckInFlight.LoadOrStore(userID, timer); loaded {
		timer.Stop()
		return func() {}, false
	}
	return func() {
		timer.Stop()
		s.bioCheckInFlight.Delete(userID)
	}, true
}

var bioCheckInFlightLocalTTL = 60 * time.Second

func (s *Service) acquireUserActionLock(chatID, userID int64) (func(), bool) {
	if chatID == 0 || userID <= 0 {
		return func() {}, false
	}
	key := "user_action:" + strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(userID, 10)
	return s.acquireActionDedupeLock(key, userActionLockTTL, &s.userActionLocks, "user action", zap.Int64("chat_id", chatID), zap.Int64("user_id", userID))
}

func (s *Service) acquireMessageDeleteLock(chatID int64, msgID int) (func(), bool) {
	if chatID == 0 || msgID <= 0 {
		return func() {}, false
	}
	key := "msg_deleted:" + strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(msgID)
	return s.acquireActionDedupeLock(key, messageDeleteLockTTL, &s.messageDeleteLocks, "message delete", zap.Int64("chat_id", chatID), zap.Int("message_id", msgID))
}

func (s *Service) acquireActionDedupeLock(key string, ttl time.Duration, local *sync.Map, label string, fields ...zap.Field) (func(), bool) {
	if key == "" || ttl <= 0 || local == nil {
		return func() {}, false
	}

	if s.redis != nil {
		ok, err := s.redis.SetNX(context.Background(), key, "1", ttl).Result()
		if err == nil {
			if !ok {
				return func() {}, false
			}
			return acquireLocalDedupeLock(key, ttl, local)
		}
		logFields := append([]zap.Field{zap.Error(err), zap.String("key", key)}, fields...)
		s.logger.Warn("acquire "+label+" lock via redis failed", logFields...)
	}

	return acquireLocalDedupeLock(key, ttl, local)
}

func acquireLocalDedupeLock(key string, ttl time.Duration, local *sync.Map) (func(), bool) {
	timer := time.AfterFunc(ttl, func() {
		local.Delete(key)
	})
	if _, loaded := local.LoadOrStore(key, timer); loaded {
		timer.Stop()
		return func() {}, false
	}
	return func() {}, true
}

func (s *Service) handleProfileOnMessageViolation(
	ctx context.Context,
	msg *tele.Message,
	policy config.GuardPolicy,
	matched string,
	aiOutput *ai.CheckOutput,
) error {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return nil
	}

	actionRelease, actionOK := s.acquireUserActionLock(msg.Chat.ID, msg.Sender.ID)
	if actionOK {
		defer actionRelease()
		if err := s.banUser(msg.Chat, msg.Sender); err != nil {
			return err
		}
	} else {
		s.logger.Info("skip duplicate on-message profile user action", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
	}
	if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
		ChatID:      msg.Chat.ID,
		UserID:      msg.Sender.ID,
		Username:    stringPtr(msg.Sender.Username),
		Rule:        "profile_match_on_message",
		Matched:     stringPtr(matched),
		Action:      "ban",
		MessageText: stringPtr(truncateString(msg.Text, 2000)),
	}); err != nil {
		s.logger.Warn("insert on-message profile violation failed", zap.Error(err))
	}

	decisionMode := "on_message_ai"
	if strings.EqualFold(strings.TrimSpace(policy.AI.ProfileOnMessageMode), "keyword") {
		decisionMode = "on_message_keyword"
	}
	if err := s.recordProfileViolationDecision(ctx, msg.Chat, msg.Sender, msg, matched, decisionMode, aiOutput); err != nil {
		s.logger.Warn("record profile violation ai_decision failed", zap.Error(err))
	}

	deleteRelease, deleteOK := s.acquireMessageDeleteLock(msg.Chat.ID, msg.ID)
	if deleteOK {
		defer deleteRelease()
		if err := s.deleteMessage(msg); err != nil {
			s.logger.Warn("delete on-message profile violation trigger failed",
				zap.Error(err),
				zap.Int64("chat_id", msg.Chat.ID),
				zap.Int64("user_id", msg.Sender.ID),
				zap.Int("message_id", msg.ID))
		}
	} else {
		s.logger.Info("skip duplicate on-message profile message delete", zap.Int64("chat_id", msg.Chat.ID), zap.Int64("user_id", msg.Sender.ID), zap.Int("message_id", msg.ID))
	}

	if !actionOK {
		extra := map[string]any{
			"message_id": msg.ID,
			"outcome":    "deduped",
			"action":     "ban",
		}
		if aiOutput != nil {
			extra["verdict"] = aiOutput.Verdict.Verdict
			extra["category"] = aiOutput.Verdict.Category
			extra["confidence"] = aiOutput.Verdict.Confidence
			extra["model"] = aiOutput.Model
			extra["reason"] = aiOutput.Verdict.Reason
		}
		s.writeModerationAudit(ctx, "ai", msg.Chat, msg.Sender, "ai_ban", matched, extra)
		return nil
	}

	s.resetTrustAfterViolation(ctx, msg, "ban", stringPtr("profile_match_on_message: "+matched))
	s.sendActionFeedback(msg.Chat, nil, policy.Feedback.Ban, map[string]string{
		"user":         feedbackUserLabel(msg.Sender, policy.Feedback.Ban.ParseMode),
		"user_mention": feedbackUserMention(msg.Sender, policy.Feedback.Ban.ParseMode),
		"reason":       "资料简介违规：" + matched,
	})
	return nil
}

// fetchUserBioCached 读取用户 bio，带 Redis 缓存。
// ttlMinutes<=0 时表示不缓存，每次直接查 getChat。
// 返回 (bio, fromCache, err)
func (s *Service) fetchUserBioCached(ctx context.Context, userID int64, ttlMinutes int) (string, bool, error) {
	cacheKey := "bio:" + strconv.FormatInt(userID, 10)

	// 读缓存
	if ttlMinutes > 0 && s.redis != nil {
		if v, err := s.redis.Get(ctx, cacheKey).Result(); err == nil {
			// 约定：缓存里用 "__EMPTY__" 表示"查过、没 bio"
			if v == "__EMPTY__" {
				return "", true, nil
			}
			return v, true, nil
		} else if !errors.Is(err, redis.Nil) {
			s.logger.Debug("bio cache read failed", zap.Error(err), zap.String("key", cacheKey))
		}
	}

	// 查 Telegram
	privateChat, err := s.bot.ChatByID(userID)
	if err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "user not found") {
			s.logger.Warn("load user chat for bio failed", zap.Error(err), zap.Int64("user_id", userID))
		}
		return "", false, err
	}
	bio := ""
	if privateChat != nil {
		bio = strings.TrimSpace(privateChat.Bio)
	}

	// 写缓存
	if ttlMinutes > 0 && s.redis != nil {
		value := bio
		if value == "" {
			value = "__EMPTY__"
		}
		_ = s.redis.Set(ctx, cacheKey, value, time.Duration(ttlMinutes)*time.Minute).Err()
	}

	return bio, false, nil
}

func (s *Service) sendWelcomeMessage(ctx context.Context, chat *tele.Chat, user *tele.User) {
	if chat == nil || user == nil {
		return
	}

	policy, err := config.LoadPolicy(ctx, s.queries, chat.ID)
	if err != nil {
		s.logger.Warn("load guard policy failed for welcome message", zap.Error(err), zap.Int64("chat_id", chat.ID))
		return
	}
	welcome := policy.Verify.WelcomeMessage
	if !welcome.Enabled || welcome.Template == nil || strings.TrimSpace(*welcome.Template) == "" {
		return
	}

	group, err := s.queries.GetGroupByChatID(ctx, chat.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Warn("load group failed for welcome message", zap.Error(err), zap.Int64("chat_id", chat.ID))
	}

	admins, err := s.queries.ListAdmins(ctx)
	if err != nil {
		s.logger.Warn("list admins failed for welcome message", zap.Error(err), zap.Int64("chat_id", chat.ID))
		admins = []store.Admin{}
	}

	text := renderWelcomeMessage(*welcome.Template, welcomeTemplateData{
		User:       user,
		Chat:       chat,
		Group:      group,
		RulesLink:  welcome.RulesLink,
		AdminList:  buildWelcomeAdminList(admins, chat.ID),
		RenderedAt: time.Now(),
	}, welcome.ParseMode)

	if strings.TrimSpace(text) == "" {
		return
	}

	parseMode := resolveParseMode(welcome.ParseMode)

	message, err := s.sendThrottled(ctx, chat, text, &tele.SendOptions{
		ParseMode:             parseMode,
		DisableWebPagePreview: true,
	})
	if err != nil {
		s.logger.Warn("send welcome message failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return
	}

	if welcome.DeleteAfterSeconds <= 0 {
		return
	}

	s.runDelayed(time.Duration(welcome.DeleteAfterSeconds)*time.Second, func() {
		if err := s.deleteDelayedMessage(message); err != nil {
			s.logger.Warn("delete welcome message failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int("message_id", message.ID))
		}
	})
}

type welcomeTemplateData struct {
	User       *tele.User
	Chat       *tele.Chat
	Group      store.Group
	RulesLink  string
	AdminList  string
	RenderedAt time.Time
}

func renderWelcomeMessage(template string, data welcomeTemplateData, parseMode string) string {
	groupTitle := ""
	memberCount := int32(0)
	if data.Chat != nil {
		groupTitle = data.Chat.Title
	}
	if groupTitle == "" {
		groupTitle = data.Group.Title
	}
	memberCount = data.Group.MemberCount

	var userMention string
	switch resolveParseMode(parseMode) {
	case tele.ModeMarkdownV2:
		userMention = mentionMarkdownV2(data.User)
	case tele.ModeMarkdown:
		userMention = mentionMarkdownLegacy(data.User)
	default:
		userMention = mentionHTML(data.User)
	}

	rendered := RenderMessageTemplate(template, parseMode, map[string]string{
		"{user_mention}": userMention,
	}, map[string]string{
		"{user_id}":      strconv.FormatInt(data.User.ID, 10),
		"{user_name}":    displayName(data.User),
		"{group_title}":  groupTitle,
		"{group_id}":     strconv.FormatInt(data.Chat.ID, 10),
		"{member_count}": strconv.FormatInt(int64(memberCount), 10),
		"{rules_link}":   strings.TrimSpace(data.RulesLink),
		"{admin_list}":   data.AdminList,
		"{date}":         data.RenderedAt.Format("2006-01-02"),
		"{time}":         data.RenderedAt.Format("15:04"),
	})
	return rendered.Text
}

// mentionMarkdownV2 returns a MarkdownV2-compatible user mention link
func mentionMarkdownV2(user *tele.User) string {
	if user == nil {
		return "该用户"
	}
	if username := strings.TrimSpace(user.Username); username != "" {
		return mdv2EscapeChars("@" + username)
	}
	name := mdv2EscapeChars(displayName(user))
	return fmt.Sprintf("[%s](%s)", name, escapeMarkdownV2LinkTarget("tg://user?id="+itoa64(user.ID)))
}

// mentionMarkdownLegacy returns a Markdown legacy mention.
func mentionMarkdownLegacy(user *tele.User) string {
	if user == nil {
		return "该用户"
	}
	if username := strings.TrimSpace(user.Username); username != "" {
		return "@" + username
	}
	return "[" + escapeMarkdownLinkText(displayName(user)) + "](tg://user?id=" + itoa64(user.ID) + ")"
}

func escapeMarkdownLinkText(s string) string {
	replacer := strings.NewReplacer(
		`\\`, `\\\\`,
		`[`, `\[`,
		`]`, `\]`,
	)
	return replacer.Replace(s)
}

func buildWelcomeAdminList(admins []store.Admin, chatID int64) string {
	mentions := make([]string, 0)
	for _, admin := range admins {
		if admin.Role != "owner" && !adminHasScopeForChat(admin.GroupScope, chatID) {
			continue
		}
		if admin.Username != nil && strings.TrimSpace(*admin.Username) != "" {
			mentions = append(mentions, "@"+strings.TrimSpace(*admin.Username))
			continue
		}
		if admin.FirstName != nil && strings.TrimSpace(*admin.FirstName) != "" {
			mentions = append(mentions, strings.TrimSpace(*admin.FirstName))
		}
	}
	if len(mentions) == 0 {
		return "暂无"
	}
	return strings.Join(mentions, " ")
}

func adminHasScopeForChat(raw []byte, chatID int64) bool {
	if len(raw) == 0 {
		return true
	}
	var scope []int64
	if err := json.Unmarshal(raw, &scope); err != nil {
		return false
	}
	if len(scope) == 0 {
		return true
	}
	for _, item := range scope {
		if item == chatID {
			return true
		}
	}
	return false
}

func (s *Service) notifyGroup(chat *tele.Chat, format string, args ...any) {
	if chat == nil {
		return
	}
	if _, err := s.bot.Send(chat, fmt.Sprintf(format, args...), &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
	}); err != nil {
		s.logger.Warn("send moderation notice failed", zap.Error(err), zap.Int64("chat_id", chat.ID))
	}
}

func mentionHTML(user *tele.User) string {
	if user == nil {
		return "该用户"
	}
	if username := strings.TrimSpace(user.Username); username != "" {
		return htmlEscape("@" + username)
	}
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, user.ID, htmlEscape(displayName(user)))
}

func escapeMarkdownV2LinkTarget(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `)`, `\)`)
	return replacer.Replace(s)
}

// extractForwardSource returns the display name of the forward source (channel title / user name).
func extractForwardSource(msg *tele.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Origin != nil {
		if msg.Origin.SenderChat != nil {
			if source := chatDisplayName(msg.Origin.SenderChat); source != "" {
				return source
			}
		}
		if msg.Origin.Chat != nil {
			if source := chatDisplayName(msg.Origin.Chat); source != "" {
				return source
			}
		}
		if msg.Origin.Sender != nil {
			return displayName(msg.Origin.Sender)
		}
		if source := strings.TrimSpace(msg.Origin.SenderUsername); source != "" {
			return source
		}
		if source := strings.TrimSpace(msg.Origin.Signature); source != "" {
			return source
		}
	}
	// 转发自频道
	if msg.OriginalChat != nil && msg.OriginalChat.Title != "" {
		return msg.OriginalChat.Title
	}
	// 转发自用户
	if msg.OriginalSender != nil {
		return displayName(msg.OriginalSender)
	}
	// 隐私设置隐藏的转发来源
	if msg.OriginalSenderName != "" {
		return msg.OriginalSenderName
	}
	return ""
}

func chatDisplayName(chat *tele.Chat) string {
	if chat == nil {
		return ""
	}
	if title := strings.TrimSpace(chat.Title); title != "" {
		return title
	}
	if username := strings.TrimSpace(chat.Username); username != "" {
		return "@" + username
	}
	return strings.TrimSpace(chat.FirstName + " " + chat.LastName)
}

// matchesTriggerKeywords checks if a message text/caption/forward source contains any of the trigger keywords (case-insensitive fuzzy match).
func (s *Service) matchesTriggerKeywords(msg *tele.Message, keywords []string) bool {
	text := strings.ToLower(strings.TrimSpace(msg.Text))
	if text == "" {
		text = strings.ToLower(strings.TrimSpace(msg.Caption))
	}
	// Also include forward source name for matching
	forwardSrc := strings.ToLower(extractForwardSource(msg))
	combined := text + " " + forwardSrc
	if strings.TrimSpace(combined) == "" {
		return false
	}
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(combined, kw) {
			return true
		}
	}
	return false
}

func truncateString(value string, max int) string {
	if max <= 0 {
		return value
	}
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}

func stringifyCASResult(result any) *string {
	if result == nil {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	return stringPtr(string(raw))
}

func (s *Service) requirePrivateAdmin(c tele.Context) (*tele.Message, error) {
	msg := c.Message()
	if msg == nil || !msg.Private() {
		return msg, c.Send("该命令仅限管理员私聊使用", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	if _, err := s.queries.GetAdminByTelegramID(context.Background(), c.Sender().ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return msg, c.Send("你不在管理员列表中", &tele.SendOptions{ParseMode: tele.ModeHTML})
		}
		return msg, err
	}

	return msg, nil
}
