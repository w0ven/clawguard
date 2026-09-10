package bot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/pressly/goose/v3"
	tele "gopkg.in/telebot.v3"
)

func TestNativeAssistantRealGoPythonSQLAndTG(t *testing.T) {
	dsn := os.Getenv("CG_NATIVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires dedicated local cg_native_test database and native/.venv")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Path != "/cg_native_test" || u.User.Username() != "cg_native_test" {
		t.Fatal("refusing non-task database")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = goose.UpToContext(ctx, db, "../../migrations", 36); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := store.New(pool)
	var mu sync.Mutex
	requests := []map[string]any{}
	sends := []map[string]any{}
	a, poolConfig := assistantVerificationPool(t, func(w http.ResponseWriter, r *http.Request) {
		var value map[string]any
		_ = json.NewDecoder(r.Body).Decode(&value)
		mu.Lock()
		requests = append(requests, value)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "原生固定回复"}}}})
	})
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value map[string]any
		_ = json.NewDecoder(r.Body).Decode(&value)
		var result any = true
		if strings.HasSuffix(r.URL.Path, "getChatMember") {
			result = map[string]any{"status": "member", "user": map[string]any{"id": 7, "is_bot": false, "first_name": "成员"}}
		} else if strings.HasSuffix(r.URL.Path, "sendMessage") {
			mu.Lock()
			sends = append(sends, value)
			id := 900 + len(sends)
			mu.Unlock()
			result = map[string]any{"message_id": id, "date": time.Now().Unix(), "chat": map[string]any{"id": -76543, "type": "supergroup", "title": "隔离群"}, "from": map[string]any{"id": 900, "is_bot": true, "first_name": "ClawGuard", "username": "assistant_test_bot"}, "text": value["text"]}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	defer tg.Close()
	service := a.service
	service.cfg.AssistantEngine = "native"
	service.cfg.AssistantBrokerSecret = strings.Repeat("local-secret-", 4)
	service.cfg.JWTSecret = strings.Repeat("signing-key-", 4)
	service.bot = &tele.Bot{Token: "123:isolated", URL: tg.URL, Me: &tele.User{ID: 900, IsBot: true, FirstName: "ClawGuard", Username: "assistant_test_bot"}}
	service.queries = q
	service.lifecycleCtx = ctx
	service.aiModels = a.models
	service.aiProviders = a.providers
	a = NewGroupAssistant(service)
	service.assistant = a
	if a.native == nil {
		t.Fatal("official native constructor path was not activated")
	}
	defer func() { cancel(); service.wg.Wait() }()
	broker := httptest.NewServer(service.NativeAssistantHandler())
	defer broker.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	nativeRoot, err := filepath.Abs("../../../native")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	childCtx, childCancel := context.WithCancel(ctx)
	defer childCancel()
	command := exec.CommandContext(childCtx, filepath.Join(nativeRoot, ".venv/bin/python"), "-m", "clawguard_native.app")
	command.Dir = nativeRoot
	command.Env = append(os.Environ(), "NATIVE_DATA_DIR="+t.TempDir(), fmt.Sprintf("NATIVE_PORT=%d", port), "CG_BROKER_URL="+broker.URL+"/internal/native", "ASSISTANT_BROKER_SECRET="+service.cfg.AssistantBrokerSecret)
	command.Stdout = &output
	command.Stderr = &output
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		childCancel()
		_ = command.Wait()
		if t.Failed() {
			t.Log(output.String())
		}
	}()
	service.cfg.AssistantNativeURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	ready := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		resp, e := http.Get(service.cfg.AssistantNativeURL + "/healthz")
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("native did not start")
	}
	raw, _ := json.Marshal(poolConfig)
	if _, err = pool.Exec(ctx, `INSERT INTO authorized_groups(chat_id,title) VALUES (-76543,'隔离群')`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO group_assistant_policies(chat_id,chat_enabled,learning_enabled) VALUES (-76543,true,true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO group_assistant_pools(chat_id,config) VALUES (-76543,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	msg := &tele.Message{ID: 1, Unixtime: time.Now().Unix(), Text: "@assistant_test_bot 你好", Sender: &tele.User{ID: 7, FirstName: "成员"}, Chat: &tele.Chat{ID: -76543, Type: tele.ChatSuperGroup, Title: "隔离群"}}
	if err = service.handleApprovedAssistantMessage(ctx, msg, false, assistantEligibilityEligible, false, false); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		var done int
		err = pool.QueryRow(ctx, `SELECT count(*) FROM assistant_native_events WHERE chat_id=-76543 AND status='done'`).Scan(&done)
		if err != nil {
			t.Fatal(err)
		}
		if done > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 || len(sends) != 1 {
		t.Fatalf("expected one source model round and one delivery; requests=%d sends=%d", len(requests), len(sends))
	}
	if sends[0]["text"] != "原生固定回复" {
		t.Fatalf("wrong delivery %+v", sends)
	}
	first := requests[0]["messages"].([]any)[0].(map[string]any)
	if first["content"] == "" {
		t.Fatal("broker inserted an extra empty system message")
	}
	var legacyFacts int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM group_assistant_memories`).Scan(&legacyFacts); err != nil || legacyFacts != 0 || len(a.learning) != 0 {
		t.Fatalf("legacy automatic learning remained active: facts=%d queue=%d err=%v", legacyFacts, len(a.learning), err)
	}
	var receipts int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM assistant_native_deliveries WHERE chat_id=-76543 AND status='delivered'`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("missing durable TG receipt: %d %v", receipts, err)
	}
	attempt, err := q.BeginNativeDelivery(ctx, "uncertain-fixture", -76543, 0, "sendVoice", "reply")
	if err != nil {
		t.Fatal(err)
	}
	if err = q.FinishNativeDelivery(ctx, attempt, nil, "uncertain"); err != nil {
		t.Fatal(err)
	}
	if _, err = q.BeginNativeDelivery(ctx, "uncertain-fixture", -76543, 0, "sendVoice", "reply"); err == nil {
		t.Fatal("uncertain final side effect was replayable")
	}
	progress, err := q.BeginNativeDelivery(ctx, "uncertain-fixture", -76543, 0, "sendMessage", "progress")
	if err != nil {
		t.Fatal("progress must be independently scoped", err)
	}
	if err = q.FinishNativeDelivery(ctx, progress, nil, "failed"); err != nil {
		t.Fatal(err)
	}
	old, err := q.EnqueueNativeAssistantEvent(ctx, store.NativeAssistantEvent{ChatID: -76543, MessageID: 2, Revision: "old", Kind: "message", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.EnqueueNativeAssistantEvent(ctx, store.NativeAssistantEvent{ChatID: -76543, MessageID: 2, Revision: "revoked", Kind: "invalidate", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	current, err := q.NativeEventIsCurrent(ctx, old)
	if err != nil || current {
		t.Fatal("superseded source could be replayed", err)
	}
}
