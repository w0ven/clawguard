package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
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

func TestNativeAssistantOutboxIsolatesSlowAndFailedDelivery(t *testing.T) {
	dsn := os.Getenv("CG_NATIVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires dedicated local cg_native_test database")
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
	const chatID int64 = -87655
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM assistant_native_events WHERE chat_id=$1`, chatID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM assistant_native_scopes WHERE chat_id=$1`, chatID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM group_assistant_pools WHERE chat_id=$1`, chatID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM group_assistant_policies WHERE chat_id=$1`, chatID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM authorized_groups WHERE chat_id=$1`, chatID)
	}()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var healthy atomic.Bool
	thirdReplyPresent := make(chan bool, 1)
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			http.Error(w, "injected native failure", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/sources/check" {
			_, _ = w.Write([]byte(`{"visible":true}`))
			return
		}
		var event struct {
			GroupID int64 `json:"group_id"`
			Message struct {
				ID      int             `json:"message_id"`
				ReplyTo json.RawMessage `json:"reply_to_message"`
			} `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&event)
		if event.GroupID == chatID && event.Message.ID == 3 {
			select {
			case thirdReplyPresent <- len(event.Message.ReplyTo) > 0 && string(event.Message.ReplyTo) != "null":
			default:
			}
		}
		_, _ = w.Write([]byte(`{"done":true}`))
	}))
	defer native.Close()

	a, poolConfig := assistantVerificationPool(t, func(http.ResponseWriter, *http.Request) {
		t.Error("outbox transport must not call a model directly")
	})
	service := a.service
	service.logger = zap.NewNop()
	service.lifecycleCtx = ctx
	service.cfg.AssistantEngine = "native"
	service.cfg.AssistantBrokerSecret = strings.Repeat("local-fixture-", 4)
	service.cfg.JWTSecret = strings.Repeat("local-signing-", 4)
	service.cfg.AssistantNativeURL = native.URL
	service.queries = store.New(pool)
	service.aiModels, service.aiProviders = a.models, a.providers
	raw, _ := json.Marshal(poolConfig)
	for _, statement := range []string{
		`INSERT INTO authorized_groups(chat_id,title) VALUES ($1,'outbox fixture') ON CONFLICT(chat_id) DO UPDATE SET enabled=true`,
		`INSERT INTO group_assistant_policies(chat_id,chat_enabled) VALUES ($1,true) ON CONFLICT(chat_id) DO UPDATE SET chat_enabled=true`,
	} {
		if _, err = pool.Exec(ctx, statement, chatID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO group_assistant_pools(chat_id,config) VALUES ($1,$2) ON CONFLICT(chat_id) DO UPDATE SET config=EXCLUDED.config`, chatID, raw); err != nil {
		t.Fatal(err)
	}
	service.assistant = NewGroupAssistant(service)
	defer func() { unblock(); cancel(); service.wg.Wait() }()

	sentAt := time.Now().Unix()
	message := func(id int) *tele.Message {
		return &tele.Message{ID: id, Unixtime: sentAt, Text: "approved message", Sender: &tele.User{ID: 7, FirstName: "fixture"}, Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}}
	}
	enqueue := func(msg *tele.Message) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			done <- service.handleApprovedAssistantMessage(ctx, msg, false, assistantEligibilityEligible, false, false)
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("native delivery blocked acknowledgement of an already moderated message")
		}
	}
	enqueue(message(1))
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("durable event was not delivered by the worker")
	}
	// The first native request still holds its scope lock. Another webhook in
	// that same scope must nevertheless persist and return immediately.
	enqueue(message(2))
	enqueue(message(2))
	var pending int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM assistant_native_events WHERE chat_id=$1 AND status='pending'`, chatID).Scan(&pending); err != nil || pending != 2 {
		t.Fatalf("durability/deduplication failure: pending=%d err=%v", pending, err)
	}
	unblock()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("failed delivery was not retained for retry")
	}
	healthy.Store(true)
	service.assistant.native.notifyDelivery()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var done int
		err = pool.QueryRow(ctx, `SELECT count(*) FROM assistant_native_events WHERE chat_id=$1 AND status='done'`, chatID).Scan(&done)
		if err != nil {
			t.Fatal(err)
		}
		if done == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("native recovery did not complete both durable events: %d", done)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Revoke a quoted source after enqueue but before native delivery. The
	// queued message must not resurrect that source when the worker resumes.
	lock := service.assistant.native.scopeLock(chatID, 0)
	lock.Lock()
	third := message(3)
	third.ReplyTo = message(1)
	enqueue(third)
	var storedReply bool
	err = pool.QueryRow(ctx, `SELECT payload->'message'->'reply_to_message' IS NOT NULL FROM assistant_native_events WHERE chat_id=$1 AND telegram_message_id=3`, chatID).Scan(&storedReply)
	if err != nil || !storedReply {
		lock.Unlock()
		t.Fatalf("fixture did not enqueue the visible reply: %v", err)
	}
	err = service.handleApprovedAssistantMessage(ctx, message(1), true, assistantEligibilityBlocked, false, false)
	lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	service.assistant.native.notifyDelivery()
	select {
	case present := <-thirdReplyPresent:
		if present {
			t.Fatal("outbox delivered a reply revoked after enqueue")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("queued message was not delivered after source invalidation")
	}
	cancel()
	service.wg.Wait()
	closedPool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	closedPool.Close()
	service.assistant.queries = store.New(closedPool)
	if err := service.handleApprovedAssistantMessage(context.Background(), message(4), false, assistantEligibilityEligible, false, false); err == nil {
		t.Fatal("database failure was acknowledged without a durable event")
	}
}
