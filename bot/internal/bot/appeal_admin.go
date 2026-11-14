package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/store"
)

func (s *Service) SetAppealDecisionHandler(fn func(ctx context.Context, adminTelegramID, appealID int64, action string) (*store.AppealReview, string, error)) {
	if s == nil {
		return
	}
	s.appealDecisionHandler = fn
}

func formatAppealUserSummaryHTML(v store.AppealReview) (string, string) {
	fullName := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(v.FirstName), strings.TrimSpace(v.LastName)}, " "))
	uname := strings.TrimPrefix(strings.TrimSpace(v.Username), "@")
	cleanUname := ""
	if uname != "" && !strings.ContainsAny(uname, "/?&# \t\r\n<>\"'") {
		cleanUname = uname
	}
	var parts []string
	if fullName != "" {
		parts = append(parts, fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", v.UserID, htmlEscape(fullName)))
	} else {
		parts = append(parts, fmt.Sprintf("<a href=\"tg://user?id=%d\">点击打开主页</a>", v.UserID))
	}
	if cleanUname != "" {
		parts = append(parts, fmt.Sprintf("<a href=\"https://t.me/%s\">@%s</a>", htmlEscape(cleanUname), htmlEscape(cleanUname)))
	}
	parts = append(parts, fmt.Sprintf("ID: <code>%d</code>", v.UserID))
	return strings.Join(parts, " · "), cleanUname
}

func formatAppealAdminCardHTML(v store.AppealReview, publicBaseURL string) string {
	groupLabel := strings.TrimSpace(v.GroupTitle)
	if groupLabel == "" {
		groupLabel = fmt.Sprintf("群组 %d", v.ChatID)
	}
	userHTML, _ := formatAppealUserSummaryHTML(v)
	isProfileCase := v.SourceKind == "join_check" || v.SourceKind == "profile_event" || strings.HasPrefix(strings.TrimSpace(v.Summary), "个人简介：")

	var b strings.Builder
	fmt.Fprintf(&b, "📨 <b>新申诉待处理 · APL-%d</b>\n", v.ID)
	fmt.Fprintf(&b, "👥 <b>群组：</b>%s (<code>%d</code>)\n", htmlEscape(groupLabel), v.ChatID)
	fmt.Fprintf(&b, "👤 <b>申诉人：</b>%s\n", userHTML)
	fmt.Fprintf(&b, "🗂 <b>关联案件：</b><code>CASE-%d</code>（%s / %s）\n", v.CaseID, htmlEscape(v.Action), htmlEscape(v.CaseResult))
	if strings.TrimSpace(v.Explanation) != "" {
		fmt.Fprintf(&b, "⚖️ <b>被封/处理原因：</b>%s\n", htmlEscape(v.Explanation))
	}
	if strings.TrimSpace(v.Summary) != "" {
		label := "触发原文"
		if isProfileCase {
			label = "触发简介/原文"
		}
		fmt.Fprintf(&b, "📄 <b>%s：</b>\n<blockquote>%s</blockquote>\n", label, htmlEscape(truncateString(v.Summary, 360)))
	}
	fmt.Fprintf(&b, "\n📝 <b>用户申诉理由：</b>\n<blockquote>%s</blockquote>\n", htmlEscape(truncateString(v.Reason, 600)))
	if strings.TrimSpace(v.ReferenceURL) != "" {
		fmt.Fprintf(&b, "🔗 <b>补充链接：</b>%s\n", htmlEscape(v.ReferenceURL))
	}

	if v.Status == "pending" {
		b.WriteString("\n⏳ <b>状态：</b>待审核（可直接点击上方用户名查看资料，或点击下方按钮处理）")
		return b.String()
	}

	decisionLabel := "维持原处理（已驳回）"
	if v.Decision != nil && *v.Decision == "approve" {
		decisionLabel = "同意申诉"
	}
	fmt.Fprintf(&b, "\n✅ <b>审核决定：</b>%s\n", htmlEscape(decisionLabel))
	fmt.Fprintf(&b, "📌 <b>执行结果：</b>%s（<code>%s</code>）\n", htmlEscape(v.ExecutionResult), htmlEscape(v.Status))
	if strings.TrimSpace(v.PublicNote) != "" {
		fmt.Fprintf(&b, "💬 <b>处理说明：</b>%s\n", htmlEscape(v.PublicNote))
	}
	if v.ReviewerID != nil && *v.ReviewerID > 0 {
		fmt.Fprintf(&b, "👤 <b>审核管理员 ID：</b><code>%d</code>", *v.ReviewerID)
	}
	return b.String()
}

func (s *Service) appealAdminMarkup(v store.AppealReview) *tele.ReplyMarkup {
	unique := s.appealAdminBtn.Unique
	if unique == "" {
		unique = "appeal_admin_act"
	}
	markup := &tele.ReplyMarkup{}
	var rows []tele.Row
	if v.Status == "pending" {
		approveBtn := markup.Data("✅ 同意并解除处罚", unique, fmt.Sprintf("%d|approve", v.ID))
		upholdBtn := markup.Data("❌ 驳回申诉", unique, fmt.Sprintf("%d|uphold", v.ID))
		rows = append(rows, markup.Row(approveBtn, upholdBtn))
	} else if v.Decision != nil && *v.Decision == "approve" && (v.Status == "failed" || v.Status == "executing") {
		retryBtn := markup.Data("🔄 重试执行解除", unique, fmt.Sprintf("%d|retry", v.ID))
		rows = append(rows, markup.Row(retryBtn))
	}
	var actionBtns []tele.Btn
	if _, cleanUname := formatAppealUserSummaryHTML(v); cleanUname != "" {
		actionBtns = append(actionBtns, markup.URL("👤 查看 @"+cleanUname, "https://t.me/"+cleanUname))
	}
	if base := strings.TrimRight(strings.TrimSpace(s.cfg.AdminWebBaseURL()), "/"); base != "" && strings.HasPrefix(base, "https://") {
		actionBtns = append(actionBtns, markup.URL("🌐 后台查看详情", fmt.Sprintf("%s/appeals", base)))
	}
	if len(actionBtns) > 0 {
		rows = append(rows, markup.Row(actionBtns...))
	}
	if len(rows) == 0 {
		return &tele.ReplyMarkup{}
	}
	markup.Inline(rows...)
	return markup
}

// NotifyAdminsNewAppeal sends an interactive private message to eligible group
// admins and owners when a new appeal is submitted. Unreachable admins are
// logged and skipped without blocking appeal submission.
func (s *Service) NotifyAdminsNewAppeal(ctx context.Context, appealID int64) {
	if s == nil || s.queries == nil || s.bot == nil || appealID <= 0 {
		return
	}
	v, err := s.queries.GetAppealReview(ctx, appealID, true, []int64{})
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("load appeal for admin notification failed", zap.Error(err), zap.Int64("appeal_id", appealID))
		}
		return
	}

	recipients := map[int64]struct{}{}
	admins, err := s.queries.ListAdmins(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("list admins for appeal notification failed", zap.Error(err), zap.Int64("appeal_id", appealID))
		}
	} else {
		for _, admin := range admins {
			if admin.TelegramID != 0 && botAdminCanAccessChat(admin, v.ChatID) {
				recipients[admin.TelegramID] = struct{}{}
			}
		}
	}
	for _, telegramID := range s.cfg.AdminTelegramIDs {
		if telegramID != 0 {
			recipients[telegramID] = struct{}{}
		}
	}
	if len(recipients) == 0 {
		return
	}

	text := formatAppealAdminCardHTML(v, s.cfg.AdminWebBaseURL())
	markup := s.appealAdminMarkup(v)
	opts := &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	}
	for telegramID := range recipients {
		if _, sendErr := s.sendThrottled(ctx, &tele.User{ID: telegramID}, text, opts); sendErr != nil && s.logger != nil {
			s.logger.Debug("send appeal notification to admin failed", zap.Error(sendErr), zap.Int64("telegram_id", telegramID), zap.Int64("appeal_id", appealID))
		}
	}
}

func (s *Service) handleAppealAdminCallback(c tele.Context) error {
	callback := c.Callback()
	sender := c.Sender()
	if callback == nil || sender == nil {
		return nil
	}
	if s.appealDecisionHandler == nil {
		return c.Respond(&tele.CallbackResponse{Text: "申诉处理服务暂未就绪，请稍后重试", ShowAlert: true})
	}

	parts := strings.Split(strings.TrimSpace(c.Data()), "|")
	if len(parts) != 2 {
		return c.Respond(&tele.CallbackResponse{Text: "操作参数无效", ShowAlert: true})
	}
	appealID, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || appealID <= 0 {
		return c.Respond(&tele.CallbackResponse{Text: "申诉编号无效", ShowAlert: true})
	}
	action := strings.ToLower(strings.TrimSpace(parts[1]))
	if action != "approve" && action != "uphold" && action != "retry" {
		return c.Respond(&tele.CallbackResponse{Text: "不支持的操作类型", ShowAlert: true})
	}

	ctx := context.Background()
	updated, alertText, err := s.appealDecisionHandler(ctx, sender.ID, appealID, action)
	if err != nil {
		if updated != nil && callback.Message != nil {
			s.editAppealAdminMessage(callback.Message, *updated)
		}
		if alertText == "" {
			alertText = "处理失败，请稍后重试或前往后台查看"
		}
		return c.Respond(&tele.CallbackResponse{Text: alertText, ShowAlert: true})
	}

	if updated != nil && callback.Message != nil {
		s.editAppealAdminMessage(callback.Message, *updated)
	}
	if alertText == "" {
		alertText = "已处理"
	}
	return c.Respond(&tele.CallbackResponse{Text: alertText, ShowAlert: true})
}

func (s *Service) editAppealAdminMessage(msg *tele.Message, v store.AppealReview) {
	if s == nil || s.bot == nil || msg == nil {
		return
	}
	text := formatAppealAdminCardHTML(v, s.cfg.AdminWebBaseURL())
	opts := &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           s.appealAdminMarkup(v),
	}
	if _, err := s.bot.Edit(msg, text, opts); err != nil && s.logger != nil {
		s.logger.Debug("edit appeal admin card failed", zap.Error(err), zap.Int64("appeal_id", v.ID), zap.Int("message_id", msg.ID))
	}
}
