package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	tele "gopkg.in/telebot.v3"
)

func TestNativePausedAndCallbackGrantBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &Service{cfg: config.Config{AssistantEngine: "paused", AssistantBrokerSecret: strings.Repeat("isolated-pause-", 3)}, lifecycleCtx: ctx}
	service.assistant = NewGroupAssistant(service)
	if service.assistant.native != nil {
		t.Fatal("paused unit started native transport")
	}
	done := make(chan struct{})
	go func() { service.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("paused mode started legacy workers")
	}
	msg := &tele.Message{ID: 1, Chat: &tele.Chat{ID: -123}, Sender: &tele.User{ID: 7}}
	if err := service.handleApprovedAssistantMessage(ctx, msg, false, assistantEligibilityEligible, false, true); err != nil {
		t.Fatal(err)
	}
	for _, authorized := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodPost, "/internal/native/state", nil)
		if authorized {
			r.Header.Set("Authorization", "Bearer "+service.cfg.AssistantBrokerSecret)
		}
		w := httptest.NewRecorder()
		service.NativeAssistantHandler().ServeHTTP(w, r)
		if authorized && (w.Code != 200 || !strings.Contains(w.Body.String(), `"engine":"paused"`)) {
			t.Fatal(w.Code, w.Body.String())
		}
		if !authorized && w.Code != 401 {
			t.Fatal("state leaked without authentication", w.Code)
		}
	}
	n := &NativeAssistant{a: service.assistant}
	grant := nativeGrant{GroupID: -123, TopicID: 17, ActorID: 7, MessageID: 901, CallbackID: "one"}
	for _, req := range []nativeTelegramRequest{
		{Method: "sendMessage", Data: map[string]any{"chat_id": -123, "text": "denied"}},
		{Method: "answerCallbackQuery", Data: map[string]any{"callback_query_id": "other"}},
		{Method: "deleteMessage", Data: map[string]any{"chat_id": -123, "message_id": 902}},
		{Method: "editMessageText", Data: map[string]any{"chat_id": -123, "message_id": 902, "text": "denied"}},
		{Method: "banChatMember", Data: map[string]any{"chat_id": -123, "user_id": 7}},
	} {
		raw, _ := json.Marshal(req)
		if _, err := n.telegram(ctx, grant, nativeEnvelope{Payload: raw, Turn: "callback:one"}); err == nil {
			t.Fatalf("callback accepted %s", req.Method)
		}
	}
}
