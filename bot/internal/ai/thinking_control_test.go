package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openclaw/clawguard/internal/config"
	"go.uber.org/zap"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestModerationThinkingOptIn(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("disabled=%t/batch=%t", disabled, batch), func(t *testing.T) {
				seen := false
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					seen = true
					thinking, exists := body["thinking"]
					if disabled {
						if !exists || thinking.(map[string]any)["type"] != "disabled" {
							t.Errorf("missing disabled thinking: %#v", body)
						}
					} else if exists {
						t.Errorf("default request changed: %#v", body)
					}
					if body["max_tokens"] != float64(512) {
						t.Errorf("token budget changed")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"items":[{"verdict":"ad","confidence":0.95,"category":"引流","reason":"test"}]}`}, "finish_reason": "stop"}}})
				}))
				defer srv.Close()
				ref := NewModelRef("p", "m")
				models := fakeModelRegistry{items: map[ModelRef]Model{ref: {ID: 1, ProviderID: 1, ProviderKey: "p", ModelKey: "m", Enabled: true, CapabilityTags: []string{"moderation"}}}}
				providers := moderatorProviderRegistry{clients: map[string]LLMClient{"p": NewOpenAICompatibleClient(srv.URL, "test", time.Second, nil)}}
				moderator := NewModerator(context.Background(), zap.NewNop(), nil, nil, providers, models, NewResolver(models), nil)
				input := CheckInput{ChatID: 1, UserID: 2, Text: "test", Policy: config.AIPolicy{PrimaryModelRef: ref.String(), TimeoutMs: 1000, DisableThinking: disabled}}
				var err error
				if batch {
					_, err = moderator.checkBatch(context.Background(), []CheckInput{input})
				} else {
					_, err = moderator.checkSingle(context.Background(), input)
				}
				if err != nil {
					t.Fatal(err)
				}
				if !seen {
					t.Fatal("request not sent")
				}
			})
		}
	}
}
func TestThinkingPolicyCacheIsolation(t *testing.T) {
	m := &Moderator{}
	p := config.AIPolicy{}
	before := m.policyFingerprint("message", p)
	p.DisableThinking = true
	if before == m.policyFingerprint("message", p) {
		t.Fatal("policy cache not isolated")
	}
}
