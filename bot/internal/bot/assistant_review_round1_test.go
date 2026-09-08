package bot

// Regression copy of independent acceptance probes (BI-001/002/003/005).
// Original assertions retained; only names and an isolated-DB opt-in guard differ.
import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type reviewRound1Sender struct {
	mu      sync.Mutex
	count   int
	replyTo int
}

func (s *reviewRound1Sender) Send(to tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	for _, o := range opts {
		if x, ok := o.(*tele.SendOptions); ok && x.ReplyTo != nil {
			s.replyTo = x.ReplyTo.ID
		}
	}
	return &tele.Message{ID: 9000 + s.count, Chat: &tele.Chat{ID: -1}}, nil
}
func (*reviewRound1Sender) Delete(tele.Editable) error { return nil }
func (s *reviewRound1Sender) snapshot() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count, s.replyTo
}
func reviewRound1PG(t *testing.T, h http.HandlerFunc, limit int) (*GroupAssistant, AssistantPoolConfig, store.GroupAssistantPolicy, *pgxpool.Pool, *reviewRound1Sender) {
	t.Helper()
	dsn := os.Getenv("CG_SOURCE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("dedicated local PG required")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Path != "/assistant_source_test" || u.User == nil || u.User.Username() != "assistant_source_test" {
		t.Fatal("refusing non-task DB")
	}
	db, e := pgxpool.New(context.Background(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(db.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	q := store.New(db)
	chat := -time.Now().UnixNano() / 1000
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := db.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO groups(chat_id,title,type,config)VALUES($1,'independent local','supergroup','{"keep":"moderation"}')`, chat)
	exec(`INSERT INTO authorized_groups(chat_id)VALUES($1)`, chat)
	exec(`INSERT INTO group_assistant_policies(chat_id,chat_enabled,learning_enabled,proactive_interject_enabled,history_limit)VALUES($1,true,false,false,500)`, chat)
	// Test-local DB, explicit settings: keep input merge finite; disable query embeddings except in the nesting probe.
	exec(`UPDATE group_assistant_global_settings SET memory_recall_enabled=false,inbound_merge_window_sec=0.4 WHERE id=1`)
	a, cfg := sourceRefactorPool(t, h)
	custom := false
	cfg.InheritGlobal = &custom
	cfg.Strategy = "primary-overflow"
	cfg.Endpoints = cfg.Endpoints[:1]
	cfg.Endpoints[0].MaxConcurrency = limit
	cfg.TaskAssignments = map[string]AssistantTaskAssignment{"chat": {Primary: "main"}, "vector": {Primary: "main"}}
	cfg.MaxQueueWaitSec = 1
	if e = a.ValidatePoolConfig(cfg, true); e != nil {
		t.Fatal(e)
	}
	if _, e = q.UpsertGroupAssistantPool(ctx, store.UpsertGroupAssistantPoolParams{ChatID: chat, Strategy: cfg.Strategy, Config: EncodeAssistantPool(cfg)}); e != nil {
		t.Fatal(e)
	}
	sender := &reviewRound1Sender{}
	a.ctx = ctx
	a.queries = q
	a.service.assistant = a
	a.service.queries = q
	a.service.logger = zap.NewNop()
	a.service.sender = sender
	a.service.lifecycleCtx = ctx
	p, e := a.Policy(ctx, chat)
	if e != nil {
		t.Fatal(e)
	}
	return a, cfg, p, db, sender
}
func TestReviewRound1MentionSupplementThroughApprovedHandler(t *testing.T) {
	inputs := make(chan string, 4)
	a, _, p, _, sender := reviewRound1PG(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Messages []ai.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		inputs <- fmt.Sprint(b.Messages[len(b.Messages)-1].Content)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"schema\":\"smart-group-bot.reply.v2\",\"messages\":[{\"text\":\"确认\",\"delivery_mode\":\"reply\",\"reply_to\":\"latest_input\"}]}"}}]}`)
	}, 2)
	first := &tele.Message{ID: 1, Chat: &tele.Chat{ID: p.ChatID}, ThreadID: 7, Sender: &tele.User{ID: 12}, Text: "@assistant_test_bot 独立第一段"}
	second := &tele.Message{ID: 2, Chat: first.Chat, ThreadID: 7, Sender: first.Sender, Text: "独立补充第二段"}
	if e := a.service.handleApprovedAssistantMessage(a.ctx, first, false, assistantEligibilityEligible, false, false); e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	if e := a.service.handleApprovedAssistantMessage(a.ctx, second, false, assistantEligibilityEligible, false, false); e != nil {
		t.Fatal(e)
	}
	var input string
	select {
	case input = <-inputs:
	case <-time.After(3 * time.Second):
		t.Fatal("no upstream request")
	}
	time.Sleep(200 * time.Millisecond)
	count, target := sender.snapshot()
	t.Logf("actual CURRENT input=%q, actual sends=%d reply_to=%d", input, count, target)
	if !strings.Contains(input, "独立第一段\n独立补充第二段") || count != 1 || target != 2 {
		t.Errorf("BI-001: approved @ + 100ms unmentioned supplement must form one direct batch and one reply to latest input; got current=%q sends=%d target=%d", input, count, target)
	}
}
func TestReviewRound1RealPromptBudget(t *testing.T) {
	inputs := make(chan int, 4)
	a, _, p, _, _ := reviewRound1PG(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Messages []ai.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		tokens := 0
		for _, m := range b.Messages {
			tokens += assistantEstimateTokens(fmt.Sprint(m.Content))
		}
		inputs <- tokens
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"schema\":\"smart-group-bot.reply.v2\",\"should_reply\":false}"}}]}`)
	}, 2)
	for i := 1; i <= 500; i++ {
		msg := &tele.Message{ID: i, Chat: &tele.Chat{ID: p.ChatID}, ThreadID: 7, Sender: &tele.User{ID: 12}, Text: strings.Repeat("历史中文内容", 25)}
		if e := a.storeIncoming(a.ctx, p, msg, msg.Text, false); e != nil {
			t.Fatal(e)
		}
	}
	msg := &tele.Message{ID: 501, Chat: &tele.Chat{ID: p.ChatID}, ThreadID: 7, Sender: &tele.User{ID: 12}, Text: "@assistant_test_bot 问好"}
	if e := a.storeIncoming(a.ctx, p, msg, msg.Text, false); e != nil {
		t.Fatal(e)
	}
	if e := a.service.processAssistantReplyNow(a.ctx, msg, msg.Text, p, true, assistantReplyDirect, false, 1); e != nil {
		t.Fatal(e)
	}
	n := <-inputs
	t.Logf("actual HTTP prompt estimate=%d tokens with history_limit=500; configured local budget=12000 including reserved output", n)
	if n > 12000 {
		t.Errorf("BI-002: reply target system block bypasses token budget: actual=%d > 12000", n)
	}
}
func TestReviewRound1RecallWithinSingleCapacityToolTurn(t *testing.T) {
	var mu sync.Mutex
	embeddings := 0
	toolResult := ""
	chatCalls := 0
	a, cfg, p, db, _ := reviewRound1PG(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Messages []ai.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		if r.URL.Path == "/embeddings" {
			mu.Lock()
			embeddings++
			mu.Unlock()
			fmt.Fprint(w, `{"data":[{"embedding":[1,0]}]}`)
			return
		}
		mu.Lock()
		chatCalls++
		n := chatCalls
		mu.Unlock()
		if n == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"recall","type":"function","function":{"name":"conversation_recall","arguments":"{\"query\":\"zebraquery\",\"before_after\":0}"}}]}}]}`)
			return
		}
		for _, m := range b.Messages {
			if m.Role == "tool" {
				mu.Lock()
				toolResult = fmt.Sprint(m.Content)
				mu.Unlock()
			}
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"完成"}}]}`)
	}, 1)
	msg := &tele.Message{ID: 1, Chat: &tele.Chat{ID: p.ChatID}, ThreadID: 7, Sender: &tele.User{ID: 12}, Text: "完全不同的中文原文"}
	if e := a.storeIncoming(a.ctx, p, msg, msg.Text, false); e != nil {
		t.Fatal(e)
	}
	rows, e := a.queries.ListGroupAssistantMessages(a.ctx, store.ListGroupAssistantMessagesParams{ChatID: p.ChatID, Limit: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = a.queries.PutAssistantMessageVector(a.ctx, rows[0], "mock:main", []float64{1, 0}); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(a.ctx, `UPDATE group_assistant_global_settings SET memory_recall_enabled=true WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	settings := a.loadRuntimeSettings(a.ctx)
	control, e := a.recallArchive(a.ctx, p.ChatID, int32Ptr(7), "zebraquery", nil, 0, 4, settings)
	if e != nil || len(control) != 1 || control[0].Text != msg.Text {
		t.Fatalf("outside-turn control failed: %v %v", control, e)
	}
	mu.Lock()
	t.Logf("control: same model/config/persistent semantic query outside tool turn succeeds with embedding HTTP=%d", embeddings)
	embeddings = 0
	mu.Unlock()
	rt := &assistantToolRuntime{msg: msg, mode: assistantReplyDirect, settings: settings}
	ctx, cancel := context.WithTimeout(withAssistantRuntime(a.ctx, rt), 4*time.Second)
	defer cancel()
	start := time.Now()
	_, _, e = a.dispatchTools(ctx, p.ChatID, cfg, p, "s", []ai.Message{{Role: "user", Content: "问"}}, assistantToolsFor(p, settings, false))
	mu.Lock()
	defer mu.Unlock()
	t.Logf("actual embedding HTTP=%d chat HTTP=%d tool_result=%s elapsed=%v err=%v", embeddings, chatCalls, toolResult, time.Since(start), e)
	if embeddings != 1 || !strings.Contains(toolResult, msg.Text) {
		t.Errorf("BI-003: capable inherited vector + valid persistent index must remain usable in a tool turn at allowed max_concurrency=1; lease prevents nested recall (embeddings=%d result=%s)", embeddings, toolResult)
	}
	round4AssertReleased(t, a)
}

func TestReviewRound1SharedCapacityAcrossRoleAliases(t *testing.T) {
	var mu sync.Mutex
	active := map[string]int{}
	peak := map[string]int{}
	calls := 0
	a, cfg := sourceRefactorPool(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		mu.Lock()
		active[b.Model]++
		calls++
		if active[b.Model] > peak[b.Model] {
			peak[b.Model] = active[b.Model]
		}
		mu.Unlock()
		defer func() { mu.Lock(); active[b.Model]--; mu.Unlock() }()
		time.Sleep(5 * time.Millisecond)
		if r.URL.Path == "/embeddings" {
			fmt.Fprint(w, `{"data":[{"embedding":[1,0]}]}`)
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"done"}}]}`)
		}
	})
	endpoints := cfg.Endpoints
	cfg.Endpoints = nil
	cfg.TaskAssignments = map[string]AssistantTaskAssignment{}
	tasks := []string{"chat", "learning", "decision", "vision", "compress", "vector"}
	for _, task := range tasks {
		ids := []string{}
		for _, ep := range endpoints {
			ep.ID = task + "-" + ep.ID
			ids = append(ids, ep.ID)
			cfg.Endpoints = append(cfg.Endpoints, ep)
		}
		cfg.TaskAssignments[task] = AssistantTaskAssignment{Primary: ids[0], Backups: ids[1:]}
	}
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			task := tasks[i%len(tasks)]
			chat := int64(-1 - i%4)
			var e error
			switch task {
			case "chat":
				_, _, e = a.dispatchTools(ctx, chat, cfg, store.GroupAssistantPolicy{}, "s", nil, nil)
			case "vector":
				_, _, e = a.dispatchEmbedding(ctx, chat, cfg, store.GroupAssistantPolicy{}, "query")
			default:
				_, _, e = a.dispatchPlain(ctx, chat, task, cfg, store.GroupAssistantPolicy{}, ai.CheckRequest{})
			}
			if e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	round4AssertReleased(t, a)
	if calls != 60 {
		t.Fatal("missing actual HTTP", calls)
	}
	for model, p := range peak {
		if p > 2 {
			t.Error("split upstream capacity", model, p)
		}
	}
	t.Logf("actual 60 concurrent HTTP executions across 6 task-specific endpoint aliases and 4 groups: peak=%v; all leases/queues released", peak)
}

func TestReviewRound1UnsupportedCapabilityChineseError(t *testing.T) {
	a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) { t.Error("incompatible candidate must not contact HTTP") })
	cfg.TaskAssignments["chat"] = AssistantTaskAssignment{Primary: "text"}
	e := a.ValidatePoolConfig(cfg, true)
	if e == nil {
		t.Fatal("missing capability not rejected")
	}
	t.Logf("model-pool API returns validation error verbatim: %s", e)
	chinese := false
	for _, r := range e.Error() {
		if r >= '一' && r <= '鿿' {
			chinese = true
		}
	}
	if !chinese {
		t.Errorf("BI-005: unsupported tools model must have clear Chinese explanation, actual=%q", e.Error())
	}
}
