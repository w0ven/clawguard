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
