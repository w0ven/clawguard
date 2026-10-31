package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
func (w *ModerationNotifications) send(parent context.Context, n store.ModerationNotification) (string, time.Duration) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	// No user-supplied HTML or provider reason is interpreted as formatting.
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
	text := fmt.Sprintf("群规则处理通知 · CASE-%d\n群聊：%s\n处理：%s（%s）", n.CaseID, n.GroupTitle, action, result)
	if w.queries != nil {
		if item, err := w.queries.AppealCaseForUser(ctx, n.CaseID, n.UserID); err == nil {
			text += "\n记录时间：" + item.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")
		}
	}
	if n.Explanation != "" {
		text += "\n说明：" + n.Explanation
	}
	if n.Summary != "" {
		text += "\n消息摘要：" + n.Summary
	}
	text += "\n如认为此处理有误，可发起申诉，管理员将进行复核。"
	if n.EventKind == "appeal_result" {
		text = n.EventText
	}
	payload := map[string]any{"chat_id": n.UserID, "text": text, "disable_web_page_preview": true}
	if w.publicBaseURL != "" && n.EventKind != "appeal_result" {
		payload["reply_markup"] = map[string]any{"inline_keyboard": [][]any{{map[string]any{"text": "申诉", "web_app": map[string]string{"url": fmt.Sprintf("%s/appeal/mini?case=%d", w.publicBaseURL, n.CaseID)}}}}}
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
