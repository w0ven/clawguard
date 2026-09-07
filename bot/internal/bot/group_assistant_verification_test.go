package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

type assistantVerificationRegistry struct {
	models  map[ai.ModelRef]ai.Model
	clients map[string]ai.LLMClient
}

func (r *assistantVerificationRegistry) Get(ref ai.ModelRef) (ai.Model, bool) {
	v, ok := r.models[ref]
	return v, ok
}
func (r *assistantVerificationRegistry) List(ai.ModelFilter) []ai.Model {
	out := []ai.Model{}
	for _, v := range r.models {
		out = append(out, v)
	}
	return out
}
func (r *assistantVerificationRegistry) Reload(context.Context) error { return nil }

type assistantVerificationProviders struct {
	r *assistantVerificationRegistry
}

func (p assistantVerificationProviders) GetByKey(key string) (ai.Provider, bool) {
	_, ok := p.r.clients[key]
	return ai.Provider{Key: key, Enabled: ok}, ok
}
func (p assistantVerificationProviders) List() []ai.Provider { return nil }
func (p assistantVerificationProviders) Client(key string) (ai.LLMClient, bool) {
	v, ok := p.r.clients[key]
	return v, ok
}
func (p assistantVerificationProviders) Reload(context.Context) error { return nil }
func assistantVerificationPool(t *testing.T, h http.HandlerFunc) (*GroupAssistant, AssistantPoolConfig) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	r := &assistantVerificationRegistry{models: map[ai.ModelRef]ai.Model{}, clients: map[string]ai.LLMClient{"mock": ai.NewOpenAICompatibleClient(srv.URL, "assistant-test-fake", time.Second, nil)}}
	for _, name := range []string{"main", "backup", "text"} {
		r.models[ai.NewModelRef("mock", name)] = ai.Model{Enabled: true, SupportsTools: name != "text", ProviderKey: "mock", ModelKey: name}
	}
	a := NewGroupAssistant(nil)
	a.models = r
	a.providers = assistantVerificationProviders{r}
	a.service = &Service{bot: &tele.Bot{Me: &tele.User{ID: 900, Username: "assistant_test_bot"}}}
	cfg := AssistantPoolConfig{Strategy: "primary-overflow", TaskAssignments: map[string]AssistantTaskAssignment{"chat": {Primary: "main", Backups: []string{"backup"}}, "learning": {Primary: "main", Backups: []string{"text"}}}, MaxQueueDepth: 1, MaxQueueWaitSec: 1}
	for i, name := range []string{"main", "backup", "text"} {
		role := "backup"
		if i == 0 {
			role = "primary"
		}
		cfg.Endpoints = append(cfg.Endpoints, AssistantPoolEndpoint{ID: name, ModelRef: "mock:" + name, Role: role, Priority: i, MaxConcurrency: 1, TimeoutMs: 1000, CooldownSeconds: 1})
	}
	return a, cfg
}
func TestAssistantVerificationPoolPrioritySharedCapacity(t *testing.T) {
	a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) { t.Error("acquire must not call provider") })
	if e := a.ValidatePoolConfig(cfg, true); e != nil {
		t.Fatal(e)
	}
	p := store.GroupAssistantPolicy{}
	first, e := a.acquire(context.Background(), -1, "chat", cfg, true, p)
	if e != nil || first.ep.ID != "main" {
		t.Fatalf("main %+v %v", first, e)
	}
	defer first.finish(true, nil)
	second, e := a.acquire(context.Background(), -2, "chat", cfg, true, p)
	if e != nil || second.ep.ID != "backup" {
		t.Fatalf("shared overflow %+v %v", second, e)
	}
	second.finish(true, nil)
	first.finish(true, nil)
	third, e := a.acquire(context.Background(), -3, "chat", cfg, true, p)
	if e != nil || third.ep.ID != "main" {
		t.Fatalf("main restored %+v %v", third, e)
	}
	third.finish(true, nil)
}
func TestAssistantVerificationPoolCanceledFreeSlot(t *testing.T) {
	a, cfg := assistantVerificationPool(t, func(http.ResponseWriter, *http.Request) {})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lease, e := a.acquire(ctx, -1, "chat", cfg, true, store.GroupAssistantPolicy{})
	if lease != nil {
		lease.finish(true, nil)
	}
	if e == nil {
		t.Fatal("canceled request acquired free provider capacity")
	}
}
func TestAssistantVerificationPoolQueue(t *testing.T) {
	a, cfg := assistantVerificationPool(t, func(http.ResponseWriter, *http.Request) {})
	cfg.TaskAssignments["chat"] = AssistantTaskAssignment{Primary: "main"}
	held, e := a.acquire(context.Background(), -1, "chat", cfg, true, store.GroupAssistantPolicy{})
	if e != nil {
		t.Fatal(e)
	}
	defer held.finish(true, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := a.acquire(ctx, -1, "chat", cfg, true, store.GroupAssistantPolicy{}); done <- e }()
	deadline := time.Now().Add(time.Second)
	for {
		a.queue.mu.Lock()
		n := a.queue.depth[-1]
		a.queue.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queue never entered")
		}
		time.Sleep(time.Millisecond)
	}
	if _, e = a.acquire(context.Background(), -1, "chat", cfg, true, store.GroupAssistantPolicy{}); e == nil || !strings.Contains(e.Error(), "full") {
		t.Fatalf("queue full: %v", e)
	}
	cancel()
	if e = <-done; e == nil {
		t.Fatal("queue cancel succeeded")
	}
	a.queue.mu.Lock()
	n := a.queue.depth[-1]
	a.queue.mu.Unlock()
	if n != 0 {
		t.Fatal("queue leaked", n)
	}
	start := time.Now()
	if _, e = a.acquire(context.Background(), -2, "chat", cfg, true, store.GroupAssistantPolicy{}); e == nil || time.Since(start) > 2*time.Second {
		t.Fatal("queue timeout", e)
	}
	a.queue.mu.Lock()
	n = a.queue.depth[-2]
	a.queue.mu.Unlock()
	if n != 0 {
		t.Fatal("timeout leaked queue", n)
	}
}
func TestAssistantVerificationPoolCooldownHalfOpen(t *testing.T) {
	rt := &assistantEndpointRuntime{limit: 3, status: "unknown"}
	if !rt.tryAcquire(time.Now()) {
		t.Fatal("initial acquire")
	}
	rt.release(false, &ai.ProviderError{StatusCode: 429, RetryAfter: 2 * time.Second, Err: errors.New("busy")}, 0, time.Second)
	if rt.tryAcquire(time.Now()) {
		t.Fatal("acquired cooldown")
	}
	rt.mu.Lock()
	until := rt.cooldownUntil
	rt.mu.Unlock()
	if time.Until(until) < time.Second {
		t.Fatal("Retry-After ignored")
	}
	if !rt.tryAcquire(until.Add(time.Millisecond)) || rt.statusValue() != "half_open" {
		t.Fatal("halfopen unavailable")
	}
	if rt.tryAcquire(until.Add(time.Millisecond)) {
		t.Fatal("multiple halfopen probes")
	}
	rt.release(true, nil, 0, time.Second)
	if rt.statusValue() != "healthy" {
		t.Fatal("success not healthy")
	}
	if !rt.tryAcquire(time.Now()) {
		t.Fatal("healthy not available")
	}
	rt.release(false, &ai.ProviderError{StatusCode: 503, Err: errors.New("unavailable")}, 0, time.Second)
	if rt.statusValue() != "unhealthy" {
		t.Fatal("5xx not unhealthy")
	}
}
func TestAssistantVerificationPoolToolAffinityAndErrors(t *testing.T) {
	var main, backup atomic.Int32
	a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string       `json:"model"`
			Messages []ai.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		if body.Model != "main" {
			backup.Add(1)
			t.Error("same turn switched endpoint")
		}
		n := main.Add(1)
		if n == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"call","type":"function","function":{"name":"shell","arguments":"{}"}}]}}]}`)
		} else {
			found := false
			for _, m := range body.Messages {
				if m.Role == "tool" && m.ToolCallID == "call" && strings.Contains(fmt.Sprint(m.Content), "tool_not_allowed") {
					found = true
				}
			}
			if !found {
				t.Error("tool failure not re-injected")
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"safe answer"}}]}`)
		}
	})
	answer, ep, e := a.dispatchTools(context.Background(), -1, cfg, store.GroupAssistantPolicy{}, "system", []ai.Message{{Role: "user", Content: "question"}}, nil)
	if e != nil || answer != "safe answer" || ep.ID != "main" || main.Load() != 2 || backup.Load() != 0 {
		t.Fatalf("affinity answer=%q ep=%s err=%v", answer, ep.ID, e)
	}
}
func TestAssistantVerificationPoolHTTP429ThenBackup(t *testing.T) {
	var main, backup atomic.Int32
	a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] == "main" {
			main.Add(1)
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
			return
		}
		backup.Add(1)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"backup answer"}}]}`)
	})
	// Observe a real provider 429 and finish its lease to establish cooldown.
	// This test covers the NEXT request; it must not require the first dispatch
	// to fail, because current-request overflow is a separate required contract.
	lease, e := a.acquire(context.Background(), -1, "chat", cfg, true, store.GroupAssistantPolicy{})
	if e != nil {
		t.Fatal(e)
	}
	client, ok := a.providers.Client("mock")
	if !ok {
		lease.finish(true, nil)
		t.Fatal("missing mock client")
	}
	_, e = client.(ai.ToolCallingClient).ChatWithTools(context.Background(), ai.ToolChatRequest{Model: "main"})
	lease.finish(false, e)
	var providerError *ai.ProviderError
	if !errors.As(e, &providerError) || providerError.StatusCode != 429 {
		t.Fatalf("provider 429 not observed: %v", e)
	}
	answer, ep, e := a.dispatchTools(context.Background(), -2, cfg, store.GroupAssistantPolicy{}, "system", nil, nil)
	if e != nil || ep.ID != "backup" || answer != "backup answer" || main.Load() != 1 || backup.Load() != 1 {
		t.Fatalf("cooldown overflow %s %s %v", answer, ep.ID, e)
	}
}
func TestAssistantVerificationPoolRuntimeRace(t *testing.T) {
	a, _ := assistantVerificationPool(t, func(http.ResponseWriter, *http.Request) {})
	for i := 0; i < 500; i++ {
		ref := fmt.Sprint("mock:", i)
		rt := a.endpointRuntime(ref, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; a.endpointRuntime(ref, 1) }()
		go func() {
			defer wg.Done()
			<-start
			if rt.tryAcquire(time.Now()) {
				rt.release(true, nil, 0, time.Second)
			}
		}()
		close(start)
		wg.Wait()
	}
}
func TestAssistantVerificationToolsRejectUnknownExtraScope(t *testing.T) {
	a := NewGroupAssistant(nil)
	for _, tc := range []struct{ name, args string }{{"shell", "{}"}, {"knowledge_query", `{"query":"a","group_id":-2}`}, {"knowledge_query", `{"query":"a","chat_id":-2}`}, {"conversation_recall", `{"query":"a","sender_id":2}`}, {"webfetch_readonly", `{"url":"https://example.org","headers":{}}`}, {"knowledge_query", `{"query":"a","limit":1.5}`}, {"knowledge_query", `{"query":"a","limit":11}`}} {
		t.Run(tc.name+tc.args, func(t *testing.T) {
			call := ai.ToolCall{}
			call.Function.Name = tc.name
			call.Function.Arguments = tc.args
			result, e := a.executeReadOnlyTool(context.Background(), -1, store.GroupAssistantPolicy{}, call)
			if e == nil || !strings.Contains(result, `"ok":false`) {
				t.Fatalf("unsafe call accepted %s %v", result, e)
			}
		})
	}
}
func TestAssistantVerificationSessionScopeAndLimits(t *testing.T) {
	a, _ := assistantVerificationPool(t, func(http.ResponseWriter, *http.Request) {})
	p := store.GroupAssistantPolicy{FollowupWindowSec: 30, MaxFollowupTurns: 3}
	m := &tele.Message{ID: 1, Chat: &tele.Chat{ID: -1}, Sender: &tele.User{ID: 2}, ThreadID: 3, Text: "normal"}
	if a.shouldTrigger(m, p) {
		t.Fatal("unsolicited normal message")
	}
	m.Text = "@assistant_test_bot question"
	if !a.shouldTrigger(m, p) {
		t.Fatal("explicit mention ignored")
	}
	a.markSession(m, p)
	m.Text = "followup"
	if !a.shouldTrigger(m, p) {
		t.Fatal("same scope followup ignored")
	}
	for _, other := range []*tele.Message{{Chat: &tele.Chat{ID: -2}, Sender: m.Sender, ThreadID: 3}, {Chat: m.Chat, Sender: &tele.User{ID: 4}, ThreadID: 3}, {Chat: m.Chat, Sender: m.Sender, ThreadID: 4}} {
		if a.shouldTrigger(other, p) {
			t.Fatal("cross scope followup")
		}
	}
	a.markSession(m, p)
	a.markSession(m, p)
	if a.shouldTrigger(m, p) {
		t.Fatal("turn limit exceeded")
	}
	a.sessions["-1:3:2"] = assistantSession{LastAt: time.Now().Add(-31 * time.Second), Turns: 1}
	if a.shouldTrigger(m, p) {
		t.Fatal("expired followup")
	}
	m.Text = "@assistant_test_bot_fake"
	if a.shouldTrigger(m, p) {
		t.Error("username prefix is not an explicit bot mention")
	}
}
func TestAssistantVerificationLearningParser(t *testing.T) {
	good := `{"facts":[{"subject":"hours","content":"closes 8pm","valid_scope":"today","expires_at":"","source_quote":"closes 8pm"}]}`
	facts, e := parseLearningFacts(good, "shop closes 8pm", 7, "learned_fact")
	if e != nil || len(facts) != 1 || facts[0].Authority != "learned_fact" {
		t.Fatalf("valid fact %+v %v", facts, e)
	}
	for _, raw := range []string{good + ` {}`, strings.Replace(good, `"subject"`, `"admin":true,"subject"`, 1), "```json\n" + good + "\n```"} {
		if _, e = parseLearningFacts(raw, "shop closes 8pm", 7, "learned_fact"); e == nil {
			t.Error("non-strict learning output accepted")
		}
	}
	for _, raw := range []string{strings.Replace(good, "closes 8pm\"}]}", "absent\"}]}", 1), strings.Replace(good, `"expires_at":""`, `"expires_at":"2000-01-01T00:00:00Z"`, 1)} {
		got, e := parseLearningFacts(raw, "shop closes 8pm", 7, "learned_fact")
		if e == nil && len(got) > 0 {
			t.Fatalf("unmatched quote/expired fact accepted %+v", got)
		}
	}
}

func TestAssistantVerificationLearningQueueBounded(t *testing.T) {
	a := NewGroupAssistant(nil)
	for i := 0; i < cap(a.learning); i++ {
		if !a.enqueueLearning(assistantLearningJob{chatID: -1, messageID: int64(i)}) {
			t.Fatal("learning capacity prematurely unavailable")
		}
	}
	start := time.Now()
	if a.enqueueLearning(assistantLearningJob{chatID: -1, messageID: 999}) {
		t.Fatal("full learning queue accepted extra job")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("low priority learning blocked foreground")
	}
}

func TestAssistantVerificationPoolLearningDoesNotOvertakeQueuedChat(t *testing.T) {
	a, cfg := assistantVerificationPool(t, func(http.ResponseWriter, *http.Request) {})
	cfg.TaskAssignments["chat"] = AssistantTaskAssignment{Primary: "main"}
	cfg.TaskAssignments["learning"] = AssistantTaskAssignment{Primary: "main"}
	held, e := a.acquire(context.Background(), -1, "chat", cfg, true, store.GroupAssistantPolicy{})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		task string
		l    *assistantLease
		e    error
	}
	out := make(chan result, 2)
	go func() {
		l, e := a.acquire(ctx, -1, "chat", cfg, true, store.GroupAssistantPolicy{})
		out <- result{"chat", l, e}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		a.queue.mu.Lock()
		n := a.queue.depth[-1]
		a.queue.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			held.finish(true, nil)
			t.Fatal("chat never queued")
		}
		time.Sleep(time.Millisecond)
	}
	held.finish(true, nil)
	go func() {
		l, e := a.acquire(ctx, -2, "learning", cfg, false, store.GroupAssistantPolicy{})
		out <- result{"learning", l, e}
	}()
	first := <-out
	if first.e != nil {
		t.Error(first.e)
	}
	if first.task != "chat" {
		t.Error("low-priority learning overtook already queued chat")
	}
	if first.l != nil {
		first.l.finish(true, nil)
		if first.l.queued {
			a.releaseQueued(-1)
		}
	}
	second := <-out
	if second.e != nil {
		t.Error(second.e)
	}
	if second.l != nil {
		second.l.finish(true, nil)
		if second.l.queued {
			if second.task == "chat" {
				a.releaseQueued(-1)
			} else {
				a.releaseQueued(-2)
			}
		}
	}
}

func TestAssistantVerificationRound3MentionEntities(t *testing.T) {
	a, _ := assistantVerificationPool(t, func(http.ResponseWriter, *http.Request) {})
	name := "@assistant_test_bot"
	p := store.GroupAssistantPolicy{}
	for _, tc := range []struct {
		name, text string
		entities   []tele.MessageEntity
		reply      *tele.Message
		want       bool
	}{
		{"utf16 emoji prefix", "😀 " + name + " 问题", []tele.MessageEntity{{Type: tele.EntityMention, Offset: 3, Length: len(name)}}, nil, true},
		{"exact entity", name, []tele.MessageEntity{{Type: tele.EntityMention, Offset: 0, Length: len(name)}}, nil, true},
		{"fake username entity", name + "_fake", []tele.MessageEntity{{Type: tele.EntityMention, Offset: 0, Length: len(name) + 5}}, nil, false},
		{"plain text in other entity", name, []tele.MessageEntity{{Type: tele.EntityBold, Offset: 0, Length: len(name)}}, nil, false},
		{"text mention correct bot", "助手", []tele.MessageEntity{{Type: tele.EntityTMention, Offset: 0, Length: 2, User: &tele.User{ID: 900}}}, nil, true},
		{"text mention foreign user", "助手", []tele.MessageEntity{{Type: tele.EntityTMention, Offset: 0, Length: 2, User: &tele.User{ID: 901}}}, nil, false},
		{"embedded username", "prefix" + name, nil, nil, false},
		{"exact fallback", name + "?", nil, nil, true},
		{"reply bot", "question", nil, &tele.Message{Sender: &tele.User{ID: 900}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &tele.Message{Chat: &tele.Chat{ID: -1}, Sender: &tele.User{ID: 8}, Text: tc.text, Entities: tc.entities, ReplyTo: tc.reply}
			if got := a.shouldTrigger(m, p); got != tc.want {
				t.Fatalf("trigger=%v want=%v", got, tc.want)
			}
		})
	}
	if got := stripAssistantMention("😀 "+name+" question", a.service.bot); strings.Contains(got, name) || !strings.Contains(got, "😀") {
		t.Fatal("strip corrupted UTF16-prefix text", got)
	}
}

func TestAssistantVerificationRound3CanceledChatReleasesPriority(t *testing.T) {
	a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"learned"}}]}`)
	})
	cfg.TaskAssignments["chat"] = AssistantTaskAssignment{Primary: "main"}
	cfg.TaskAssignments["learning"] = AssistantTaskAssignment{Primary: "main"}
	held, e := a.acquire(context.Background(), -1, "chat", cfg, true, store.GroupAssistantPolicy{})
	if e != nil {
		t.Fatal(e)
	}
	defer held.finish(true, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := a.acquire(ctx, -2, "chat", cfg, true, store.GroupAssistantPolicy{}); done <- e }()
	deadline := time.Now().Add(time.Second)
	for !a.hasQueuedChat() {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("chat not queued")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if e = <-done; !errors.Is(e, context.Canceled) {
		t.Fatalf("queue cancel %v", e)
	}
	a.queue.mu.Lock()
	waiters, depth := a.queue.chatWaiters, a.queue.depth[-2]
	a.queue.mu.Unlock()
	if waiters != 0 || depth != 0 {
		t.Fatalf("canceled waiter leaked priority=%d depth=%d", waiters, depth)
	}
	held.finish(true, nil)
	result, _, e := a.dispatchPlain(context.Background(), -3, "learning", cfg, store.GroupAssistantPolicy{}, ai.CheckRequest{})
	if e != nil || result == nil || result.Content != "learned" {
		t.Fatalf("learning starved after canceled chat: %+v %v", result, e)
	}
}

func TestAssistantVerificationRound3CurrentRequestOverflow(t *testing.T) {
	for _, status := range []int{429, 503} {
		for _, task := range []string{"chat", "learning"} {
			t.Run(fmt.Sprintf("%s/%d", task, status), func(t *testing.T) {
				var main, backup atomic.Int32
				a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["model"] == "main" {
						main.Add(1)
						w.Header().Set("Retry-After", "1")
						w.WriteHeader(status)
						return
					}
					backup.Add(1)
					fmt.Fprint(w, `{"choices":[{"message":{"content":"overflow answer"}}]}`)
				})
				var answer string
				var ep AssistantPoolEndpoint
				var e error
				if task == "chat" {
					answer, ep, e = a.dispatchTools(context.Background(), -1, cfg, store.GroupAssistantPolicy{}, "system", nil, nil)
				} else {
					var result *ai.ChatRawResult
					result, ep, e = a.dispatchPlain(context.Background(), -1, "learning", cfg, store.GroupAssistantPolicy{}, ai.CheckRequest{})
					if result != nil {
						answer = result.Content
					}
				}
				if e != nil || answer != "overflow answer" || ep.ID == "main" || main.Load() != 1 || backup.Load() != 1 {
					t.Errorf("CURRENT request did not overflow: primaryHTTP=%d backupHTTP=%d selected=%s answer=%q err=%v", main.Load(), backup.Load(), ep.ID, answer, e)
				}
			})
		}
	}
}

func TestAssistantVerificationRound3NoSwitchAfterToolStarted(t *testing.T) {
	var main, backup atomic.Int32
	a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "main" {
			backup.Add(1)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"wrong endpoint"}}]}`)
			return
		}
		if main.Add(1) == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"shell","arguments":"{}"}}]}}]}`)
			return
		}
		w.WriteHeader(503)
	})
	_, ep, e := a.dispatchTools(context.Background(), -1, cfg, store.GroupAssistantPolicy{}, "system", nil, nil)
	if e == nil || ep.ID != "main" || main.Load() != 2 || backup.Load() != 0 {
		t.Fatalf("tool round illegally switched: ep=%s primary=%d backup=%d err=%v", ep.ID, main.Load(), backup.Load(), e)
	}
}
