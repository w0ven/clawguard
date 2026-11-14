package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

type appealNotificationTransport func(*http.Request) (*http.Response, error)

func (f appealNotificationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAppealNotificationButton(t *testing.T) {
	var payload struct {
		Text        string `json:"text"`
		ReplyMarkup struct {
			InlineKeyboard [][]struct {
				Text   string `json:"text"`
				WebApp struct {
					URL string `json:"url"`
				} `json:"web_app"`
			} `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	w := NewModerationNotifications(zap.NewNop(), nil, "fake", "https://example.com/")
	w.client = &http.Client{Transport: appealNotificationTransport(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	state, _ := w.send(context.Background(), store.ModerationNotification{CaseID: 42, UserID: 12, Action: "ban", Result: "partial", Explanation: "仅删除消息"})
	if state != "sent" || len(payload.ReplyMarkup.InlineKeyboard) != 1 || len(payload.ReplyMarkup.InlineKeyboard[0]) != 1 {
		t.Fatalf("payload %+v state=%s", payload, state)
	}
	button := payload.ReplyMarkup.InlineKeyboard[0][0]
	if button.Text != "申诉" || button.WebApp.URL != "https://example.com/appeal/mini?case=42" {
		t.Fatalf("button %+v", button)
	}
	if !strings.Contains(payload.Text, "原拟处理") || !strings.Contains(payload.Text, "仅删除消息") {
		t.Fatalf("partial %s", payload.Text)
	}
}

func TestNotificationAmbiguousResultsAreNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport appealNotificationTransport
		want      string
	}{
		{"timeout", func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded }, "uncertain"},
		{"server error", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":500}`))}, nil
		}, "uncertain"},
		{"invalid success response", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`broken`))}, nil
		}, "uncertain"},
		{"rate limited", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":429,"parameters":{"retry_after":2}}`))}, nil
		}, "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := NewModerationNotifications(zap.NewNop(), nil, "fake")
			w.client = &http.Client{Transport: tc.transport}
			state, _ := w.send(context.Background(), store.ModerationNotification{UserID: 1})
			if state != tc.want {
				t.Fatalf("state=%q want %q", state, tc.want)
			}
		})
	}
}

func TestAppealResultJoinButtonForApprovedCompleted(t *testing.T) {
	t.Run("approved completed attaches group join button", func(t *testing.T) {
		var sentPayload struct {
			Text        string `json:"text"`
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					Text string `json:"text"`
					URL  string `json:"url"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		w := NewModerationNotifications(zap.NewNop(), nil, "fake", "https://example.com/")
		w.client = &http.Client{Transport: appealNotificationTransport(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/getChat"):
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"username":"rfchost"}}`))}, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				if err := json.NewDecoder(r.Body).Decode(&sentPayload); err != nil {
					t.Error(err)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
			default:
				t.Fatalf("unexpected method %s", r.URL.Path)
				return nil, nil
			}
		})}
		state, _ := w.send(context.Background(), store.ModerationNotification{
			CaseID:         2201,
			UserID:         910374375,
			ChatID:         -1002699516772,
			Action:         "ban",
			EventKind:      "appeal_result",
			EventText:      "✅ 申诉审核通过 · APL-3\n群聊：RFCHOST官方群组\n关联处理：CASE-2201\n审核结果：审核通过，原封禁已解除；如有已删除的消息，无法恢复\n处理说明：管理员通过 Bot 私信审核通过",
			AppealID:       3,
			AppealDecision: "approve",
			AppealStatus:   "completed",
		})
		if state != "sent" || len(sentPayload.ReplyMarkup.InlineKeyboard) != 1 || len(sentPayload.ReplyMarkup.InlineKeyboard[0]) != 1 {
			t.Fatalf("state=%s payload=%+v", state, sentPayload)
		}
		btn := sentPayload.ReplyMarkup.InlineKeyboard[0][0]
		if btn.Text != "🔗 重新加入群聊" || btn.URL != "https://t.me/rfchost" {
			t.Fatalf("unexpected button %+v", btn)
		}
		if !strings.Contains(sentPayload.Text, "重新加入群聊") {
			t.Fatalf("expected rejoin guidance in text, got %q", sentPayload.Text)
		}
	})

	t.Run("private group creates single-member invite link", func(t *testing.T) {
		var inviteReq map[string]any
		var sentPayload struct {
			Text        string `json:"text"`
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					Text string `json:"text"`
					URL  string `json:"url"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		w := NewModerationNotifications(zap.NewNop(), nil, "fake")
		w.client = &http.Client{Transport: appealNotificationTransport(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/getChat"):
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"title":"Private Group"}}`))}, nil
			case strings.HasSuffix(r.URL.Path, "/createChatInviteLink"):
				_ = json.NewDecoder(r.Body).Decode(&inviteReq)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"invite_link":"https://t.me/+AbCdEf12345"}}`))}, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				_ = json.NewDecoder(r.Body).Decode(&sentPayload)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
			default:
				t.Fatalf("unexpected method %s", r.URL.Path)
				return nil, nil
			}
		})}
		state, _ := w.send(context.Background(), store.ModerationNotification{
			CaseID:         100,
			UserID:         200,
			ChatID:         -100999,
			Action:         "delete_ban",
			EventKind:      "appeal_result",
			EventText:      "✅ 申诉审核通过 · APL-9",
			AppealID:       9,
			AppealDecision: "approve",
			AppealStatus:   "completed",
		})
		if state != "sent" {
			t.Fatalf("state=%s", state)
		}
		if inviteReq["member_limit"] != float64(1) || inviteReq["name"] != "APL-9" {
			t.Fatalf("unexpected invite request %+v", inviteReq)
		}
		btn := sentPayload.ReplyMarkup.InlineKeyboard[0][0]
		if btn.Text != "🔗 重新加入群聊" || btn.URL != "https://t.me/+AbCdEf12345" {
			t.Fatalf("unexpected button %+v", btn)
		}
	})

	t.Run("upheld appeal never attaches join button", func(t *testing.T) {
		var rawPayload map[string]any
		w := NewModerationNotifications(zap.NewNop(), nil, "fake")
		w.client = &http.Client{Transport: appealNotificationTransport(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Fatalf("unexpected lookup call %s for upheld appeal", r.URL.Path)
			}
			_ = json.NewDecoder(r.Body).Decode(&rawPayload)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
		})}
		state, _ := w.send(context.Background(), store.ModerationNotification{
			CaseID:         100,
			UserID:         200,
			ChatID:         -100999,
			Action:         "ban",
			EventKind:      "appeal_result",
			EventText:      "申诉审核结果 · APL-10",
			AppealID:       10,
			AppealDecision: "uphold",
			AppealStatus:   "upheld",
		})
		if state != "sent" || rawPayload["reply_markup"] != nil {
			t.Fatalf("state=%s payload=%+v", state, rawPayload)
		}
	})

	t.Run("approved profile check appeal includes bio reminder and HTML formatting", func(t *testing.T) {
		var sentPayload struct {
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		w := NewModerationNotifications(zap.NewNop(), nil, "fake")
		w.client = &http.Client{Transport: appealNotificationTransport(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/getChat"):
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"username":"rfchost"}}`))}, nil
			case strings.HasSuffix(r.URL.Path, "/sendMessage"):
				_ = json.NewDecoder(r.Body).Decode(&sentPayload)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
			default:
				t.Fatalf("unexpected method %s", r.URL.Path)
				return nil, nil
			}
		})}
		state, _ := w.send(context.Background(), store.ModerationNotification{
			CaseID:         2849,
			UserID:         8258749205,
			ChatID:         -1002699516772,
			SourceKind:     "join_check",
			Action:         "ban",
			Explanation:    "入群资料检查（ad@引流）：简介引导频道私信",
			Summary:        "个人简介：Channel DM / 有事频道私信 https://t.me/zyh099 <tag>",
			EventKind:      "appeal_result",
			EventText:      "✅ 申诉审核通过 · APL-10\n群聊：RFCHOST官方群组\n关联处理：CASE-2849\n审核结果：审核通过，原封禁已解除\n处理说明：管理员通过 Bot 私信审核通过",
			AppealID:       10,
			AppealDecision: "approve",
			AppealStatus:   "completed",
		})
		if state != "sent" || sentPayload.ParseMode != "HTML" {
			t.Fatalf("state=%s parse_mode=%q", state, sentPayload.ParseMode)
		}
		for _, want := range []string{"重要提醒", "重新入群前请先修改或清空个人简介", "<blockquote>个人简介：Channel DM", "&lt;tag&gt;"} {
			if !strings.Contains(sentPayload.Text, want) {
				t.Fatalf("missing %q in %s", want, sentPayload.Text)
			}
		}
	})
}
