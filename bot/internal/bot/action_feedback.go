package bot

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
)

// sendActionFeedback 把一条动作反馈发到群里（根据配置）
// feedback: 对应动作的配置项
// chat: 目标群
// replyTo: 若 ReplyToMessage=true 时 reply 的消息（可为 nil）
// vars: 模板变量，key 不带花括号
func (s *Service) sendActionFeedback(
	chat *tele.Chat,
	replyTo *tele.Message,
	feedback config.ActionFeedback,
	vars map[string]string,
) {
	if chat == nil || !feedback.Enabled {
		return
	}
	tpl := strings.TrimSpace(feedback.Template)
	if tpl == "" {
		return
	}

	rendered := renderFeedbackTemplate(tpl, vars, feedback.ParseMode)
	if rendered == "" {
		return
	}

	parseMode := resolveParseMode(feedback.ParseMode)

	opts := &tele.SendOptions{
		ParseMode:             parseMode,
		DisableWebPagePreview: true,
	}
	if feedback.ReplyToMessage && replyTo != nil {
		opts.ReplyTo = replyTo
	}

	sent, err := s.sendThrottled(context.Background(), chat, rendered, opts)
	if err != nil {
		s.logger.Warn("send action feedback failed",
			zap.Error(err),
			zap.Int64("chat_id", chat.ID))
		return
	}

	if feedback.AutoDeleteSeconds > 0 && sent != nil {
		secs := feedback.AutoDeleteSeconds
		s.runDelayed(time.Duration(secs)*time.Second, func() {
			if delErr := s.deleteDelayedMessage(sent); delErr != nil {
				s.logger.Debug("auto delete feedback failed",
					zap.Error(delErr),
					zap.Int("message_id", sent.ID))
			}
		})
	}
}

// renderFeedbackTemplate 渲染模板，支持 {user} {admin} {reason} {duration} 等变量
// 未知变量保留原样（让用户看到自己写错了）
func renderFeedbackTemplate(tpl string, vars map[string]string, parseMode string) string {
	structuralVars := make(map[string]string, 2)
	plainVars := make(map[string]string, len(vars))
	for k, v := range vars {
		placeholder := "{" + k + "}"
		switch k {
		case "user", "admin", "user_mention", "admin_mention":
			structuralVars[placeholder] = v
		default:
			plainVars[placeholder] = v
		}
	}
	rendered := RenderMessageTemplate(tpl, parseMode, structuralVars, plainVars)
	return rendered.Text
}

// feedbackUserLabel 返回动作反馈里用于 {user} 的纯数字 ID。
func feedbackUserLabel(user *tele.User, parseMode string) string {
	if user == nil {
		return "未知用户"
	}
	return itoa64(user.ID)
}

// feedbackUserMention 返回动作反馈里用于 {user_mention} 的可点击 mention，
// 规则与欢迎语 {user_mention} 一致：有 username 用 @username，否则用 mention link。
func feedbackUserMention(user *tele.User, parseMode string) string {
	if user == nil {
		return "该用户"
	}
	switch resolveParseMode(parseMode) {
	case tele.ModeMarkdownV2:
		return mentionMarkdownV2(user)
	case tele.ModeMarkdown:
		return mentionMarkdownLegacy(user)
	default:
		return mentionHTML(user)
	}
}

// feedbackAdminLabel 返回动作反馈里用于 {admin} 的纯数字 ID。
func feedbackAdminLabel(user *tele.User, parseMode string) string {
	return feedbackUserLabel(user, parseMode)
}

// feedbackAdminMention 同上，用于 {admin_mention}
func feedbackAdminMention(user *tele.User, parseMode string) string {
	return feedbackUserMention(user, parseMode)
}

// formatDuration 把秒数格式化成人类可读
func formatDuration(seconds int) string {
	if seconds <= 0 {
		return "永久"
	}
	if seconds < 60 {
		return itoa64(int64(seconds)) + "秒"
	}
	if seconds < 3600 {
		return itoa64(int64(seconds/60)) + "分钟"
	}
	if seconds < 86400 {
		return itoa64(int64(seconds/3600)) + "小时"
	}
	return itoa64(int64(seconds/86400)) + "天"
}

// itoa64 简单的 int64 → string（避免在热路径引 strconv）
func itoa64(n int64) string {
	// 用 fmt.Sprint 也行，这里保持简单
	return strFormatInt(n)
}

// 留一个小 helper 避免重复 import strconv
func strFormatInt(n int64) string {
	// 直接用 fmt.Sprint 语义
	b := [20]byte{}
	pos := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	if n == 0 {
		pos--
		b[pos] = '0'
	}
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// WithCtx 为实现 sendActionFeedback 的调用方统一一个 ctx 签名（预留）
var _ = context.Background
