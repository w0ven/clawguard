package bot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/alicebob/miniredis/v2"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/redis/go-redis/v9"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/pressly/goose/v3"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

// Uses the real orchestration + pgx store + local provider HTTP and Telegram sender stub.
// It does NOT replace upstream moderation eligibility decisions with claimed verification.
func TestAssistantVerificationFlowPostgres(t *testing.T) {
	dsn := os.Getenv("ASSISTANT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BLOCKED: dedicated assistant-test PostgreSQL required")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Hostname() != "127.0.0.1" || u.Path != "/assistant_test" || u.User.Username() != "assistant_test" {
		t.Fatal("refusing non-task database")
	}
	ctx := context.Background()
	sqlDB, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer sqlDB.Close()
	if e = goose.UpContext(ctx, sqlDB, "../../migrations"); e != nil {
		t.Fatal(e)
	}
	db, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	q := store.New(db)
	makeService := func(t *testing.T, chat int64, chatOn, learnOn bool) (*Service, *fakeTelegramSender, *atomic.Int32, store.GroupAssistantPolicy) {
		t.Helper()
		calls := &atomic.Int32{}
		a, cfg := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"test answer"}}]}`)
		})
		sender := &fakeTelegramSender{}
		svc := &Service{bot: a.service.bot, queries: q, sender: sender, logger: zap.NewNop(), assistant: a,
			verifyBtn: tele.Btn{Unique: "verify_human"}, verifyMathBtn: tele.Btn{Unique: "verify_math"}, verifyRandBtn: tele.Btn{Unique: "verify_random"}, verifyAdminBtn: tele.Btn{Unique: "verify_admin"}}
		a.service = svc
		a.queries = q
		p, err := q.UpsertGroupAssistantPolicy(ctx, store.UpsertGroupAssistantPolicyParams{ChatID: chat, ChatEnabled: chatOn, LearningEnabled: learnOn, TriggerMode: "mention_or_reply", FollowupWindowSec: 30, MaxFollowupTurns: 5, ChatModelRef: "mock:main", LearningModelRef: "mock:main", HistoryLimit: 30, RetentionDays: 7, CollectionPolicy: "history_7d_and_long_term_summary", ToolAllowlist: []string{"knowledge_query", "conversation_recall", "webfetch_readonly"}, AllowDomains: []string{}, MaxQueueDepth: 1, MaxQueueWaitSec: 1})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(cfg)
		if _, err = q.UpsertGroupAssistantPool(ctx, store.UpsertGroupAssistantPoolParams{ChatID: chat, Strategy: "primary-overflow", Config: raw}); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, `INSERT INTO authorized_groups(chat_id) VALUES($1)`, chat); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, `INSERT INTO groups(chat_id,title,type,config) VALUES($1,'assistant-test','supergroup','{"ai":{"enabled":false}}')`, chat); err != nil {
			t.Fatal(err)
		}
		return svc, sender, calls, p
	}
	message := func(chat int64) *tele.Message {
		return &tele.Message{ID: 10, Chat: &tele.Chat{ID: chat}, Sender: &tele.User{ID: 8, FirstName: "test"}, Text: "@assistant_test_bot question", Unixtime: time.Now().Unix()}
	}
	count := func(t *testing.T, chat int64, role string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM group_assistant_messages WHERE chat_id=$1 AND role=$2", chat, role).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Run("DisabledNoWritesModelOrSend", func(t *testing.T) {
		s, sender, calls, _ := makeService(t, -2001, false, false)
		if e := s.handleApprovedAssistantMessage(ctx, message(-2001), false, assistantEligibilityEligible, false, false); e != nil {
			t.Fatal(e)
		}
		if count(t, -2001, "user") != 0 || count(t, -2001, "assistant") != 0 || calls.Load() != 0 || atomic.LoadInt32(&sender.calls) != 0 || len(s.assistant.learning) != 0 {
			t.Fatal("disabled side effects")
		}
	})
	for i, state := range []assistantEligibility{assistantEligibilityUnknown, assistantEligibilityBlocked} {
		for _, edited := range []bool{false, true} {
			chat := int64(-2010 - i*2)
			if edited {
				chat--
			}
			t.Run(fmt.Sprintf("Eligibility%dEdited%v", state, edited), func(t *testing.T) {
				s, sender, calls, _ := makeService(t, chat, true, true)
				if e := s.handleApprovedAssistantMessage(ctx, message(chat), edited, state, false, false); e != nil {
					t.Fatal(e)
				}
				if count(t, chat, "user") != 0 || count(t, chat, "assistant") != 0 || calls.Load() != 0 || atomic.LoadInt32(&sender.calls) != 0 || len(s.assistant.learning) != 0 {
					t.Errorf("blocked/unresolved input stored/learned/answered; userRows=%d model=%d send=%d", count(t, chat, "user"), calls.Load(), atomic.LoadInt32(&sender.calls))
				}
			})
		}
	}
	t.Run("KeywordRepliedNeverDoubleAnswer", func(t *testing.T) {
		s, sender, calls, _ := makeService(t, -2020, true, true)
		if e := s.handleApprovedAssistantMessage(ctx, message(-2020), false, assistantEligibilityEligible, true, false); e != nil {
			t.Fatal(e)
		}
		if calls.Load() != 0 || atomic.LoadInt32(&sender.calls) != 0 {
			t.Fatal("keyword double answer")
		}
		if len(s.assistant.learning) != 1 {
			t.Fatal("eligible keyword source should remain learnable")
		}
	})
	t.Run("SuccessfulSendThenHistoryAndLearningDisabled", func(t *testing.T) {
		s, sender, calls, _ := makeService(t, -2021, true, false)
		if e := s.handleApprovedAssistantMessage(ctx, message(-2021), false, assistantEligibilityEligible, false, false); e != nil {
			t.Fatal(e)
		}
		if calls.Load() != 1 || atomic.LoadInt32(&sender.calls) != 1 || count(t, -2021, "assistant") != 1 || len(s.assistant.learning) != 0 {
			t.Fatalf("delivery counts model=%d send=%d assistant=%d learning=%d", calls.Load(), atomic.LoadInt32(&sender.calls), count(t, -2021, "assistant"), len(s.assistant.learning))
		}
	})
	t.Run("FailedSendNeverAssistantHistory", func(t *testing.T) {
		s, sender, calls, _ := makeService(t, -2022, true, false)
		sender.errs = []error{errors.New("test send failure")}
		if e := s.handleApprovedAssistantMessage(ctx, message(-2022), false, assistantEligibilityEligible, false, false); e != nil {
			t.Fatal(e)
		}
		if calls.Load() != 1 || atomic.LoadInt32(&sender.calls) != 1 || count(t, -2022, "assistant") != 0 {
			t.Fatal("failed delivery was recorded")
		}
	})
	t.Run("DuplicateUpdateNoSecondChatOrLearning", func(t *testing.T) {
		s, sender, calls, _ := makeService(t, -2023, true, true)
		// Duplicate Telegram updates are owned by ProcessUpdate/Redis, not the post-moderation hook.
		tb, _ := assistantVerificationTelegram(t, -2023)
		s.bot = tb
		s.registerHandlers()
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()
		s.redis = rdb
		m := message(-2023)
		m.Chat.Type = tele.ChatSuperGroup
		for i := 0; i < 2; i++ {
			if e := s.ProcessUpdate(tele.Update{ID: 123023, Message: m}); e != nil {
				t.Fatal(e)
			}
		}
		if count(t, -2023, "user") != 1 || calls.Load() != 1 || atomic.LoadInt32(&sender.calls) != 1 || len(s.assistant.learning) != 1 {
			t.Errorf("duplicate update: user=%d model=%d send=%d learning=%d", count(t, -2023, "user"), calls.Load(), atomic.LoadInt32(&sender.calls), len(s.assistant.learning))
		}
	})
	t.Run("EditUpdatesOnlyDoesNotExtendRetention", func(t *testing.T) {
		s, sender, calls, _ := makeService(t, -2024, true, true)
		m := message(-2024)
		if e := s.handleApprovedAssistantMessage(ctx, m, false, assistantEligibilityEligible, false, false); e != nil {
			t.Fatal(e)
		}
		pastExpiry := time.Now().Add(time.Hour)
		if _, e = db.Exec(ctx, "UPDATE group_assistant_messages SET expires_at=$1 WHERE chat_id=$2 AND role='user'", pastExpiry, m.Chat.ID); e != nil {
			t.Fatal(e)
		}
		m.Text = "edited source"
		if e := s.handleApprovedAssistantMessage(ctx, m, true, assistantEligibilityEligible, false, false); e != nil {
			t.Fatal(e)
		}
		if calls.Load() != 1 || atomic.LoadInt32(&sender.calls) != 1 || len(s.assistant.learning) != 1 {
			t.Error("edit started new model/send/learning")
		}
		var got time.Time
		if e = db.QueryRow(ctx, "SELECT expires_at FROM group_assistant_messages WHERE chat_id=$1 AND role='user'", m.Chat.ID).Scan(&got); e != nil {
			t.Fatal(e)
		}
		if got.After(pastExpiry.Add(time.Millisecond)) {
			t.Errorf("edit extended retention: %s -> %s", pastExpiry, got)
		}
	})
	t.Run("EligibleEditCannotInsertNewSource", func(t *testing.T) {
		s, sender, calls, _ := makeService(t, -2050, true, true)
		if e := s.handleApprovedAssistantMessage(ctx, message(-2050), true, assistantEligibilityEligible, false, false); e != nil {
			t.Fatal(e)
		}
		if count(t, -2050, "user") != 0 || calls.Load() != 0 || atomic.LoadInt32(&sender.calls) != 0 || len(s.assistant.learning) != 0 {
			t.Fatal("new source inserted by edit")
		}
	})
	t.Run("BlockedEditRevokesExistingSourceAndKnowledgeTool", func(t *testing.T) {
		s, sender, calls, _ := makeService(t, -2051, true, true)
		chat, msg := int64(-2051), int64(10)
		exp := time.Now().Add(time.Hour)
		digest := sha256.Sum256([]byte("original"))
		sourceHash := hex.EncodeToString(digest[:])
		if _, e := db.Exec(ctx, `INSERT INTO group_assistant_messages(chat_id,telegram_message_id,role,text,content_hash,expires_at) VALUES($1,$2,'user','original',$3,$4)`, chat, msg, sourceHash, exp); e != nil {
			t.Fatal(e)
		}
		_, e := q.CreateGroupAssistantMemory(ctx, store.CreateGroupAssistantMemoryParams{ChatID: chat, Subject: "old", Content: "old fact", MemoryType: "learned", AuthorityLevel: "learned_fact", ValidScope: "today", SourceType: "telegram_approved_message", SourceVerified: "verified", SourceMessageID: &msg, SourceChatID: &chat, SourceContentHash: sourceHash, ExpiresAt: exp, DedupeHash: "old"})
		if e != nil {
			t.Fatal(e)
		}
		for _, name := range []string{"knowledge_query", "conversation_recall"} {
			call := ai.ToolCall{}
			call.Function.Name = name
			call.Function.Arguments = `{"query":"old"}`
			if name == "conversation_recall" {
				call.Function.Arguments = `{"query":"original"}`
			}
			out, e := s.assistant.executeReadOnlyTool(ctx, chat, store.GroupAssistantPolicy{}, call)
			if e != nil || out == "[]" {
				t.Fatalf("positive recall control failed %s %s %v", name, out, e)
			}
		}
		m := message(chat)
		m.Text = "blocked replacement"
		if e = s.handleApprovedAssistantMessage(ctx, m, true, assistantEligibilityBlocked, false, false); e != nil {
			t.Fatal(e)
		}
		for _, name := range []string{"knowledge_query", "conversation_recall"} {
			call := ai.ToolCall{}
			call.Function.Name = name
			call.Function.Arguments = `{"query":"old"}`
			if name == "conversation_recall" {
				call.Function.Arguments = `{"query":"original"}`
			}
			out, e := s.assistant.executeReadOnlyTool(ctx, chat, store.GroupAssistantPolicy{}, call)
			if e != nil || out != "[]" {
				t.Fatalf("revoked source tool leak %s %s %v", name, out, e)
			}
		}
		if calls.Load() != 0 || atomic.LoadInt32(&sender.calls) != 0 || len(s.assistant.learning) != 0 {
			t.Fatal("blocked edit downstream action")
		}
	})
	t.Run("InboundModerationToDelivery", func(t *testing.T) {
		s, sender, calls, _ := makeService(t, -2052, true, false)
		tb, _ := assistantVerificationTelegram(t, -2052)
		s.bot = tb
		m := message(-2052)
		m.Chat.Type = tele.ChatSuperGroup
		c := tb.NewContext(tele.Update{ID: 99001, Message: m})
		if e := s.handleIncomingMessage(c); e != nil {
			t.Fatal(e)
		}
		if calls.Load() != 1 || atomic.LoadInt32(&sender.calls) != 1 || count(t, -2052, "assistant") != 1 {
			t.Fatalf("inbound not delivered model=%d send=%d assistant=%d", calls.Load(), atomic.LoadInt32(&sender.calls), count(t, -2052, "assistant"))
		}
	})
	t.Run("OtherBotWithAIOffBlocked", func(t *testing.T) {
		s, sender, calls, _ := makeService(t, -2053, true, true)
		tb, _ := assistantVerificationTelegram(t, -2053)
		s.bot = tb
		m := message(-2053)
		m.Chat.Type = tele.ChatSuperGroup
		m.Sender.IsBot = true
		if e := s.handleIncomingMessage(tb.NewContext(tele.Update{ID: 99002, Message: m})); e != nil {
			t.Fatal(e)
		}
		if count(t, -2053, "user") != 0 || calls.Load() != 0 || atomic.LoadInt32(&sender.calls) != 0 {
			t.Fatal("other bot was learned/answered")
		}
	})
	t.Run("SharedCapStatusReadOnlyAndRecovery", func(t *testing.T) {
		s, _, _, p := makeService(t, -2060, true, false)
		_, _, _, _ = makeService(t, -2061, true, false)
		a := s.assistant
		r := a.models.(*assistantVerificationRegistry)
		r.models[ai.NewModelRef("mock", "cap")] = ai.Model{Enabled: true, SupportsTools: true, ProviderKey: "mock", ModelKey: "cap"}
		cfg := AssistantPoolConfig{Strategy: "primary-overflow", TaskAssignments: map[string]AssistantTaskAssignment{"chat": {Primary: "cap"}, "learning": {Primary: "cap"}}, Endpoints: []AssistantPoolEndpoint{{ID: "cap", ModelRef: "mock:cap", Role: "primary", MaxConcurrency: 3, TimeoutMs: 1000, CooldownSeconds: 1}}, MaxQueueWaitSec: 1, MaxQueueDepth: 1}
		raw, _ := json.Marshal(cfg)
		if _, e := q.UpsertGroupAssistantPool(ctx, store.UpsertGroupAssistantPoolParams{ChatID: -2060, ExpectedVersion: 1, Strategy: "primary-overflow", Config: raw}); e != nil {
			t.Fatal(e)
		}
		low := cfg
		low.Endpoints = append([]AssistantPoolEndpoint(nil), cfg.Endpoints...)
		low.Endpoints[0].MaxConcurrency = 1
		raw, _ = json.Marshal(low)
		if _, e := q.UpsertGroupAssistantPool(ctx, store.UpsertGroupAssistantPoolParams{ChatID: -2061, ExpectedVersion: 1, Strategy: "primary-overflow", Config: raw}); e != nil {
			t.Fatal(e)
		}
		before := len(a.runtimes)
		status, e := a.Status(ctx, -2061)
		if e != nil || len(a.runtimes) != before || status.Endpoints[0].MaxConcurrency != 1 {
			t.Fatalf("status mutated runtime %+v %v", status, e)
		}
		lease, e := a.acquire(ctx, -2060, "chat", cfg, true, p)
		if e != nil {
			t.Fatal(e)
		}
		if lease.runtime.limit != 1 {
			t.Fatal("shared cap ignored")
		}
		lease.finish(true, nil)
		if _, e = db.Exec(ctx, "UPDATE group_assistant_policies SET chat_enabled=false,learning_enabled=false WHERE chat_id=-2061"); e != nil {
			t.Fatal(e)
		}
		held := []*assistantLease{}
		defer func() {
			for _, l := range held {
				l.finish(true, nil)
			}
		}()
		for i := 0; i < 3; i++ {
			l, e := a.acquire(ctx, -2060, "chat", cfg, true, p)
			if e != nil {
				t.Fatalf("cap failed to recover slot %d: %v", i, e)
			}
			held = append(held, l)
		}
		status, e = a.Status(ctx, -2060)
		if e != nil || status.Endpoints[0].MaxConcurrency != 3 || status.Endpoints[0].CurrentActive != 3 {
			t.Fatalf("shared recovered status %+v %v", status, e)
		}
	})
	for i, mode := range []string{"ordinary", "stop_before_job", "stop_during_model", "invalid_quote", "this_week"} {
		t.Run("ExtractionHTTPStoreTool/"+mode, func(t *testing.T) {
			chat := int64(-2070 - i)
			s, _, _, p := makeService(t, chat, false, true)
			text := "本周营业时间 closes 8pm"
			hash := sha256.Sum256([]byte(text))
			if _, e := db.Exec(ctx, `INSERT INTO group_assistant_messages(chat_id,telegram_message_id,role,text,content_hash,expires_at) VALUES($1,10,'user',$2,$3,NOW()+INTERVAL '7 days')`, chat, text, hex.EncodeToString(hash[:])); e != nil {
				t.Fatal(e)
			}
			calls := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req struct {
					Messages []ai.Message `json:"messages"`
				}
				if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
					t.Error(e)
				}
				if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" || !strings.Contains(fmt.Sprint(req.Messages[1].Content), text) {
					t.Errorf("learning request lost source %+v", req)
				}
				quote := "closes 8pm"
				if mode == "invalid_quote" {
					quote = "absent text"
				}
				facts := fmt.Sprintf(`{"facts":[{"subject":"hours","content":"closes 8pm","valid_scope":"this_week","expires_at":%q,"source_quote":%q}]}`, time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339), quote)
				if mode == "stop_during_model" {
					if _, e := db.Exec(ctx, "UPDATE group_assistant_policies SET learning_enabled=false WHERE chat_id=$1", chat); e != nil {
						t.Error(e)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": facts}}}})
			}))
			defer provider.Close()
			s.assistant.providers.(assistantVerificationProviders).r.clients["mock"] = ai.NewOpenAICompatibleClient(provider.URL, "fake", time.Second, nil)
			if mode == "stop_before_job" {
				if _, e := db.Exec(ctx, "UPDATE group_assistant_policies SET learning_enabled=false WHERE chat_id=$1", chat); e != nil {
					t.Fatal(e)
				}
			}
			job := assistantLearningJob{policy: p, chatID: chat, messageID: 10, senderID: 8, senderName: "ordinary admin chat", text: text, authority: "learned_fact", sourceType: "telegram_approved_message", sourceChat: chat, operatorID: 8}
			s.assistant.extractAndStoreFact(job)
			memories, e := q.ListGroupAssistantMemories(ctx, chat, false, "hours", 10)
			if e != nil {
				t.Fatal(e)
			}
			shouldWrite := mode == "ordinary" || mode == "this_week"
			if shouldWrite {
				if len(memories) != 1 || memories[0].AuthorityLevel != "learned_fact" || memories[0].ValidScope != "this_week" || memories[0].ExpiresAt.After(assistantWeekEnd(time.Now())) {
					t.Fatalf("invalid extraction %+v", memories)
				}
				call := ai.ToolCall{}
				call.Function.Name = "knowledge_query"
				call.Function.Arguments = `{"query":"hours"}`
				out, e := s.assistant.executeReadOnlyTool(ctx, chat, p, call)
				if e != nil || !strings.Contains(out, "closes 8pm") {
					t.Fatalf("fact not recalled %s %v", out, e)
				}
			} else if len(memories) != 0 {
				t.Fatalf("disallowed extraction wrote %+v", memories)
			}
			if mode == "stop_before_job" && calls != 0 {
				t.Fatal("stopped queued job called provider")
			}
			if mode != "stop_before_job" && calls != 1 {
				t.Fatalf("no real extraction HTTP: %d", calls)
			}
		})
	}
	for i, mode := range []string{"allowed", "unauthorized", "frozen", "learning_off", "unpin_before_store", "linked", "linked_changed", "not_admin", "unauthorized_during_model", "frozen_during_model"} {
		t.Run("PinnedInboundRevalidation/"+mode, func(t *testing.T) {
			chat := int64(-2090 - i)
			s, _, _, _ := makeService(t, chat, false, true)
			tb, state := assistantVerificationTelegram(t, chat)
			s.bot = tb
			sourceChat := chat
			typ := tele.ChatSuperGroup
			if mode == "linked" || mode == "linked_changed" {
				sourceChat = chat - 100
				typ = tele.ChatChannel
				state.linked.Store(chat)
			}
			state.pin.Store(51)
			if mode == "not_admin" {
				state.admin.Store(false)
			}
			if mode == "unauthorized" {
				if _, e := db.Exec(ctx, "UPDATE authorized_groups SET enabled=false WHERE chat_id=$1", chat); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "frozen" {
				s.cacheSystemState(ctx, store.SystemState{Frozen: true})
			}
			if mode == "learning_off" {
				if _, e := db.Exec(ctx, "UPDATE group_assistant_policies SET learning_enabled=false WHERE chat_id=$1", chat); e != nil {
					t.Fatal(e)
				}
			}
			envelope := &tele.Message{ID: 51, Chat: &tele.Chat{ID: sourceChat, Type: typ}, Text: "hours closes 8pm", Sender: &tele.User{ID: 8, FirstName: "test"}}
			pin := &tele.Message{ID: 52, Chat: envelope.Chat, Sender: envelope.Sender, PinnedMessage: envelope}
			update := tele.Update{ID: 99100 + i, Message: pin}
			if typ == tele.ChatChannel {
				update.Message = nil
				update.ChannelPost = pin
			}
			if e := s.assistant.handlePinned(tb.NewContext(update)); e != nil {
				t.Fatal(e)
			}
			denied := mode == "unauthorized" || mode == "frozen" || mode == "learning_off" || mode == "not_admin"
			if denied {
				if len(s.assistant.learning) != 0 {
					t.Fatal("pin bypassed gate")
				}
				return
			}
			if len(s.assistant.learning) != 1 {
				t.Fatalf("eligible pin queue depth=%d", len(s.assistant.learning))
			}
			job := <-s.assistant.learning
			var requests atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if mode == "unpin_before_store" {
					state.pin.Store(0)
				}
				if mode == "linked_changed" {
					state.linked.Store(-999999)
				}
				if mode == "unauthorized_during_model" {
					if _, e := db.Exec(ctx, "UPDATE authorized_groups SET enabled=false WHERE chat_id=$1", chat); e != nil {
						t.Error(e)
					}
					s.InvalidateAuthorizedGroupCache(ctx, chat)
				}
				if mode == "frozen_during_model" {
					s.cacheSystemState(ctx, store.SystemState{Frozen: true})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"facts":[{"subject":"hours","content":"closes 8pm","valid_scope":"this_week","source_quote":"closes 8pm"}]}`}}}})
			}))
			defer provider.Close()
			s.assistant.providers.(assistantVerificationProviders).r.clients["mock"] = ai.NewOpenAICompatibleClient(provider.URL, "fake", time.Second, nil)
			s.assistant.extractAndStoreFact(job)
			mem, e := q.ListGroupAssistantMemories(ctx, chat, false, "hours", 10)
			if e != nil {
				t.Fatal(e)
			}
			if requests.Load() != 1 {
				t.Fatal("pin extraction did not use HTTP")
			}
			if mode == "unpin_before_store" || mode == "linked_changed" || mode == "unauthorized_during_model" || mode == "frozen_during_model" {
				if len(mem) != 0 {
					t.Fatalf("invalid pin source wrote %+v", mem)
				}
				return
			}
			if len(mem) != 1 || mem[0].AuthorityLevel != "pinned_announcement" {
				t.Fatalf("pin fact missing %+v", mem)
			}
			call := ai.ToolCall{}
			call.Function.Name = "knowledge_query"
			call.Function.Arguments = `{"query":"hours"}`
			before, e := s.assistant.executeReadOnlyTool(ctx, chat, store.GroupAssistantPolicy{}, call)
			if e != nil || !strings.Contains(before, "closes 8pm") {
				t.Fatalf("valid pin not recalled %s %v", before, e)
			}
			state.pin.Store(0)
			out, e := s.assistant.executeReadOnlyTool(ctx, chat, store.GroupAssistantPolicy{}, call)
			if e != nil || out != "[]" {
				t.Fatalf("unpin recalled %s %v", out, e)
			}
		})
	}
	t.Run("OrdinaryAdminChatNotExplicitAuthority", func(t *testing.T) {
		for _, text := range []string{"大家请记住今晚活动", "我昨天更正了这句话", "有人说以此为准"} {
			if looksLikeExplicitAssistantCorrection(text) {
				t.Errorf("ordinary admin chatter promoted: %s", text)
			}
		}
		if !looksLikeExplicitAssistantCorrection("更正：本周活动改到周五") {
			t.Error("explicit correction not recognized")
		}
	})

	for i, mode := range []string{"normal_with_learning", "keyword", "send_failure"} {
		t.Run("Round3FullUpdateHTTPDeliveryLearning/"+mode, func(t *testing.T) {
			chat := int64(-2200 - i)
			s, _, _, p := makeService(t, chat, true, true)
			tb, tg := assistantVerificationTelegram(t, chat)
			tg.admin.Store(false)
			s.bot = tb
			s.sender = tb
			s.registerHandlers()
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer rdb.Close()
			s.redis = rdb
			if mode == "send_failure" {
				tg.failSend.Store(true)
			}
			if mode == "keyword" {
				if _, e := db.Exec(ctx, `UPDATE groups SET config='{"ai":{"enabled":false},"messages":{"keyword_replies":[{"id":"kw","enabled":true,"match_type":"fuzzy","keywords":["营业时间"],"reply_text":"keyword answer","skip_admins":false}]}}' WHERE chat_id=$1`, chat); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := q.CreateGroupAssistantMemory(ctx, store.CreateGroupAssistantMemoryParams{ChatID: chat, Subject: "rules", Content: "be kind", MemoryType: "base", AuthorityLevel: "admin_base", ValidScope: "current_group", SourceType: "admin_base", SourceVerified: "verified", ExpiresAt: time.Now().Add(time.Hour), DedupeHash: "rule"}); e != nil {
				t.Fatal(e)
			}
			var chats, learns atomic.Int32
			var firstModel atomic.Value
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Model     string       `json:"model"`
					MaxTokens int          `json:"max_tokens"`
					Messages  []ai.Message `json:"messages"`
				}
				if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
					t.Error(e)
				}
				if req.MaxTokens == assistantMaxLearningTokens {
					learns.Add(1)
					if len(req.Messages) != 2 || req.Messages[0].Role != "system" || !strings.Contains(fmt.Sprint(req.Messages[1].Content), "closes 8pm") {
						t.Error("extraction HTTP source missing")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"facts":[{"subject":"hours","content":"closes 8pm","valid_scope":"this_week","source_quote":"closes 8pm"}]}`}}}})
					return
				}
				n := chats.Add(1)
				if n == 1 {
					firstModel.Store(req.Model)
					fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"read-rule","type":"function","function":{"name":"knowledge_query","arguments":"{\"query\":\"rules\"}"}}]}}]}`)
					return
				}
				if req.Model != firstModel.Load() {
					t.Error("same tool turn changed endpoint")
				}
				found := false
				for _, m := range req.Messages {
					if m.Role == "tool" && m.ToolCallID == "read-rule" && strings.Contains(fmt.Sprint(m.Content), "be kind") {
						found = true
					}
				}
				if !found {
					t.Error("real PG knowledge tool result not carried back to provider")
				}
				fmt.Fprint(w, `{"choices":[{"message":{"content":"assistant answer"}}]}`)
			}))
			defer provider.Close()
			s.assistant.providers.(assistantVerificationProviders).r.clients["mock"] = ai.NewOpenAICompatibleClient(provider.URL, "fake", time.Second, nil)
			life, cancel := context.WithCancel(context.Background())
			s.lifecycleCtx = life
			s.lifecycleCancel = cancel
			s.assistant.ctx = life
			s.wg.Add(1)
			go s.assistant.learningWorker()
			defer s.Stop()
			m := message(chat)
			m.Chat.Type = tele.ChatSuperGroup
			m.Text = "😀 @assistant_test_bot 营业时间 closes 8pm"
			m.Entities = []tele.MessageEntity{{Type: tele.EntityMention, Offset: 3, Length: len("@assistant_test_bot")}}
			update := tele.Update{ID: 124000 + i, Message: m}
			if e := s.ProcessUpdate(update); e != nil {
				t.Fatal(e)
			}
			deadline := time.Now().Add(3 * time.Second)
			var mem []store.GroupAssistantMemory
			for {
				var e error
				mem, e = q.ListGroupAssistantMemories(ctx, chat, false, "hours", 10)
				if e != nil {
					t.Fatal(e)
				}
				if len(mem) == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("worker did not finish memory: chats=%d learns=%d", chats.Load(), learns.Load())
				}
				time.Sleep(time.Millisecond)
			}
			h := sha256.Sum256([]byte(m.Text))
			if mem[0].SourceContentHash != hex.EncodeToString(h[:]) || mem[0].AuthorityLevel != "learned_fact" {
				t.Fatal("source SHA256/ordinary authority not retained", mem[0])
			}
			expectedChats := int32(2)
			expectedHistory := 1
			expectedText := "assistant answer"
			if mode == "keyword" {
				expectedChats = 0
				expectedHistory = 0
				expectedText = "keyword answer"
			}
			if mode == "send_failure" {
				expectedHistory = 0
			}
			flushDeadline := time.Now().Add(3 * time.Second)
			for {
				if chats.Load() == expectedChats && learns.Load() == 1 && tg.sends.Load() == 1 && count(t, chat, "user") == 1 && count(t, chat, "assistant") == expectedHistory {
					break
				}
				if time.Now().After(flushDeadline) {
					t.Fatalf("full pipeline chats=%d learns=%d sends=%d history(user=%d assistant=%d)", chats.Load(), learns.Load(), tg.sends.Load(), count(t, chat, "user"), count(t, chat, "assistant"))
				}
				time.Sleep(time.Millisecond)
			}
			if tg.lastSent.Load() != expectedText {
				t.Fatalf("Telegram received wrong reply: %v", tg.lastSent.Load())
			}
			if e := s.ProcessUpdate(update); e != nil {
				t.Fatal(e)
			}
			time.Sleep(20 * time.Millisecond)
			if chats.Load() != expectedChats || learns.Load() != 1 || tg.sends.Load() != 1 {
				t.Fatal("duplicate UPDATE repeated model, learning or Telegram send")
			}
			call := ai.ToolCall{}
			call.Function.Name = "knowledge_query"
			call.Function.Arguments = `{"query":"hours"}`
			out, e := s.assistant.executeReadOnlyTool(ctx, chat, p, call)
			if e != nil || !strings.Contains(out, "closes 8pm") {
				t.Fatalf("learned fact not recallable %s %v", out, e)
			}
			if mode == "normal_with_learning" {
				var expiry time.Time
				if e := db.QueryRow(ctx, `SELECT expires_at FROM group_assistant_messages WHERE chat_id=$1 AND role='user'`, chat).Scan(&expiry); e != nil {
					t.Fatal(e)
				}
				m.Text = "edited same message"
				m.Entities = nil
				if e := s.ProcessUpdate(tele.Update{ID: update.ID + 100, EditedMessage: m}); e != nil {
					t.Fatal(e)
				}
				var after time.Time
				if e := db.QueryRow(ctx, `SELECT expires_at FROM group_assistant_messages WHERE chat_id=$1 AND role='user'`, chat).Scan(&after); e != nil {
					t.Fatal(e)
				}
				if !after.Equal(expiry) || chats.Load() != expectedChats || learns.Load() != 1 || tg.sends.Load() != 1 {
					t.Fatal("edit renewed retention or retriggered actions")
				}
			}
			t.Logf("terminal assertions reached: chatHTTP=%d learningHTTP=%d TelegramSend=%d assistantHistory=%d", chats.Load(), learns.Load(), tg.sends.Load(), count(t, chat, "assistant"))
		})
	}

	for i, mode := range []string{"fallback_success", "all_failed", "empty_content", "whitespace_content"} {
		t.Run("Round4LearningFallbackPersistenceDispatch/"+mode, func(t *testing.T) {
			chat := int64(-2300 - i)
			s, _, _, p := makeService(t, chat, false, true)
			text := "hours closes 8pm"
			hash := sha256.Sum256([]byte(text))
			sourceHash := hex.EncodeToString(hash[:])
			if _, e := q.UpsertGroupAssistantMessage(ctx, store.UpsertGroupAssistantMessageParams{ChatID: chat, TelegramMessageID: 80, Role: "user", Text: text, Approved: true, Delivered: true, ContentHash: sourceHash, ExpiresAt: time.Now().Add(7 * 24 * time.Hour), SourceType: "telegram_approved_message"}); e != nil {
				t.Fatal(e)
			}
			requests := &round4Requests{}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				model := requests.add(r)
				if mode == "empty_content" || mode == "whitespace_content" {
					content := ""
					if mode == "whitespace_content" {
						content = "  \n\t"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
					return
				}
				if model == "main" {
					w.Header().Set("Retry-After", "2")
					w.WriteHeader(429)
					return
				}
				if mode == "all_failed" {
					w.WriteHeader(503)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"facts":[{"subject":"hours","content":"closes 8pm","valid_scope":"this_week","source_quote":"closes 8pm"}]}`}}}})
			}))
			defer provider.Close()
			s.assistant.providers.(assistantVerificationProviders).r.clients["mock"] = ai.NewOpenAICompatibleClient(provider.URL, "fake", time.Second, nil)
			s.assistant.extractAndStoreFact(assistantLearningJob{policy: p, chatID: chat, messageID: 80, senderID: 8, senderName: "test", text: text, authority: "learned_fact", sourceType: "telegram_approved_message", sourceChat: chat, operatorID: 8})
			memories, e := q.ListGroupAssistantMemories(ctx, chat, false, "hours", 10)
			if e != nil {
				t.Fatal(e)
			}
			wantMemory := 0
			if mode == "fallback_success" {
				wantMemory = 1
			}
			if len(memories) != wantMemory {
				t.Errorf("fallback duplicated/falsely wrote memories: %+v", memories)
			}
			if len(memories) == 1 {
				versions, e := q.ListGroupAssistantMemoryVersions(ctx, memories[0].ID, chat)
				if e != nil || len(versions) != 1 || memories[0].Version != 1 || memories[0].SourceContentHash != sourceHash {
					t.Errorf("fallback persisted more than once or lost source: %+v %+v %v", memories, versions, e)
				}
			}
			records, e := q.ListGroupAssistantDispatches(ctx, chat, 10)
			if e != nil {
				t.Fatal(e)
			}
			wantAttempts := 2
			if mode == "empty_content" || mode == "whitespace_content" {
				wantAttempts = 1
			}
			if len(records) != wantAttempts || len(requests.snapshot()) != wantAttempts {
				t.Fatalf("attempt audit count mismatch records=%+v HTTP=%v", records, requests.snapshot())
			}
			// Query is newest first; compare every record against the actual HTTP order.
			seen := requests.snapshot()
			for j, record := range records {
				wantModel := "mock:" + seen[len(seen)-1-j]
				if record.ModelRef != wantModel || record.LatencyMs == nil || *record.LatencyMs < 0 {
					t.Errorf("dispatch does not represent actual attempt: %+v want=%s", record, wantModel)
				}
			}
			if mode == "empty_content" || mode == "whitespace_content" {
				if records[0].Status != "error" || records[0].ErrorText == "" {
					t.Errorf("empty learning result falsely audited as success: %+v", records[0])
				}
				var memoryCount, versionCount int
				if e := db.QueryRow(ctx, `SELECT count(*) FROM group_assistant_memories WHERE chat_id=$1`, chat).Scan(&memoryCount); e != nil {
					t.Fatal(e)
				}
				if e := db.QueryRow(ctx, `SELECT count(*) FROM group_assistant_memory_versions v JOIN group_assistant_memories m ON m.id=v.memory_id WHERE m.chat_id=$1`, chat).Scan(&versionCount); e != nil {
					t.Fatal(e)
				}
				if memoryCount != 0 || versionCount != 0 {
					t.Errorf("empty response created persisted facts/audit versions: memories=%d versions=%d", memoryCount, versionCount)
				}
				rt := s.assistant.runtimeSnapshot("mock:main")
				if rt == nil || rt.statusValue() == "healthy" {
					t.Error("empty learning response falsely marked healthy")
				}
				t.Logf("empty-result terminal: HTTP=%v dispatchStatus=%s memoryCount=%d versionCount=%d", requests.snapshot(), records[0].Status, memoryCount, versionCount)
			} else {
				primary := records[1]
				if primary.EndpointID != "main" || primary.Status != "error" || !strings.Contains(primary.ErrorText, "429") {
					t.Errorf("primary 429 audit wrong: %+v", primary)
				}
				backup := records[0]
				wantStatus := "error"
				if mode == "fallback_success" {
					wantStatus = "success"
				}
				if backup.EndpointID != "text" || backup.Status != wantStatus || !strings.Contains(backup.Reason, "current_request_overflow_http_429_after_main") {
					t.Errorf("fallback audit wrong: %+v", backup)
				}
				if mode == "all_failed" && !strings.Contains(backup.ErrorText, "503") {
					t.Errorf("backup failure error lost: %+v", backup)
				}
				rt := s.assistant.runtimeSnapshot("mock:main")
				rt.mu.Lock()
				cooldown, active, status := rt.cooldownUntil, rt.active, rt.status
				rt.mu.Unlock()
				if active != 0 || status != "cooldown" || time.Until(cooldown) < time.Second {
					t.Errorf("429 Retry-After/capacity lost: status=%s active=%d until=%s", status, active, cooldown)
				}
			}
			round4AssertReleased(t, s.assistant)
			t.Logf("actual learning attempts=%v memoryRows=%d dispatchRows=%d", requests.snapshot(), len(memories), len(records))
		})
	}

}

type assistantVerificationTelegramState struct {
	sends    atomic.Int32
	failSend atomic.Bool
	lastSent atomic.Value
	admin    atomic.Bool
	pin      atomic.Int64
	linked   atomic.Int64
}

func assistantVerificationTelegram(t *testing.T, chat int64) (*tele.Bot, *assistantVerificationTelegramState) {
	t.Helper()
	state := &assistantVerificationTelegramState{}
	state.admin.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"id":900,"is_bot":true,"first_name":"Test","username":"assistant_test_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/getChatMember"):
			role := "member"
			if state.admin.Load() {
				role = "administrator"
			}
			fmt.Fprintf(w, `{"ok":true,"result":{"status":%q,"user":{"id":8,"first_name":"Test"}}}`, role)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			n := state.sends.Add(1)
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			text, _ := req["text"].(string)
			state.lastSent.Store(text)
			if state.failSend.Load() {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"fixture send failure"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 700 + n, "chat": map[string]any{"id": chat, "type": "supergroup"}, "from": map[string]any{"id": 900, "is_bot": true, "first_name": "Test"}, "text": text}})
		case strings.HasSuffix(r.URL.Path, "/getChat"):
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			id := chat
			switch v := req["chat_id"].(type) {
			case string:
				id, _ = strconv.ParseInt(v, 10, 64)
			case float64:
				id = int64(v)
			}
			result := map[string]any{"id": id, "type": "supergroup", "title": "Test", "linked_chat_id": state.linked.Load()}
			if state.pin.Load() != 0 {
				result["pinned_message"] = map[string]any{"message_id": state.pin.Load(), "text": "hours closes 8pm"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
		default:
			t.Errorf("unexpected Telegram method intercepted: %s", r.URL.Path)
			fmt.Fprint(w, `{"ok":false,"description":"unexpected local mock request"}`)
		}
	}))
	t.Cleanup(srv.Close)
	tb, e := tele.NewBot(tele.Settings{Token: "123:ASSISTANT_TEST_FAKE", URL: srv.URL, Client: srv.Client(), Synchronous: true})
	if e != nil {
		t.Fatal(e)
	}
	return tb, state
}
