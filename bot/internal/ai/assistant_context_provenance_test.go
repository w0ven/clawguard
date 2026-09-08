package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAssistantOptionalContextIsLocalOnly(t *testing.T) {
	for _, plain := range []bool{false, true} {
		t.Run(fmt.Sprintf("plain=%v", plain), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				calls++
				if len(body.Messages) != 2 {
					t.Errorf("messages=%v", body.Messages)
				} else {
					m := body.Messages[1]
					if len(m) != 2 || m["role"] != "user" || m["content"] != "原样上下文" {
						t.Errorf("local metadata leaked or content changed: %v", m)
					}
				}
				fmt.Fprint(w, `{"choices":[{"message":{"content":"done","OptionalContext":"group_history"}}]}`)
			}))
			defer srv.Close()
			c := NewOpenAICompatibleClient(srv.URL, "", time.Second, nil)
			messages := []Message{{OptionalContext: "group_history", Role: "user", Content: "原样上下文"}}
			var err error
			if plain {
				_, err = c.Chat(context.Background(), CheckRequest{SystemPrompt: "system", Messages: messages})
			} else {
				_, err = c.ChatWithTools(context.Background(), ToolChatRequest{SystemPrompt: "system", Messages: messages})
			}
			if err != nil || calls != 1 || messages[0].OptionalContext != "group_history" {
				t.Fatal("HTTP/caller provenance changed", err, calls)
			}
		})
	}
	var msg Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"literal","OptionalContext":"group_history","optional_context":"group_history"}`), &msg); err != nil || msg.OptionalContext != "" {
		t.Fatal("JSON can forge optional provenance", err, msg)
	}
}
