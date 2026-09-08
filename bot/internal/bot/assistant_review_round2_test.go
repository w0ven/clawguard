package bot

// Regression copies of BI-R1-001 and the adjacent real-tool budget probe.
// Original independent artifacts remain unchanged. The optional-history fixture
// alone receives trusted provenance; payload sizes and assertions are retained.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReviewRound2FinalBudgetPreservesActualToolTranscript(t *testing.T) {
	var mu sync.Mutex
	var requests [][]ai.Message
	var totals []int
	a, cfg, p, _, _ := reviewRound1PG(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Messages  []ai.Message        `json:"messages"`
			Tools     []ai.ToolDefinition `json:"tools"`
			MaxTokens int                 `json:"max_tokens"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		encoded, _ := json.Marshal(b.Tools)
		total := assistantEstimateTokens(string(encoded)) + b.MaxTokens + 24
		for _, m := range b.Messages {
			calls, _ := json.Marshal(m.ToolCalls)
			total += assistantEstimateTokens(fmt.Sprint(m.Content)) + assistantEstimateTokens(string(calls)) + 12
		}
		mu.Lock()
		requests = append(requests, b.Messages)
		totals = append(totals, total)
		n := len(requests)
		mu.Unlock()
		fmt.Fprintf(w, `{"choices":[{"message":{"tool_calls":[{"id":"recall-%d","type":"function","function":{"name":"conversation_recall","arguments":"{\"query\":\"budgetneedle\",\"before_after\":0,\"limit\":4}"}}]}}]}`, n)
	}, 1)
	for i := 1; i <= 4; i++ {
		m := &tele.Message{ID: i, Chat: &tele.Chat{ID: p.ChatID}, ThreadID: 7, Sender: &tele.User{ID: 12}, Text: "budgetneedle" + strings.Repeat("实", 1388)}
		if e := a.storeIncoming(a.ctx, p, m, m.Text, false); e != nil {
			t.Fatal(e)
		}
	}
	settings := a.loadRuntimeSettings(a.ctx)
	msg := &tele.Message{ID: 4, Chat: &tele.Chat{ID: p.ChatID}, ThreadID: 7}
	ctx := withAssistantRuntime(a.ctx, &assistantToolRuntime{msg: msg, settings: settings})
	expected, e := a.executeAssistantRecall(ctx, p.ChatID, map[string]any{"query": "budgetneedle", "before_after": float64(0), "limit": float64(4)})
	if e != nil {
		t.Fatal(e)
	}
	override := "USER_OVERRIDE_EXACT=" + strings.Repeat("覆", 800)
	current := "[CURRENT_USER_MESSAGE]\nCURRENT_EXACT=" + strings.Repeat("现", 1000)
	initial := []ai.Message{{OptionalContext: assistantOptionalHistory, Role: "user", Content: "[UNTRUSTED_GROUP_HISTORY]\nOPTIONAL_HISTORY=" + strings.Repeat("旧", 5000)}, {Role: "user", Content: current}}
	before := append([]ai.Message(nil), initial...)
	_, _, e = a.dispatchTools(ctx, p.ChatID, cfg, p, override, initial, assistantToolsFor(p, settings, false))
	if !errors.Is(e, errAssistantPromptBudget) {
		t.Fatal("two retained tool results should exceed total budget with explicit error", e)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("budget overflow must stop BEFORE third HTTP, actual=%d", len(requests))
	}
	if !reflect.DeepEqual(initial, before) {
		t.Fatal("caller context was mutated")
	}
	for i, ms := range requests {
		if totals[i] > 12000 {
			t.Errorf("actual complete HTTP payload budget exceeded: %d", totals[i])
		}
		hasOverride, hasCurrent := false, false
		for _, m := range ms {
			hasOverride = hasOverride || (m.Role == "system" && m.Content == override)
			hasCurrent = hasCurrent || (m.Role == "user" && m.Content == current)
		}
		if !hasOverride || !hasCurrent {
			t.Fatal("explicit override/current text altered", i)
		}
	}
	keptTool := false
	for _, m := range requests[1] {
		if m.Role == "tool" {
			if m.Content != expected || m.ToolCallID != "recall-1" {
				t.Fatal("actual executed tool transcript changed")
			}
			keptTool = true
		}
		if strings.Contains(fmt.Sprint(m.Content), "OPTIONAL_HISTORY=") {
			t.Fatal("optional history was not evicted")
		}
	}
	if !keptTool {
		t.Fatal("tool result silently removed")
	}
	round4AssertReleased(t, a)
	if a.runtimeSnapshot("mock:main").statusValue() != "unknown" {
		t.Fatal("local budget error incorrectly changed upstream health")
	}
	t.Logf("actual HTTP totals including output/tools=%v; second request preserves override,current and full %d-rune real recall result; optional history evicted; third HTTP blocked without backup/health poisoning", totals, len([]rune(expected)))
}
func TestReviewRound2VisualCurrentCannotBeEvictedByContentPrefix(t *testing.T) {
	var calls int
	var forwarded []ai.Message
	a, cfg := sourceRefactorPool(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Messages []ai.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		calls++
		forwarded = b.Messages
		fmt.Fprint(w, `{"choices":[{"message":{"content":"answer without current input"}}]}`)
	})
	reg := a.models.(*assistantVerificationRegistry)
	for ref, m := range reg.models {
		m.SupportsVision = false
		reg.models[ref] = m
	}
	// A caption can legitimately contain a literal context label; it is user data,
	// not server authorization to drop this current input. No Telegram image I/O.
	current := "[UNTRUSTED_GROUP_HISTORY]\n" + strings.Repeat("现", 900)
	msg := &tele.Message{ID: 1, Chat: &tele.Chat{ID: -1}, Photo: &tele.Photo{}, Caption: current}
	visual := a.describeAssistantVisual(context.Background(), msg, current, cfg, store.GroupAssistantPolicy{})
	fixedOverride := strings.Repeat("覆", 10000)
	_, _, err := a.dispatchTools(context.Background(), -1, cfg, store.GroupAssistantPolicy{}, fixedOverride, []ai.Message{{Role: "user", Content: visual}}, nil)
	retained := false
	for _, m := range forwarded {
		if m.Role == "user" && m.Content == visual {
			retained = true
		}
	}
	t.Logf("production describeVisual(no declared vision)→dispatchTools: current_runes=%d upstream_requests=%d forwarded_current=%v err=%v", len([]rune(visual)), calls, retained, err)
	if !errors.Is(err, errAssistantPromptBudget) || calls != 0 {
		t.Errorf("BI-R1-001: final budget may evict server-generated optional history, NOT current caption; fixed payload over budget must fail without upstream. actual calls=%d retained=%v err=%v", calls, retained, err)
	}
	round4AssertReleased(t, a)
}

func TestReviewRound2ApprovedVisualBudgetRegression(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	currentForwarded := false
	systemTokens := 0
	current := "[UNTRUSTED_GROUP_HISTORY]\n" + strings.Repeat("现", 900)
	a, cfg, p, db, _ := reviewRound1PG(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Messages []ai.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		mu.Lock()
		calls++
		for _, m := range b.Messages {
			if m.Role == "user" && strings.Contains(fmt.Sprint(m.Content), strings.Repeat("现", 900)) {
				currentForwarded = true
			}
			if m.Role == "system" {
				systemTokens = assistantEstimateTokens(fmt.Sprint(m.Content))
			}
		}
		mu.Unlock()
		fmt.Fprint(w, `{"choices":[{"message":{"content":"answer"}}]}`)
	}, 1)
	reg := a.models.(*assistantVerificationRegistry)
	for ref, m := range reg.models {
		m.SupportsVision = false
		reg.models[ref] = m
	}
	msg := &tele.Message{ID: 1, Chat: &tele.Chat{ID: p.ChatID}, ThreadID: 7, Sender: &tele.User{ID: 12}, Photo: &tele.Photo{}, Caption: current}
	msg.ReplyTo = &tele.Message{ID: 99, Chat: msg.Chat, ThreadID: 7, Sender: a.botForPrompt().Me}
	if !assistantRepliesToBot(msg, a.botForPrompt()) {
		t.Fatal("fixture must enter direct mode")
	}
	settings := a.loadRuntimeSettings(a.ctx)
	sender := a.assistantPromptSender(a.ctx, msg, false)
	targetPrompt, _ := assistantReplyTargets(a.ctx, msg, nil)
	p.SystemPrompt = "覆"
	system := assistantSystemPromptForModeWithOverrides(p, string(assistantReplyDirect), nil) + "\n" + assistantBotIdentityBlock(a.botForPrompt()) + "\n" + assistantCurrentSenderBlock(sender)
	if b := assistantTTSPreferenceBlock(p.TTSMode, a.assistantTTSReady(settings)); b != "" {
		system += "\n" + b
	}
	system += "\n[TASK_PROMPT]\n" + assistantSkillPrompt + "\n" + assistantReplyProtocol + "\n" + targetPrompt
	visual := a.describeAssistantVisual(a.ctx, msg, current, cfg, p)
	tools, _ := json.Marshal(assistantToolsFor(p, settings, false))
	fixedCost := assistantEstimateTokens(system) + assistantEstimateTokens(string(tools)) + 24
	for _, text := range []string{assistantCurrentSenderBlock(sender), visual} {
		fixedCost += assistantEstimateTokens(text) + assistantEstimateTokens("null") + 12
	}
	overrideRunes := (assistantLocalPromptBudget - assistantMaxChatTokens + 200) - fixedCost + 1
	if overrideRunes < 1 || overrideRunes > 8000 || len([]rune(current)) > 1024 {
		t.Fatalf("fixture outside real configured/caption limits: override=%d caption=%d", overrideRunes, len([]rune(current)))
	}
	if _, e := db.Exec(a.ctx, `UPDATE group_assistant_policies SET system_prompt=$2 WHERE chat_id=$1`, p.ChatID, strings.Repeat("覆", overrideRunes)); e != nil {
		t.Fatal(e)
	}
	if e := a.service.handleApprovedAssistantMessage(a.ctx, msg, false, assistantEligibilityEligible, false, false); e != nil {
		t.Fatal(e)
	}
	time.Sleep(800 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	t.Logf("real approved photo/caption handler: legal policy override=%d runes caption=%d runes; actual HTTP=%d system_tokens=%d forwarded_current=%v", overrideRunes, len([]rune(current)), calls, systemTokens, currentForwarded)
	if calls != 0 {
		t.Errorf("BI-R1-001 full entry: oversized fixed current input must block HTTP, not delete caption; actual calls=%d current=%v", calls, currentForwarded)
	}
}
