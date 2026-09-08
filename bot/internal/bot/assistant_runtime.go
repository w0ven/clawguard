package bot

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
)

type assistantToolRuntime struct {
	msg      *tele.Message
	mode     assistantReplyMode
	settings store.AssistantGlobalSettings
	roles    assistantRoleSet
	current  string
}

func assistantCosine(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func (a *GroupAssistant) embedText(ctx context.Context, roles assistantRoleSet, text string) ([]float64, bool) {
	role := roles.effective("vector")
	if strings.TrimSpace(role.ModelRef) == "" || a == nil || a.providers == nil {
		return nil, false
	}
	ref, ok := normalizeAssistantModelRef(role.ModelRef)
	if !ok {
		return nil, false
	}
	providerKey, modelName, parsed := ref.Parse()
	if !parsed {
		return nil, false
	}
	client, exists := a.providers.Client(providerKey)
	if !exists {
		return nil, false
	}
	embedder, ok := client.(ai.EmbeddingClient)
	if !ok {
		return nil, false
	}
	timeout := time.Duration(role.TimeoutSec * float64(time.Second))
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	vec, err := embedder.Embed(ctx, modelName, text, timeout)
	if err != nil || len(vec) == 0 {
		return nil, false
	}
	return vec, true
}

func (a *GroupAssistant) recallWithVector(ctx context.Context, chatID int64, query string, limit int, settings store.AssistantGlobalSettings, roles assistantRoleSet) ([]store.GroupAssistantMessage, error) {
	if a.queries == nil {
		return nil, fmt.Errorf("storage unavailable")
	}
	items, err := a.queries.ListGroupAssistantMessages(ctx, store.ListGroupAssistantMessagesParams{ChatID: chatID, Query: query, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	if !settings.MemoryRecallEnabled {
		return items, nil
	}
	queryVec, ok := a.embedText(ctx, roles, query)
	if !ok {
		return items, nil
	}
	candidates, err := a.queries.ListGroupAssistantMessages(ctx, store.ListGroupAssistantMessagesParams{ChatID: chatID, Limit: 80})
	if err != nil || len(candidates) == 0 {
		return items, nil
	}
	type scored struct {
		item  store.GroupAssistantMessage
		score float64
	}
	ranked := make([]scored, 0, len(candidates))
	for _, item := range candidates {
		vec, ok := a.embedText(ctx, roles, item.Text)
		if !ok {
			continue
		}
		score := assistantCosine(queryVec, vec)
		if score >= 0.35 {
			ranked = append(ranked, scored{item: item, score: score})
		}
	}
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].score > ranked[i].score {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}
	seen := map[int64]struct{}{}
	merged := make([]store.GroupAssistantMessage, 0, limit)
	for _, item := range items {
		seen[item.ID] = struct{}{}
		merged = append(merged, item)
	}
	for _, row := range ranked {
		if len(merged) >= limit {
			break
		}
		if _, ok := seen[row.item.ID]; ok {
			continue
		}
		seen[row.item.ID] = struct{}{}
		merged = append(merged, row.item)
	}
	return merged, nil
}

func (a *GroupAssistant) compressHotWindow(ctx context.Context, chatID int64, pool AssistantPoolConfig, policy store.GroupAssistantPolicy, history []store.GroupAssistantMessage, settings store.AssistantGlobalSettings, roles assistantRoleSet) []store.GroupAssistantMessage {
	if !settings.HotWindowCompressEnabled || settings.KeepOriginalText || len(history) < 12 {
		return history
	}
	role := roles.effective("compress")
	if strings.TrimSpace(role.ModelRef) == "" {
		return history
	}
	keep := 8
	if len(history) <= keep {
		return history
	}
	older := history[:len(history)-keep]
	recent := history[len(history)-keep:]
	lines := make([]string, 0, len(older))
	for _, item := range older {
		lines = append(lines, assistantHistoryLine(item))
	}
	result, _, err := a.dispatchPlain(ctx, chatID, "compress", pool, policy, ai.CheckRequest{
		Model: role.ModelRef, SystemPrompt: "只压缩热窗口上下文，不得删除或改写原始档案。输出不超过 800 字的中文要点。",
		Messages:  []ai.Message{{Role: "user", Content: strings.Join(lines, "\n")}},
		MaxTokens: 400, Temperature: 0.2, Timeout: 12 * time.Second,
	})
	if err != nil || result == nil || strings.TrimSpace(result.Content) == "" {
		return history
	}
	summary := store.GroupAssistantMessage{Role: "user", SenderName: "热窗口摘要", Text: "[HOT_WINDOW_SUMMARY]\n" + truncateAssistant(result.Content, 800), CreatedAt: older[0].CreatedAt}
	return append([]store.GroupAssistantMessage{summary}, recent...)
}

func (a *GroupAssistant) visionMessageParts(ctx context.Context, msg *tele.Message, current string, roles assistantRoleSet) any {
	role := roles.effective("vision")
	if strings.TrimSpace(role.ModelRef) == "" || a == nil || a.service == nil || msg == nil {
		if current == "" {
			return assistantMessageType(msg)
		}
		return current
	}
	ref, ok := normalizeAssistantModelRef(role.ModelRef)
	if !ok || a.models == nil {
		return current
	}
	model, exists := a.models.Get(ref)
	if !exists || !model.SupportsVision {
		if current == "" {
			return assistantMessageType(msg)
		}
		return current
	}
	imageB64, _, err := a.service.loadVisualForModeration(ctx, msg)
	if err != nil || imageB64 == "" {
		if current == "" {
			return assistantMessageType(msg)
		}
		return current
	}
	parts := []ai.CheckContentPart{{Type: "text", Text: current}}
	if strings.TrimSpace(current) == "" {
		parts[0].Text = "图片/贴纸：" + assistantMessageType(msg)
	}
	parts = append(parts, ai.CheckContentPart{Type: "image_url", ImageURL: map[string]string{"url": "data:image/jpeg;base64," + imageB64}})
	return parts
}
