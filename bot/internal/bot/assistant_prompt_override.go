package bot

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/openclaw/clawguard/internal/store"
)

func assistantEmbeddedPrompt(key string) string {
	switch key {
	case "persona":
		return assistantPersonaPrompt
	case "casual":
		return assistantCasualPrompt
	case "decision":
		return assistantDecisionPrompt
	case "proactive_topic":
		return assistantProactiveTopicPrompt
	case "style_distill":
		return assistantStyleDistillPrompt
	default:
		return ""
	}
}

func (a *GroupAssistant) loadPromptOverrides(ctx context.Context, chatID int64) map[string]string {
	out := map[string]string{}
	if a == nil || a.queries == nil {
		return out
	}
	load := func(id int64) {
		items, err := a.queries.ListAssistantPromptOverrides(ctx, id)
		if err != nil {
			return
		}
		for _, item := range items {
			if strings.TrimSpace(item.Content) == "" {
				continue
			}
			if _, exists := out[item.PromptKey]; !exists {
				out[item.PromptKey] = item.Content
			}
		}
	}
	if chatID != 0 {
		load(chatID)
	}
	load(0)
	return out
}

func assistantResolvedPrompt(overrides map[string]string, key string) string {
	if overrides != nil {
		if text := strings.TrimSpace(overrides[key]); text != "" {
			return text
		}
	}
	return assistantEmbeddedPrompt(key)
}

func assistantSystemPromptForModeWithOverrides(policy store.GroupAssistantPolicy, mode string, overrides map[string]string) string {
	base := assistantResolvedPrompt(overrides, "persona") + "\n" + assistantResolvedPrompt(overrides, "casual")
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
		base += "\n" + assistantResolvedPrompt(overrides, "proactive_topic")
		base += "\n[INTERACTION_MODE]\ncold：自然开启一个轻量话题，默认1到2句；没有合适话题时严格输出 SKIP_TASK。"
	}
	return base + "\n" + assistantSafetyPrompt
}

func assistantPromptOverrideMissing(err error) bool {
	return err != nil && (err == pgx.ErrNoRows || strings.Contains(err.Error(), "no rows"))
}
