package bot

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"unicode/utf8"

	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/store"
)

const assistantMaxStickersPerGroup = 100

type assistantStickerPick struct {
	FileID      string
	Source      string
	Description string
}

func assistantStickerScore(query, target string) int {
	q := strings.ToLower(strings.Join(strings.Fields(query), ""))
	t := strings.ToLower(strings.Join(strings.Fields(target), ""))
	if q == "" || t == "" {
		return 0
	}
	score := 0
	if strings.Contains(t, q) {
		score += 20
	}
	overlap := 0
	seen := map[rune]struct{}{}
	for _, r := range q {
		seen[r] = struct{}{}
	}
	for _, r := range t {
		if _, ok := seen[r]; ok {
			overlap++
		}
	}
	shorter := utf8.RuneCountInString(q)
	if n := utf8.RuneCountInString(t); n < shorter {
		shorter = n
	}
	ratio := 0
	if shorter > 0 {
		ratio = overlap * 100 / (utf8.RuneCountInString(q) + utf8.RuneCountInString(t))
	}
	return score + overlap + ratio
}

func assistantStickerRecordText(sample store.AssistantStickerSample) string {
	parts := []string{sample.Query, sample.Emoji, sample.SetName, strings.Join(sample.Aliases, " ")}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func (a *GroupAssistant) pickAssistantSticker(ctx context.Context, chatID int64, query string, fallback []string) assistantStickerPick {
	if a == nil || a.queries == nil {
		if len(fallback) > 0 {
			return assistantStickerPick{FileID: fallback[rand.Intn(len(fallback))], Source: "fallback_pool"}
		}
		return assistantStickerPick{}
	}
	rows, err := a.queries.ListAssistantStickerSamples(ctx, chatID, assistantMaxStickersPerGroup)
	if err != nil {
		rows = nil
	}
	cleanedFallback := make([]string, 0, len(fallback))
	for _, item := range fallback {
		item = strings.TrimSpace(item)
		if item != "" {
			cleanedFallback = append(cleanedFallback, item)
		}
	}
	if len(rows) > 0 {
		q := strings.TrimSpace(query)
		if q != "" {
			bestScore := -1
			var best store.AssistantStickerSample
			for _, row := range rows {
				score := assistantStickerScore(q, assistantStickerRecordText(row))
				if score > bestScore {
					bestScore = score
					best = row
				}
			}
			if best.FileID != "" && bestScore >= 25 {
				return assistantStickerPick{FileID: best.FileID, Source: "library_match", Description: best.Query}
			}
		}
		limit := 8
		if len(rows) < limit {
			limit = len(rows)
		}
		chosen := rows[rand.Intn(limit)]
		return assistantStickerPick{FileID: chosen.FileID, Source: "library_recent", Description: chosen.Query}
	}
	if len(cleanedFallback) > 0 {
		return assistantStickerPick{FileID: cleanedFallback[rand.Intn(len(cleanedFallback))], Source: "fallback_pool"}
	}
	return assistantStickerPick{}
}

func (a *GroupAssistant) collectApprovedSticker(ctx context.Context, msg *tele.Message) {
	if a == nil || a.queries == nil || msg == nil || msg.Chat == nil || msg.Sticker == nil {
		return
	}
	fileID := strings.TrimSpace(msg.Sticker.FileID)
	if fileID == "" {
		return
	}
	sourceID := int64(msg.ID)
	_, err := a.queries.UpsertAssistantStickerSample(ctx, store.UpsertAssistantStickerSampleParams{
		ChatID: msg.Chat.ID, FileID: fileID, SourceMessageID: &sourceID,
		Query: "", Emoji: strings.TrimSpace(msg.Sticker.Emoji), SetName: strings.TrimSpace(msg.Sticker.SetName),
		Source: "group_message",
	})
	if err != nil {
		return
	}
	_ = a.queries.TrimAssistantStickerSamples(ctx, msg.Chat.ID, assistantMaxStickersPerGroup)
}

func assistantStickerFallbackIDs(policy store.GroupAssistantPolicy, settings store.AssistantGlobalSettings) []string {
	if len(policy.StickerFallbackFileIDs) > 0 {
		return policy.StickerFallbackFileIDs
	}
	return settings.StickerFallbackFileIDs
}

func (s *Service) sendAssistantSticker(ctx context.Context, chat tele.Recipient, fileID string, opts *tele.SendOptions) (*tele.Message, error) {
	sticker := &tele.Sticker{File: tele.File{FileID: fileID}}
	return s.sendThrottled(ctx, chat, sticker, opts)
}

func assistantStringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func (a *GroupAssistant) executeSendStickerTool(ctx context.Context, chatID int64, policy store.GroupAssistantPolicy, args map[string]any) (string, error) {
	if err := validateAssistantToolKeys(args, "query", "sticker_file_id", "delivery_mode"); err != nil {
		return assistantToolError("invalid_arguments", err.Error()), err
	}
	if a == nil || a.service == nil {
		return assistantToolError("missing_context", "当前上下文不支持发送贴纸"), fmt.Errorf("missing service")
	}
	fileID := assistantStringArg(args, "sticker_file_id")
	query := assistantStringArg(args, "query")
	delivery := strings.ToLower(assistantStringArg(args, "delivery_mode"))
	if delivery != "message" {
		delivery = "reply"
	}
	settings := a.loadRuntimeSettings(ctx)
	if assistantRuntime(ctx) != nil {
		settings = assistantRuntime(ctx).settings
	}
	source := "explicit"
	description := ""
	if fileID == "" {
		if query == "" && assistantRuntime(ctx) != nil {
			query = assistantRuntime(ctx).current
		}
		picked := a.pickAssistantSticker(ctx, chatID, query, assistantStickerFallbackIDs(policy, settings))
		fileID = picked.FileID
		source = picked.Source
		description = picked.Description
	}
	if fileID == "" {
		return assistantToolError("no_sticker", "当前没有可用贴纸"), fmt.Errorf("no sticker")
	}
	opts := &tele.SendOptions{}
	if assistantRuntime(ctx) != nil && assistantRuntime(ctx).msg != nil {
		opts.ThreadID = assistantRuntime(ctx).msg.ThreadID
		if delivery == "reply" {
			opts.ReplyTo = assistantRuntime(ctx).msg
		}
	}
	if rt := assistantRuntime(ctx); rt != nil && a.queries != nil && !a.service.assistantPreSendReview(ctx, rt.msg, rt.mode) {
		return assistantToolError("canceled", "消息已失效或助手已关闭"), context.Canceled
	}
	if err := assistantReserveMedia(ctx, "sticker:"+fileID); err != nil {
		return assistantToolError("already_attempted", err.Error()), err
	}
	if rt := assistantRuntime(ctx); rt != nil {
		rt.deliveryUncertain = true
	}
	sent, err := a.service.sendAssistantSticker(ctx, &tele.Chat{ID: chatID}, fileID, opts)
	if err == nil && (sent == nil || sent.ID == 0) {
		err = fmt.Errorf("贴纸发送未确认")
	}
	if err != nil {
		return assistantToolError("send_failed", "贴纸发送失败"), err
	}
	if rt := assistantRuntime(ctx); rt != nil {
		rt.stickerSent = true
		rt.deliveryUncertain = false
		if sent != nil && rt.msg != nil {
			if err := a.service.storeAssistantDelivery(ctx, rt.msg, policy, "[贴纸] "+query, sent, opts); err != nil {
				return assistantToolError("storage_failed", "贴纸已发送但归档失败，不得重发"), err
			}
		}
	}
	if a.queries != nil {
		_ = a.queries.MarkAssistantStickerSent(ctx, chatID, fileID)
	}
	return assistantJSONResult(map[string]any{"ok": true, "sticker_file_id": fileID, "query": query, "source": source, "description": description, "delivery_mode": delivery}), nil
}
