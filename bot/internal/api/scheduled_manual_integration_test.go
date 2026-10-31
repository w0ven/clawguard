//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/scheduler"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

type manualSendTransport struct {
	sends     atomic.Int32
	unknown   atomic.Bool
	rejected  atomic.Bool
	malformed atomic.Bool
	payloads  chan string
	started   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (tr *manualSendTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.telegram.org" {
		return nil, fmt.Errorf("unexpected network host")
	}
	body := `{"ok":true,"result":2}`
	switch {
	case strings.HasSuffix(r.URL.Path, "/getMe"):
		body = `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"test","username":"test_bot"}}`
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		tr.sends.Add(1)
		if tr.payloads != nil {
			data, _ := io.ReadAll(r.Body)
			tr.payloads <- string(data)
		}
		if tr.started != nil {
			tr.once.Do(func() { close(tr.started); <-tr.release })
		}
		if tr.unknown.Load() {
			return nil, context.DeadlineExceeded
		}
		if tr.rejected.Load() {
			body = `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`
			break
		}
		if tr.malformed.Load() {
			body = `{"result":{"message_id":123}}`
			break
		}
		body = `{"ok":true,"result":{"message_id":123,"chat":{"id":-100,"type":"supergroup"}}}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestManualScheduledIdempotentAPI(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	_, err := fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-100,'one','supergroup'),(-200,'two','supergroup')`)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 101, Role: "owner", GroupScope: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 102, Role: "admin", GroupScope: []byte(`[-100]`)})
	if err != nil {
		t.Fatal(err)
	}
	var task, other int64
	err = fx.pool.QueryRow(ctx, `INSERT INTO scheduled_messages(chat_id,name,schedule_type,interval_minutes,content,buttons,auto_delete_seconds) VALUES(-100,'one','interval',5,'hello','[{"text":"link","url":"https://example.com"}]',60) RETURNING id`).Scan(&task)
	if err != nil {
		t.Fatal(err)
	}
	err = fx.pool.QueryRow(ctx, `INSERT INTO scheduled_messages(chat_id,name,schedule_type,interval_minutes,content) VALUES(-200,'two','interval',5,'world') RETURNING id`).Scan(&other)
	if err != nil {
		t.Fatal(err)
	}
	tr := &manualSendTransport{started: make(chan struct{}), release: make(chan struct{}), payloads: make(chan string, 10)}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	cfg := config.Config{BotToken: "123:TEST", JWTSecret: "test-only"}
	svc, err := bot.New(ctx, cfg, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	sched := scheduler.New(zap.NewNop(), fx.queries, svc.TelegramBot(), nil)
	t.Cleanup(func() { <-sched.Stop().Done() })
	srv := NewServer(cfg, zap.NewNop(), svc, sched)
	token := func(a store.Admin) string {
		v, e := srv.signAdminJWT(a)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	ownerToken, scopedToken := token(owner), token(scoped)
	call := func(chat, id int64, key, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/admin/groups/%d/scheduled-messages/%d/run-now", chat, id), nil)
		r.AddCookie(&http.Cookie{Name: adminCookieName, Value: tok})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf"})
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	key := strings.Repeat("a", 32)
	if w := call(-100, task, "", ownerToken); w.Code != 400 {
		t.Fatalf("missing key %d %s", w.Code, w.Body)
	}
	if w := call(-200, other, key, scopedToken); w.Code != 403 {
		t.Fatalf("scope %d", w.Code)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call(-100, task, key, ownerToken) }()
	select {
	case <-tr.started:
	case <-time.After(5 * time.Second):
		t.Fatal("send not started")
	}
	var wg sync.WaitGroup
	rs := make([]*httptest.ResponseRecorder, 8)
	for i := range rs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); rs[i] = call(-100, task, key, ownerToken) }(i)
	}
	wg.Wait()
	for _, w := range rs {
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"dispatching"`) {
			t.Fatalf("in progress %d %s", w.Code, w.Body)
		}
	}
	close(tr.release)
	w := <-done
	payload := <-tr.payloads
	if !strings.Contains(payload, `"parse_mode":"MarkdownV2"`) || !strings.Contains(payload, `"text":"hello"`) || !strings.Contains(payload, `"inline_keyboard"`) || !strings.Contains(payload, "https://example.com") {
		t.Fatalf("send payload changed: %s", payload)
	}
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"sent"`) || !strings.Contains(w.Body.String(), `"message_id":123`) {
		t.Fatalf("send %d %s", w.Code, w.Body)
	}
	w = call(-100, task, key, ownerToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"message_id":123`) || tr.sends.Load() != 1 {
		t.Fatalf("lost response replay %d %s sends=%d", w.Code, w.Body, tr.sends.Load())
	}
	var count int
	if err = fx.pool.QueryRow(ctx, `SELECT count(*) FROM scheduled_delete_jobs j JOIN scheduled_manual_requests r ON r.attempt_id=j.attempt_id WHERE r.task_id=$1 AND j.telegram_message_id=123`, task).Scan(&count); err != nil || count != 1 {
		t.Fatalf("delete task %d %v", count, err)
	}
	if w = call(-200, other, key, ownerToken); w.Code != 409 {
		t.Fatalf("other target %d %s", w.Code, w.Body)
	}
	if w = call(-100, task, key, scopedToken); w.Code != 409 {
		t.Fatalf("other admin %d %s", w.Code, w.Body)
	}
	_, err = fx.pool.Exec(ctx, `UPDATE scheduled_messages SET content='changed' WHERE id=$1`, task)
	if err != nil {
		t.Fatal(err)
	}
	if w = call(-100, task, key, ownerToken); w.Code != 409 {
		t.Fatalf("changed config %d %s", w.Code, w.Body)
	}
	tr.unknown.Store(true)
	key2 := strings.Repeat("b", 32)
	w = call(-100, task, key2, ownerToken)
	var result struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if w.Code != 200 || result.Status != "unknown" {
		t.Fatalf("unknown %d %s", w.Code, w.Body)
	}
	w = call(-100, task, key2, ownerToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unknown"`) || tr.sends.Load() != 2 {
		t.Fatalf("unknown retry %d %s sends=%d", w.Code, w.Body, tr.sends.Load())
	}
	tr.unknown.Store(false)
	tr.rejected.Store(true)
	key3 := strings.Repeat("c", 32)
	w = call(-100, task, key3, ownerToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"failed"`) {
		t.Fatalf("explicit refusal %d %s", w.Code, w.Body)
	}
	w = call(-100, task, key3, ownerToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"failed"`) || tr.sends.Load() != 3 {
		t.Fatalf("refusal retry %d %s", w.Code, w.Body)
	}
	tr.rejected.Store(false)
	tr.malformed.Store(true)
	keyMalformed := strings.Repeat("e", 32)
	w = call(-100, task, keyMalformed, ownerToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unknown"`) {
		t.Fatalf("malformed response %d %s", w.Code, w.Body)
	}
	w = call(-100, task, keyMalformed, ownerToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unknown"`) || tr.sends.Load() != 4 {
		t.Fatalf("malformed retry %d %s", w.Code, w.Body)
	}
	tr.malformed.Store(false)
	_, err = fx.pool.Exec(ctx, `CREATE FUNCTION fail_manual_settle() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture settlement failure'; END $$; CREATE TRIGGER fail_manual_settle BEFORE UPDATE ON scheduled_dispatch_attempts FOR EACH ROW EXECUTE FUNCTION fail_manual_settle()`)
	if err != nil {
		t.Fatal(err)
	}
	key4 := strings.Repeat("d", 32)
	w = call(-100, task, key4, ownerToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unknown"`) {
		t.Fatalf("settlement failure %d %s", w.Code, w.Body)
	}
	w = call(-100, task, key4, ownerToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"dispatching"`) || tr.sends.Load() != 5 {
		t.Fatalf("settlement retry %d %s sends=%d", w.Code, w.Body, tr.sends.Load())
	}
}
