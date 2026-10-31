//go:build integration

package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"go.uber.org/zap"
)

type muteTransport struct {
	mu               sync.Mutex
	until            int64
	restricted       bool
	fail             bool
	calls            int
	started, release chan struct{}
}

func (t *muteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/getChatMember") {
		t.mu.Lock()
		until, restricted := t.until, t.restricted
		t.mu.Unlock()
		status := "member"
		if restricted {
			status = "restricted"
		}
		body := fmt.Sprintf(`{"ok":true,"result":{"user":{"id":123,"is_bot":false,"first_name":"fixture"},"status":%q,"is_member":true,"until_date":%d}}`, status, until)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	if strings.HasSuffix(r.URL.Path, "/restrictChatMember") {
		t.mu.Lock()
		t.calls++
		fail, started, release := t.fail, t.started, t.release
		t.mu.Unlock()
		if started != nil {
			close(started)
			<-release
		}
		if fail {
			return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":403,"description":"Forbidden: insufficient rights"}`))}, nil
		}
		t.mu.Lock()
		t.restricted = false
		t.mu.Unlock()
	}
	return (exemptTelegramTransport{}).RoundTrip(r)
}
func (t *muteTransport) count() int { t.mu.Lock(); defer t.mu.Unlock(); return t.calls }
func TestAIMuteReviewIntegration(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	until := time.Now().Add(10 * time.Minute).Unix()
	_, err := fx.pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score) VALUES(-100,123,'new',0.5),(-200,123,'banned',0)`)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = fx.pool.QueryRow(ctx, `INSERT INTO ai_decisions(chat_id,user_id,message_id,model,prompt_version,verdict,confidence,category,action_taken) VALUES(-100,123,1,'x','x','spam',1,'spam','mute') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err = fx.queries.ReserveAIMute(ctx, -100, 123, id, until, "owner"); err != nil {
		t.Fatal(err)
	}
	if ok, e := fx.queries.ActivateAIMute(ctx, -100, 123, "owner"); e != nil || !ok {
		t.Fatalf("activate %v %v", ok, e)
	}
	tr := &muteTransport{until: until, restricted: true, fail: true}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	server := NewServer(config.Config{}, zap.NewNop(), svc, nil)
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/", strings.NewReader(`{"admin_override":"false_positive"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.Set(adminContextKey, fx.admin)
		c.SetParamNames("id")
		c.SetParamValues(fmt.Sprint(id))
		if e := server.handleUpdateAIDecision(c); e != nil {
			t.Error(e)
		}
		return rec
	}
	rec := call()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "解除禁言失败") || tr.count() != 1 {
		t.Fatalf("first failure %d %s calls=%d", rec.Code, rec.Body, tr.count())
	}
	var score float64
	if err = fx.pool.QueryRow(ctx, `SELECT score FROM user_trust WHERE chat_id=-100 AND user_id=123`).Scan(&score); err != nil || score < 0.599 || score > 0.601 {
		t.Fatalf("first score %v %v", score, err)
	}
	tr.mu.Lock()
	tr.fail = false
	tr.started = make(chan struct{})
	tr.release = make(chan struct{})
	started, release := tr.started, tr.release
	tr.mu.Unlock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call() }()
	<-started
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { second <- call() }()
	select {
	case got := <-second:
		close(release)
		t.Fatalf("concurrent subject was not serialized: %d %s", got.Code, got.Body)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	rec = <-done
	if got := <-second; got.Code != 200 || tr.count() != 2 {
		t.Fatalf("concurrent completion %d %s calls=%d", got.Code, got.Body, tr.count())
	}
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unmute_performed":true`) {
		t.Fatalf("retry %d %s", rec.Code, rec.Body)
	}
	tr.mu.Lock()
	tr.started, tr.release = nil, nil
	tr.mu.Unlock()
	rec = call()
	if rec.Code != 200 || tr.count() != 2 {
		t.Fatalf("repeat %d %s %d", rec.Code, rec.Body, tr.count())
	}
	if err = fx.pool.QueryRow(ctx, `SELECT score FROM user_trust WHERE chat_id=-100 AND user_id=123`).Scan(&score); err != nil || score < 0.599 || score > 0.601 {
		t.Fatalf("repeat score %v %v", score, err)
	}
	newMute := func() int64 {
		t.Helper()
		var v int64
		if e := fx.pool.QueryRow(ctx, `INSERT INTO ai_decisions(chat_id,user_id,message_id,model,prompt_version,verdict,confidence,category,action_taken) VALUES(-100,123,2,'x','x','spam',1,'spam','mute') RETURNING id`).Scan(&v); e != nil {
			t.Fatal(e)
		}
		if e := fx.queries.ReserveAIMute(ctx, -100, 123, v, until, fmt.Sprint(v)); e != nil {
			t.Fatal(e)
		}
		if ok, e := fx.queries.ActivateAIMute(ctx, -100, 123, fmt.Sprint(v)); e != nil || !ok {
			t.Fatalf("activate %v %v", ok, e)
		}
		tr.mu.Lock()
		tr.restricted = true
		tr.until = until
		tr.mu.Unlock()
		return v
	}
	id = newMute()
	if _, err = fx.pool.Exec(ctx, `CREATE FUNCTION fail_mute_settle() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.state='completed' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END$$; CREATE TRIGGER fail_mute_settle BEFORE UPDATE ON ai_mute_review_claims FOR EACH ROW EXECUTE FUNCTION fail_mute_settle()`); err != nil {
		t.Fatal(err)
	}
	before := tr.count()
	rec = call()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "结算失败") || tr.count() != before+1 {
		t.Fatalf("settlement failure %d %s", rec.Code, rec.Body)
	}
	if _, err = fx.pool.Exec(ctx, `DROP TRIGGER fail_mute_settle ON ai_mute_review_claims;DROP FUNCTION fail_mute_settle()`); err != nil {
		t.Fatal(err)
	}
	rec = call()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unmute_performed":true`) || tr.count() != before+1 {
		t.Fatalf("settlement retry %d %s", rec.Code, rec.Body)
	}
	id = newMute()
	tr.mu.Lock()
	tr.until = until + 3600
	tr.mu.Unlock()
	before = tr.count()
	rec = call()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "不一致") || tr.count() != before {
		t.Fatalf("independent telegram restriction %d %s", rec.Code, rec.Body)
	}
	id = newMute()
	tr.mu.Lock()
	tr.started = make(chan struct{})
	tr.release = make(chan struct{})
	started, release = tr.started, tr.release
	tr.mu.Unlock()
	done = make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call() }()
	<-started
	banDone := make(chan error, 1)
	go func() { banDone <- svc.BanChatUser(ctx, -100, 123) }()
	select {
	case e := <-banDone:
		close(release)
		t.Fatalf("ban did not wait for review: %v", e)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	rec = <-done
	if e := <-banDone; e != nil {
		t.Fatal(e)
	}
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unmute_performed":true`) {
		t.Fatalf("review before later ban %d %s", rec.Code, rec.Body)
	}
	tr.mu.Lock()
	tr.started, tr.release = nil, nil
	tr.mu.Unlock()
	before = tr.count()
	rec = call()
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"unmute_performed":true`) || tr.count() != before {
		t.Fatalf("completed review repeated after later ban %d %s", rec.Code, rec.Body)
	}
	var other string
	if err = fx.pool.QueryRow(ctx, `SELECT status FROM user_trust WHERE chat_id=-200 AND user_id=123`).Scan(&other); err != nil || other != "banned" {
		t.Fatalf("cross group %s %v", other, err)
	}
}
