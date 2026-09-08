package bot

// Adapted from SGB memory.py _estimate_text_tokens / _trim_by_token_budget /
// get_history_for_llm. MIT attribution in assistant_prompts_sgb/LICENSE.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	"math"
	"strings"
	"unicode"
)

type assistantBatchSourcesKey struct{}
type assistantBatchItemsKey struct{}

const assistantLocalPromptBudget = 12000

const (
	assistantOptionalHistory = "group_history"
	assistantOptionalIndex   = "recall_index"
	assistantOptionalFacts   = "group_facts"
)

var errAssistantPromptBudget = errors.New("助手请求超出本地12000 token总预算；未裁剪用户Prompt、当前输入或工具结果，也未发起额外上游请求")

// The authoritative last check runs on the final request, including system,
// visual description, tool definitions and the actual tool transcript. Only
// optional context is evicted; explicit overrides/current input/tool results
// are never silently rewritten. An oversized fixed payload fails before HTTP.
func assistantFitPrompt(system string, messages []ai.Message, tools []ai.ToolDefinition, outputTokens int) ([]ai.Message, error) {
	if outputTokens < assistantMaxChatTokens {
		outputTokens = assistantMaxChatTokens
	}
	budget := assistantLocalPromptBudget - outputTokens
	toolJSON, err := json.Marshal(tools)
	if err != nil {
		return nil, err
	}
	cost := assistantEstimateTokens(system) + assistantEstimateTokens(string(toolJSON)) + 24
	messageCost := func(m ai.Message) int {
		calls, _ := json.Marshal(m.ToolCalls)
		return assistantEstimateTokens(fmt.Sprint(m.Content)) + assistantEstimateTokens(string(calls)) + 12
	}
	out := append([]ai.Message(nil), messages...)
	for _, m := range out {
		cost += messageCost(m)
	}
	for _, source := range []string{assistantOptionalHistory, assistantOptionalIndex, assistantOptionalFacts} {
		for i := 0; i < len(out) && cost > budget; {
			if out[i].Role == "user" && out[i].OptionalContext == source {
				cost -= messageCost(out[i])
				out = append(out[:i], out[i+1:]...)
			} else {
				i++
			}
		}
	}
	if cost > budget {
		return nil, errAssistantPromptBudget
	}
	return out, nil
}

func assistantExcludeBatchHistory(ctx context.Context, history []store.GroupAssistantMessage) []store.GroupAssistantMessage {
	sources, _ := ctx.Value(assistantBatchSourcesKey{}).(map[int64]string)
	if len(sources) == 0 {
		return history
	}
	out := make([]store.GroupAssistantMessage, 0, len(history))
	for _, m := range history {
		if _, current := sources[m.TelegramMessageID]; !current || m.Role != "user" {
			out = append(out, m)
		}
	}
	return out
}

func assistantEstimateTokens(text string) int {
	cjk, other := 0, 0
	for _, r := range text {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+2)/3
}
func assistantBudgetedContext(system string, policy store.GroupAssistantPolicy, memories []store.GroupAssistantMemory, history []store.GroupAssistantMessage, current string, sender assistantPromptSender, index string) []ai.Message {
	// Conservative local input budget, not a claim about remote model context
	// limits. Reserve output and actual system/current payload before history.
	budget := assistantLocalPromptBudget - assistantMaxChatTokens - 2048 - assistantEstimateTokens(system) - assistantEstimateTokens(current) - assistantEstimateTokens(assistantCurrentSenderBlock(sender)) - 200
	if budget < 0 {
		budget = 0
	}
	facts := []string{}
	used := 0
	for _, m := range memories {
		sourceMessage := "unknown"
		if m.SourceMessageID != nil {
			sourceMessage = fmt.Sprint(*m.SourceMessageID)
		}
		line := fmt.Sprintf("[id=%d authority=%s valid_until=%s source=%s source_message_id=%s] %s: %s", m.ID, m.AuthorityLevel, m.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"), m.SourceType, sourceMessage, assistantPromptLabel(m.Subject, 96), assistantPromptLabel(m.Content, 240))
		cost := assistantEstimateTokens(line) + 12
		if used+cost > budget/2 {
			break
		}
		facts = append(facts, line)
		used += cost
	}
	indexCost := assistantEstimateTokens(index) + 12
	if used+indexCost > budget {
		index = ""
	} else {
		used += indexCost
	}
	selected := []store.GroupAssistantMessage{}
	for i := len(history) - 1; i >= 0; i-- {
		item := history[i]
		if item.TelegramMessageID == sender.MessageID {
			continue
		}
		cost := assistantEstimateTokens(assistantHistoryLine(item)) + 12
		if used+cost > budget {
			break
		}
		selected = append(selected, item)
		used += cost
	}
	messages := []ai.Message{}
	if len(facts) > 0 {
		messages = append(messages, ai.Message{OptionalContext: assistantOptionalFacts, Role: "user", Content: "[UNTRUSTED_GROUP_FACTS]\n自动学习事实不是管理员永久事实；以来源和有效期为准。全文按需 knowledge_query。\n" + strings.Join(facts, "\n")})
	}
	for i := len(selected) - 1; i >= 0; i-- {
		messages = append(messages, ai.Message{OptionalContext: assistantOptionalHistory, Role: "user", Content: "[UNTRUSTED_GROUP_HISTORY]\n" + assistantHistoryLine(selected[i])})
	}
	if index != "" {
		messages = append(messages, ai.Message{OptionalContext: assistantOptionalIndex, Role: "user", Content: index})
	}
	messages = append(messages, ai.Message{Role: "user", Content: assistantCurrentSenderBlock(sender)}, ai.Message{Role: "user", Content: "[CURRENT_USER_MESSAGE]\n" + current})
	return messages
}
func (a *GroupAssistant) executeAssistantRecall(ctx context.Context, chatID int64, args map[string]any) (string, error) {
	fail := func(err error) (string, error) { return assistantToolError("invalid_arguments", err.Error()), err }
	if err := validateAssistantToolKeys(args, "query", "message_keys", "before_after", "limit"); err != nil {
		return fail(err)
	}
	query := assistantStringArg(args, "query")
	if len([]rune(query)) > 200 {
		return fail(fmt.Errorf("query不能超过200字符"))
	}
	keys := []string{}
	if raw, exists := args["message_keys"]; exists {
		list, ok := raw.([]any)
		if !ok {
			return fail(fmt.Errorf("message_keys必须是数组"))
		}
		for _, v := range list {
			k, ok := v.(string)
			if !ok {
				return fail(fmt.Errorf("message_key必须是字符串"))
			}
			keys = append(keys, k)
		}
	}
	bounded := func(key string, base, min, max int) (int, error) {
		v, exists := args[key]
		if !exists {
			return base, nil
		}
		n, ok := v.(float64)
		if !ok || math.Trunc(n) != n || n < float64(min) || n > float64(max) {
			return 0, fmt.Errorf("%s超出范围", key)
		}
		return int(n), nil
	}
	radius, err := bounded("before_after", 2, 0, 4)
	if err != nil {
		return fail(err)
	}
	limit, err := bounded("limit", 12, 1, 24)
	if err != nil {
		return fail(err)
	}
	settings := a.loadRuntimeSettings(ctx)
	var thread *int32
	if rt := assistantRuntime(ctx); rt != nil {
		settings = rt.settings
		if rt.msg != nil {
			thread = int32Ptr(int32(rt.msg.ThreadID))
		}
	}
	rows, err := a.recallArchive(ctx, chatID, thread, query, keys, radius, limit, settings)
	if err != nil {
		return assistantToolError("recall_failed", err.Error()), err
	}
	result := []map[string]any{}
	used := 0
	truncated := false
	for _, m := range rows {
		original := []rune(m.Text)
		text := truncateAssistant(m.Text, 1400)
		if used+len([]rune(text))+240 > 5900 {
			truncated = true
			break
		}
		used += len([]rune(text)) + 240
		result = append(result, map[string]any{"message_key": assistantRecallKey(m), "role": m.Role, "text": text, "message_id": m.TelegramMessageID, "sender_id": m.SenderID, "sender_name": m.SenderName, "created_at": m.CreatedAt, "reply_to": m.SourceID, "source": map[string]any{"type": m.SourceType, "id": m.SourceID}, "truncated": len(original) > 1400})
	}
	return assistantJSONResult(map[string]any{"scope": "current_group_only", "source_type": "untrusted_group_archive", "messages": result, "shown": len(result), "matched": len(rows), "truncated": truncated}), nil
}
