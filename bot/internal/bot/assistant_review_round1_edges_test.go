package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

func TestReviewRound1OrdinaryInputNeverEnablesProactive(t *testing.T) {
	var calls atomic.Int32
	a, _, p, _, sender := reviewRound1PG(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"unexpected"}}]}`)
	}, 1)
	msg := &tele.Message{ID: 1, Chat: &tele.Chat{ID: p.ChatID}, ThreadID: 7, Sender: &tele.User{ID: 12}, Text: "没有点名的普通消息"}
	if err := a.service.handleApprovedAssistantMessage(a.ctx, msg, false, assistantEligibilityEligible, false, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.replyMu.Lock()
		n := len(a.replyBatches)
		a.replyMu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending batch did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count, _ := sender.snapshot(); count != 0 || calls.Load() != 0 {
		t.Fatalf("proactive-off emitted model/TG requests: %d/%d", calls.Load(), count)
	}
	actual, err := a.Policy(a.ctx, p.ChatID)
	if err != nil || actual.ProactiveInterjectEnabled {
		t.Fatal("proactive setting changed", actual, err)
	}
}

func reviewRound1SinglePool(t *testing.T, h http.HandlerFunc) (*GroupAssistant, AssistantPoolConfig) {
	t.Helper()
	a, cfg := sourceRefactorPool(t, h)
	cfg.Strategy = "primary-overflow"
	cfg.Endpoints = cfg.Endpoints[:1]
	cfg.Endpoints[0].MaxConcurrency = 1
	cfg.MaxQueueWaitSec = 5
	cfg.TaskAssignments = map[string]AssistantTaskAssignment{"chat": {Primary: "main"}, "vector": {Primary: "main"}, "decision": {Primary: "main"}}
	return a, cfg
}
func reviewRound1Active(a *GroupAssistant, ref string) int {
	rt := a.runtimeSnapshot(ref)
	if rt == nil {
		return 0
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.active
}

func TestReviewRound1BorrowKeepsForeignGroupQueued(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var chatCalls atomic.Int32
	a, cfg := reviewRound1SinglePool(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/embeddings" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"data":[{"embedding":[1,0]}]}`)
			return
		}
		chatCalls.Add(1)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
	})
	parent, err := a.acquire(context.Background(), -1, "chat", cfg, true, store.GroupAssistantPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.finish(false, context.Canceled)
	ctx := context.WithValue(context.Background(), assistantToolReservationKey{}, assistantToolReservation{chatID: -1, lease: parent})
	nested := make(chan error, 1)
	go func() {
		_, _, e := a.dispatchEmbedding(ctx, -1, cfg, store.GroupAssistantPolicy{}, "query")
		nested <- e
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("own embedding could not enter")
	}
	foreign := make(chan error, 1)
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, e := a.dispatchPlain(c, -2, "decision", cfg, store.GroupAssistantPolicy{}, ai.CheckRequest{})
		foreign <- e
	}()
	deadline := time.Now().Add(time.Second)
	for {
		a.queue.mu.Lock()
		n := a.queue.depth[-2]
		a.queue.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("foreign group did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	if n := reviewRound1Active(a, "mock:main"); n != 1 {
		t.Fatalf("nested execution changed user capacity: active=%d", n)
	}
	close(release)
	if err = <-nested; err != nil {
		t.Fatal(err)
	}
	if n := reviewRound1Active(a, "mock:main"); n != 1 || chatCalls.Load() != 0 {
		t.Fatalf("borrow prematurely released parent/foreign call: active=%d calls=%d", n, chatCalls.Load())
	}
	parent.finish(true, nil)
	if err = <-foreign; err != nil {
		t.Fatal(err)
	}
	if chatCalls.Load() != 1 {
		t.Fatal("foreign request not served after parent release")
	}
	round4AssertReleased(t, a)
	t.Log("same model cap=1: nested embedding used one reserved slot; foreign group remained queued until the original turn released it")
}
func TestReviewRound1BorrowCancellationDoesNotReleaseParent(t *testing.T) {
	entered := make(chan struct{}, 1)
	stop := make(chan struct{})
	defer close(stop)
	a, cfg := reviewRound1SinglePool(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // allow net/http to observe peer cancellation
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	})
	parent, err := a.acquire(context.Background(), -1, "chat", cfg, true, store.GroupAssistantPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.finish(false, context.Canceled)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), assistantToolReservationKey{}, assistantToolReservation{chatID: -1, lease: parent}))
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, e := a.dispatchEmbedding(ctx, -1, cfg, store.GroupAssistantPolicy{}, "query"); done <- e }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("nested request did not start")
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not propagated", err)
	}
	if n := reviewRound1Active(a, "mock:main"); n != 1 {
		t.Fatalf("nested cancel lost parent reservation: %d", n)
	}
	parent.finish(false, context.Canceled)
	round4AssertReleased(t, a)
}
func TestReviewRound1WeightedBorrowDiscardPreservesParent(t *testing.T) {
	var model string
	a, cfg := sourceRefactorPool(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		model = b.Model
		fmt.Fprint(w, `{"data":[{"embedding":[1,0]}]}`)
	})
	cfg.Endpoints = cfg.Endpoints[:2]
	cfg.Endpoints[0].Weight = 1
	cfg.Endpoints[1].Weight = 5
	for i := range cfg.Endpoints {
		cfg.Endpoints[i].MaxConcurrency = 1
	}
	cfg.TaskAssignments = map[string]AssistantTaskAssignment{"chat": {Primary: "main"}, "vector": {Primary: "main", Backups: []string{"backup"}}}
	primary := cfg
	primary.Strategy = "primary-overflow"
	parent, err := a.acquire(context.Background(), -1, "chat", primary, true, store.GroupAssistantPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.finish(false, context.Canceled)
	ctx := context.WithValue(context.Background(), assistantToolReservationKey{}, assistantToolReservation{chatID: -1, lease: parent})
	if _, _, err = a.dispatchEmbedding(ctx, -1, cfg, store.GroupAssistantPolicy{}, "query"); err != nil {
		t.Fatal(err)
	}
	if model != "backup" || reviewRound1Active(a, "mock:main") != 1 {
		t.Fatalf("discarded weighted candidate released parent: model=%s active=%d", model, reviewRound1Active(a, "mock:main"))
	}
	parent.finish(true, nil)
	round4AssertReleased(t, a)
}

func TestReviewRound1BoundedTargetsAndFinalBudget(t *testing.T) {
	chat := &tele.Chat{ID: -1}
	first := &tele.Message{ID: 501, Chat: chat, ThreadID: 7, Text: "本批第一条", ReplyTo: &tele.Message{ID: 1, Chat: chat, ThreadID: 7}}
	latest := &tele.Message{ID: 502, Chat: chat, ThreadID: 7, Text: "本批第二条", ReplyTo: &tele.Message{ID: 2, Chat: &tele.Chat{ID: -2}}}
	history := []store.GroupAssistantMessage{}
	for i := 1; i <= 500; i++ {
		history = append(history, store.GroupAssistantMessage{ChatID: -1, ThreadID: 7, TelegramMessageID: int64(i), Text: strings.Repeat("大历史", 150)})
	}
	ctx := context.WithValue(context.Background(), assistantBatchItemsKey{}, []assistantReplyBatchItem{{msg: first}, {msg: latest}})
	prompt, targets := assistantReplyTargets(ctx, latest, history)
	if targets["first_input"].ID != 501 || targets["latest_input"].ID != 502 || targets["input_1_reply_target"].ID != 1 {
		t.Fatal("SGB batch aliases missing", targets)
	}
	if targets["message_2"] != nil || targets["latest_reply_target"] != nil || targets["message_500"] != nil {
		t.Fatal("unrelated/foreign history became reply candidates")
	}
	if assistantEstimateTokens(prompt) > 2048 {
		t.Fatal("candidate preview bypassed budget")
	}
	a, cfg := sourceRefactorPool(t, func(http.ResponseWriter, *http.Request) { t.Error("oversized fixed prompt must not reach an upstream") })
	override := strings.Repeat("用户覆盖", 4000)
	_, _, err := a.dispatchTools(context.Background(), -1, cfg, store.GroupAssistantPolicy{}, override, []ai.Message{{Role: "user", Content: "当前输入"}}, nil)
	if !errors.Is(err, errAssistantPromptBudget) {
		t.Fatal("oversized override not rejected truthfully", err)
	}
	_, _, err = a.dispatchPlain(context.Background(), -1, "chat", cfg, store.GroupAssistantPolicy{}, ai.CheckRequest{SystemPrompt: override, Messages: []ai.Message{{Role: "user", Content: "当前输入"}}})
	if !errors.Is(err, errAssistantPromptBudget) {
		t.Fatal("plain chat bypassed final prompt budget", err)
	}
	round4AssertReleased(t, a)
	for _, ref := range []string{"mock:main", "mock:backup", "mock:text"} {
		if rt := a.runtimeSnapshot(ref); rt != nil && rt.statusValue() == "unhealthy" {
			t.Fatal("local budget error poisoned provider health")
		}
	}
	fixed := []ai.Message{{Role: "user", Content: "[CURRENT_USER_MESSAGE]\n保留"}, {Role: "assistant", ToolCalls: []ai.ToolCall{{ID: "done"}}}, {Role: "tool", ToolCallID: "done", Content: strings.Repeat("真实结果", 4000)}}
	if _, err := assistantFitPrompt("system", fixed, nil, 1200); !errors.Is(err, errAssistantPromptBudget) {
		t.Fatal("oversized executed transcript silently altered")
	}
}
