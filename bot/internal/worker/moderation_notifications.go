package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

const moderationNotificationBatch = 16
const moderationNotificationConcurrency = 4

type ModerationNotifications struct {
	logger        *zap.Logger
	queries       *store.Queries
	token         string
	publicBaseURL string
	client        *http.Client
}

func NewModerationNotifications(logger *zap.Logger, q *store.Queries, token string, publicBaseURL ...string) *ModerationNotifications {
	base := ""
	if len(publicBaseURL) > 0 {
		base = strings.TrimRight(publicBaseURL[0], "/")
	}
	return &ModerationNotifications{logger: logger, queries: q, token: token, publicBaseURL: base, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}
}
func (w *ModerationNotifications) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		w.process(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (w *ModerationNotifications) process(ctx context.Context) {
	if err := w.queries.RecoverModerationNotifications(ctx); err != nil {
		w.logger.Warn("recover notification leases failed")
		return
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return
	}
	token := hex.EncodeToString(random)
	jobs, err := w.queries.ClaimModerationNotifications(ctx, token, moderationNotificationBatch)
	if err != nil {
		w.logger.Warn("claim notifications failed")
		return
	}
	ch := make(chan store.ModerationNotification)
	var wg sync.WaitGroup
	for i := 0; i < moderationNotificationConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range ch {
				w.deliver(ctx, job)
			}
		}()
	}
	for _, job := range jobs {
		select {
		case ch <- job:
		case <-ctx.Done():
			close(ch)
			wg.Wait()
			return
		}
	}
	close(ch)
	wg.Wait()
}
func (w *ModerationNotifications) deliver(ctx context.Context, n store.ModerationNotification) {
	ok, err := w.queries.BeginModerationNotification(ctx, n.ID, n.LeaseToken)
	if err != nil || !ok {
		return
	}
	state, delay := w.send(ctx, n)
	if state == "uncertain" {
		// Store settlement currently accepts only known outcomes. Preserve the
		// sending lease: recovery converts it to uncertain after expiry without
		// replaying it. Other users remain eligible in the meantime.
		w.logger.Warn("notification delivery result uncertain; awaiting lease recovery", zap.Int64("notification_id", n.ID))
		return
	}
	if state == "pending" { // Exponential backoff and provider retry_after, whichever is larger.
		backoff := time.Duration(1<<min(int(n.Attempts), 5)) * 15 * time.Second
		if delay < backoff {
			delay = backoff
		}
	}
	settled, err := w.queries.SettleModerationNotification(ctx, n.ID, n.LeaseToken, state, time.Now().Add(delay))
	if err != nil || !settled {
		w.logger.Warn("notification result not recorded; delivery may be uncertain", zap.Int64("notification_id", n.ID))
	}
}
func isSafeTelegramJoinURL(raw string) bool {
	s := strings.TrimSpace(raw)
	return (strings.HasPrefix(s, "https://t.me/") || strings.HasPrefix(s, "https://telegram.me/")) && !strings.ContainsAny(s, " \t\r\n")
}

func (w *ModerationNotifications) resolveGroupJoinURL(parent context.Context, chatID, appealID int64) string {
	if chatID == 0 || strings.TrimSpace(w.token) == "" || w.client == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()

	callTelegram := func(method string, reqBody map[string]any, out any) bool {
		rawBody, err := json.Marshal(reqBody)
		if err != nil {
			return false
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+w.token+"/"+method, bytes.NewReader(rawBody))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := w.client.Do(req)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return false
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 8192))
		if err != nil {
			return false
		}
		return json.Unmarshal(data, out) == nil
	}

	var chatResp struct {
		OK     bool `json:"ok"`
		Result struct {
			Username   string `json:"username"`
			InviteLink string `json:"invite_link"`
		} `json:"result"`
	}
	if callTelegram("getChat", map[string]any{"chat_id": chatID}, &chatResp) && chatResp.OK {
		if u := strings.TrimPrefix(strings.TrimSpace(chatResp.Result.Username), "@"); u != "" && !strings.ContainsAny(u, "/?&# \t\r\n") {
			return "https://t.me/" + u
		}
	}

	name := "AppealRejoin"
	if appealID > 0 {
		name = fmt.Sprintf("APL-%d", appealID)
	}
	var inviteResp struct {
		OK     bool `json:"ok"`
		Result struct {
			InviteLink string `json:"invite_link"`
		} `json:"result"`
	}
	if callTelegram("createChatInviteLink", map[string]any{
		"chat_id":      chatID,
		"name":         name,
		"member_limit": 1,
		"expire_date":  time.Now().Add(7 * 24 * time.Hour).Unix(),
	}, &inviteResp) && inviteResp.OK && isSafeTelegramJoinURL(inviteResp.Result.InviteLink) {
		return strings.TrimSpace(inviteResp.Result.InviteLink)
	}

	if isSafeTelegramJoinURL(chatResp.Result.InviteLink) {
		return strings.TrimSpace(chatResp.Result.InviteLink)
	}
	return ""
}

func isProfileRelatedNotification(n store.ModerationNotification) bool {
	if n.SourceKind == "join_check" || n.SourceKind == "profile_event" {
		return strings.Contains(n.Explanation, "资料") || strings.Contains(n.Explanation, "简介") || strings.HasPrefix(strings.TrimSpace(n.Summary), "个人简介：")
	}
	return strings.HasPrefix(strings.TrimSpace(n.Summary), "个人简介：") || strings.Contains(n.Explanation, "个人资料") || strings.Contains(n.Explanation, "资料检查")
}

func formatEventTextHTML(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	var out []string
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			out = append(out, "")
			continue
		}
		if i == 0 {
			out = append(out, "<b>"+html.EscapeString(line)+"</b>")
			continue
		}
		if k, v, ok := strings.Cut(line, "："); ok {
			out = append(out, fmt.Sprintf("<b>%s：</b>%s", html.EscapeString(k), html.EscapeString(v)))
			continue
		}
		out = append(out, html.EscapeString(line))
	}
	return strings.Join(out, "\n")
}

func (w *ModerationNotifications) send(parent context.Context, n store.ModerationNotification) (string, time.Duration) {
	var appealJoinURL string
	if n.EventKind == "appeal_result" && n.AppealDecision == "approve" && n.AppealStatus == "completed" {
		appealJoinURL = w.resolveGroupJoinURL(parent, n.ChatID, n.AppealID)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	// All user/provider strings are HTML-escaped before formatting.
	result := map[string]string{"executed": "已执行", "partial": "部分执行", "failed": "未确认", "reversed": "已撤销"}[n.Result]
	action := map[string]string{"delete": "删除消息", "delete_warn": "删除消息并警告", "delete_and_warn": "删除消息并警告", "warn": "警告", "mute": "禁言", "mute_5m": "禁言", "mute_1h": "禁言", "delete_mute": "删除消息并禁言", "delete_ban": "删除消息并封禁", "ban": "封禁", "kick": "移出群聊", "unban": "解除封禁", "unmute": "解除禁言"}[n.Action]
	if action == "" {
		action = "群规则处理"
	}
	if n.Result == "partial" || n.Result == "failed" {
		action = "原拟处理：" + action
	}
	if n.Action == "unban" && n.Result == "partial" {
		action, result = "解封请求", "状态待确认"
	}
	isProfileCase := isProfileRelatedNotification(n)

	var b strings.Builder
	fmt.Fprintf(&b, "⚠️ <b>群规则处理通知 · CASE-%d</b>\n", n.CaseID)
	fmt.Fprintf(&b, "<b>群聊：</b>%s\n", html.EscapeString(n.GroupTitle))
	fmt.Fprintf(&b, "<b>处理：</b>%s（%s）", html.EscapeString(action), html.EscapeString(result))
	if w.queries != nil {
		if item, err := w.queries.AppealCaseForUser(ctx, n.CaseID, n.UserID); err == nil {
			fmt.Fprintf(&b, "\n<b>记录时间：</b><code>%s</code>", html.EscapeString(item.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")))
		}
	}
	if n.Explanation != "" {
		fmt.Fprintf(&b, "\n<b>处理原因：</b>%s", html.EscapeString(n.Explanation))
	}
	if n.Summary != "" {
		summaryLabel := "触发内容"
		if isProfileCase {
			summaryLabel = "触发简介/内容"
		}
		fmt.Fprintf(&b, "\n<b>%s：</b>\n<blockquote>%s</blockquote>", summaryLabel, html.EscapeString(n.Summary))
	}
	if isProfileCase {
		b.WriteString("\n💡 <i>提示：入群及发言前会检查个人简介；如简介含频道、外链或引流联系方式，请先修改后再申请解除或重新加群。</i>")
	}
	b.WriteString("\n\n如认为此处理有误，可点击下方按钮发起申诉，管理员将进行复核。")
	text := b.String()

	if n.EventKind == "appeal_result" {
		text = formatEventTextHTML(n.EventText)
		if n.AppealDecision == "approve" && isProfileCase {
			if strings.TrimSpace(n.Summary) != "" {
				text += "\n<b>原触发简介：</b>\n<blockquote>" + html.EscapeString(n.Summary) + "</blockquote>"
			}
			text += "\n\n⚠️ <b>重要提醒：</b>本次因<b>个人简介/资料</b>触发规则。系统已为您清除资料缓存，<b>重新入群前请先修改或清空个人简介中的频道、链接或联系方式</b>，否则入群自动检查仍会再次拦截封禁！"
		}
		if appealJoinURL != "" {
			actLower := strings.ToLower(n.Action)
			if strings.Contains(actLower, "ban") || strings.Contains(actLower, "kick") {
				text += "\n\n您的群组限制已解除，确认个人资料合规后点击下方按钮即可重新加入群聊。"
			} else {
				text += "\n\n您的群组限制已解除，点击下方按钮可进入群聊。"
			}
		}
	}
	payload := map[string]any{"chat_id": n.UserID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if w.publicBaseURL != "" && n.EventKind != "appeal_result" {
		payload["reply_markup"] = map[string]any{"inline_keyboard": [][]any{{map[string]any{"text": "申诉", "web_app": map[string]string{"url": fmt.Sprintf("%s/appeal/mini?case=%d", w.publicBaseURL, n.CaseID)}}}}}
	} else if n.EventKind == "appeal_result" && appealJoinURL != "" {
		btnText := "💬 进入群聊"
		actLower := strings.ToLower(n.Action)
		if strings.Contains(actLower, "ban") || strings.Contains(actLower, "kick") {
			btnText = "🔗 重新加入群聊"
		}
		payload["reply_markup"] = map[string]any{"inline_keyboard": [][]any{{map[string]any{"text": btnText, "url": appealJoinURL}}}}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+w.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return "failed", 0
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := w.client.Do(req)
	if err != nil {
		// The request may have reached Telegram even when the response was lost
		// or shutdown cancelled the call. Never replay an unknown outcome.
		return "uncertain", 0
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusForbidden {
		return "unreachable", 0
	}
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, 8192))
	var response struct {
		OK         bool `json:"ok"`
		ErrorCode  int  `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	parseErr := json.Unmarshal(raw, &response)
	if res.StatusCode == http.StatusOK && readErr == nil && parseErr == nil && response.OK {
		return "sent", 0
	}
	if res.StatusCode == http.StatusForbidden || response.ErrorCode == http.StatusForbidden {
		return "unreachable", 0
	}
	if res.StatusCode == http.StatusTooManyRequests || response.ErrorCode == http.StatusTooManyRequests {
		seconds := response.Parameters.RetryAfter
		if h := strings.TrimSpace(res.Header.Get("Retry-After")); h != "" {
			if v, e := strconv.Atoi(h); e == nil && v > seconds {
				seconds = v
			} else if at, e := http.ParseTime(h); e == nil {
				v := int(time.Until(at).Seconds()) + 1
				if v > seconds {
					seconds = v
				}
			}
		}
		if seconds < 0 {
			seconds = 0
		}
		// A provider wait beyond our 24h expiry cannot be retried in time.
		if seconds > 259200 {
			seconds = 259200
		}
		return "pending", time.Duration(seconds) * time.Second
	}
	if res.StatusCode >= 500 || response.ErrorCode >= 500 || readErr != nil || parseErr != nil {
		return "uncertain", 0
	}
	return "failed", 0
}
