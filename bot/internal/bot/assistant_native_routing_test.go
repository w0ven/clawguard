package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

func TestNativeModelDeniesModerationToolsWithoutCallingProviders(t *testing.T) {
	a, _ := assistantVerificationPool(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("moderation tools must not reach a provider")
	})
	n := &NativeAssistant{a: a, turns: map[string]*nativeTurn{}}
	for _, name := range []string{"rule_manage", "vote_ban"} {
		raw, _ := json.Marshal(nativeModelRequest{
			Role: "chat",
			Tools: []ai.ToolDefinition{{
				Type:     "function",
				Function: ai.ToolFunction{Name: name, Parameters: map[string]any{}},
			}},
		})
		if _, err := n.model(context.Background(), nativeGrant{GroupID: -1}, nativeEnvelope{Turn: "1", Payload: raw}); err == nil || !strings.Contains(err.Error(), "moderation tool denied") {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestNativePreserveMessagesSkipsLegacy12000Cap(t *testing.T) {
	huge := strings.Repeat("预算", 8000)
	var seen map[string]any
	a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "ok"}}}})
	})
	result, _, err := a.dispatchPlain(context.Background(), -1, "chat", cfg, store.GroupAssistantPolicy{}, ai.CheckRequest{
		PreserveMessages: true,
		Messages:         []ai.Message{{Role: "system", Content: huge}, {Role: "user", Content: huge}},
		MaxTokens:        2048,
		Temperature:      0.4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "ok" {
		t.Fatalf("unexpected content %q", result.Content)
	}
	messages := seen["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("source messages rewritten: %#v", seen["messages"])
	}
	first := messages[0].(map[string]any)["content"].(string)
	if len([]rune(first)) < 8000 {
		t.Fatalf("legacy 12000 cap truncated native payload: %d runes", len([]rune(first)))
	}
}

func TestNativeConstructorDoesNotStartLegacyWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &Service{cfg: config.Config{AssistantEngine: "native", AssistantBrokerSecret: strings.Repeat("isolated-native-", 3), AssistantNativeURL: "http://127.0.0.1:9"}, lifecycleCtx: ctx}
	service.assistant = NewGroupAssistant(service)
	if service.assistant.native == nil {
		t.Fatal("native constructor did not start native transport")
	}
	cancel()
	done := make(chan struct{})
	go func() { service.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("native constructor leaked legacy workers")
	}
}
