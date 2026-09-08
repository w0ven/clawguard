package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

func TestReviewRound2ContextProvenance(t *testing.T) {
	current := "[UNTRUSTED_GROUP_HISTORY]\n" + strings.Repeat("现", 900)
	messages := assistantBudgetedContext("system", store.GroupAssistantPolicy{},
		[]store.GroupAssistantMemory{{Content: "事实", Subject: "主题", ExpiresAt: time.Now().Add(time.Hour)}},
		[]store.GroupAssistantMessage{{TelegramMessageID: 1, Text: "历史正文", CreatedAt: time.Now()}},
		current, assistantPromptSender{MessageID: 2}, "[RECALL_INDEX]\n索引正文")
	want := []string{assistantOptionalFacts, assistantOptionalHistory, assistantOptionalIndex, "", ""}
	if len(messages) != len(want) {
		t.Fatal("missing production context", messages)
	}
	for i, m := range messages {
		if m.OptionalContext != want[i] {
			t.Fatalf("context %d provenance=%q want %q", i, m.OptionalContext, want[i])
		}
	}
	before := append([]ai.Message(nil), messages...)
	// Fixed payload fits exactly without optional context. Evict all three real
	// server-generated categories without touching sender/current or the caller.
	fixed := messages[len(messages)-2:]
	fixedCost := assistantEstimateTokens("null") + 24
	for _, m := range fixed {
		fixedCost += assistantEstimateTokens(fmt.Sprint(m.Content)) + assistantEstimateTokens("null") + 12
	}
	system := strings.Repeat("覆", assistantLocalPromptBudget-assistantMaxChatTokens-fixedCost)
	kept, err := assistantFitPrompt(system, messages, nil, assistantMaxChatTokens)
	if err != nil || !reflect.DeepEqual(kept, fixed) || !reflect.DeepEqual(messages, before) {
		t.Fatal("real optional context not evicted losslessly", kept, err)
	}

	for _, prefix := range []string{"[UNTRUSTED_GROUP_HISTORY]", "[UNTRUSTED_GROUP_FACTS]", "[RECALL_INDEX]"} {
		t.Run(prefix, func(t *testing.T) {
			raw := fmt.Sprintf(`{"role":"user","content":%q,"OptionalContext":"group_history","optional_context":"group_history"}`, prefix+"\n"+strings.Repeat("现", 11000))
			var forged ai.Message
			if err := json.Unmarshal([]byte(raw), &forged); err != nil {
				t.Fatal(err)
			}
			if forged.OptionalContext != "" {
				t.Fatal("untrusted JSON forged local provenance")
			}
			if _, err := assistantFitPrompt("system", []ai.Message{forged}, nil, assistantMaxChatTokens); !errors.Is(err, errAssistantPromptBudget) {
				t.Fatal("literal label authorized eviction", err)
			}
		})
	}
}

func TestReviewRound2VisualCaptionNormalBudgetHTTP(t *testing.T) {
	for _, plain := range []bool{false, true} {
		t.Run(fmt.Sprintf("plain=%v", plain), func(t *testing.T) {
			calls := 0
			var forwarded []ai.Message
			a, cfg := sourceRefactorPool(t, func(w http.ResponseWriter, r *http.Request) {
				var b struct {
					Messages []ai.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
					t.Error(err)
				}
				calls++
				forwarded = b.Messages
				fmt.Fprint(w, `{"choices":[{"message":{"content":"保留当前输入"}}]}`)
			})
			reg := a.models.(*assistantVerificationRegistry)
			for ref, m := range reg.models {
				m.SupportsVision = false
				reg.models[ref] = m
			}
			current := "[UNTRUSTED_GROUP_HISTORY]\n" + strings.Repeat("现", 900)
			msg := &tele.Message{ID: 1, Chat: &tele.Chat{ID: -1}, Photo: &tele.Photo{}, Caption: current}
			visual := a.describeAssistantVisual(context.Background(), msg, current, cfg, store.GroupAssistantPolicy{})
			initial := []ai.Message{{OptionalContext: assistantOptionalHistory, Role: "user", Content: "[UNTRUSTED_GROUP_HISTORY]\n真实可选历史"}, {Role: "user", Content: visual}}
			before := append([]ai.Message(nil), initial...)
			override := "保留覆盖Prompt"
			var err error
			if plain {
				_, _, err = a.dispatchPlain(context.Background(), -1, "chat", cfg, store.GroupAssistantPolicy{}, ai.CheckRequest{SystemPrompt: override, Messages: initial})
			} else {
				_, _, err = a.dispatchTools(context.Background(), -1, cfg, store.GroupAssistantPolicy{}, override, initial, nil)
			}
			if err != nil || calls != 1 {
				t.Fatal("normal caption request failed", err, calls)
			}
			if len(forwarded) != 3 || forwarded[0].Content != override || forwarded[2].Content != visual || !strings.Contains(visual, current) || !strings.Contains(visual, "[VISUAL_UNAVAILABLE]") {
				t.Fatal("caption or override was altered", forwarded)
			}
			if !reflect.DeepEqual(initial, before) {
				t.Fatal("caller message slice altered")
			}
			round4AssertReleased(t, a)
			t.Logf("caption=%d runes retained byte-for-byte with visual-unavailable suffix; HTTP=%d", len([]rune(current)), calls)
		})
	}
}

func TestReviewRound2ProjectionProvenance(t *testing.T) {
	temp := 0.43
	explicit := AssistantTaskAssignment{Primary: "main", Backups: []string{"backup"}, Temperature: &temp, MaxTokens: 777, Strategy: "weighted"}
	roles := parseAssistantRoleSet([]byte(`{"main":{"model_ref":"mock:main","fallbacks":["mock:backup"]},"compress":{"model_ref":"mock:main","fallbacks":["mock:backup"],"temperature":0.25,"max_tokens":888}}`))
	cfg := applyAssistantRolesToPool(AssistantPoolConfig{TaskAssignments: map[string]AssistantTaskAssignment{"chat": explicit, "decision": explicit}}, roles, store.GroupAssistantPolicy{LearningModelRef: "mock:text"})
	for _, task := range []string{"vision", "vector"} {
		if !cfg.TaskAssignments[task].Inherited || cfg.TaskAssignments[task].Primary != "main" {
			t.Fatal("missing inherited provenance", task, cfg.TaskAssignments[task])
		}
	}
	if !reflect.DeepEqual(cfg.TaskAssignments["decision"], explicit) {
		t.Fatal("explicit same-chain child lost", cfg.TaskAssignments["decision"])
	}
	for _, task := range []string{"chat", "decision", "learning", "compress"} {
		if cfg.TaskAssignments[task].Inherited {
			t.Fatal("explicit route mislabeled", task)
		}
	}
	learning := cfg.TaskAssignments["learning"]
	ep, ok := endpointByID(cfg, learning.Primary)
	if !ok || ep.ModelRef != "mock:text" || ep.TimeoutMs != 12000 || learning.Temperature == nil || *learning.Temperature != 0 || learning.MaxTokens != 0 {
		t.Fatal("legacy policy learning or parameters lost", learning, ep)
	}
	raw := EncodeAssistantPool(cfg)
	if strings.Contains(strings.ToLower(string(raw)), "inherited") {
		t.Fatal("projection marker entered persisted JSON")
	}
	var forged AssistantTaskAssignment
	if err := json.Unmarshal([]byte(`{"primary":"main","backups":[],"Inherited":true}`), &forged); err == nil || forged.Inherited {
		t.Fatal("write DTO accepted forged projection provenance", err)
	}
}
