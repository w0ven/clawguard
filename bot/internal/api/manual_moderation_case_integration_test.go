//go:build integration

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"go.uber.org/zap"
)

func TestManualModerationCases(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-100,'test','supergroup'); INSERT INTO user_trust(chat_id,user_id,status,score,is_bot) VALUES(-100,102,'new',0.5,true)`); err != nil {
		t.Fatal(err)
	}
	tr := &trustSyncTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(config.Config{}, zap.NewNop(), svc, nil)
	call := func(action string, id int) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/admin/%s?chat_id=-100&user_id=%d", action, id), nil)
		c := echo.New().NewContext(req, rec)
		c.Set(adminContextKey, fx.admin)
		if action == "ban" {
			err = srv.handleBan(c)
		} else {
			err = srv.handleUnban(c)
		}
		if err != nil {
			t.Fatal(err)
		}
		return rec.Code
	}
	if code := call("ban", 101); code != 204 {
		t.Fatalf("ban: %d", code)
	}
	if code := call("unban", 101); code != 204 {
		t.Fatalf("unban: %d", code)
	}
	tr.setFailure("/kickChatMember")
	if code := call("ban", 103); code != 500 {
		t.Fatalf("failed ban: %d", code)
	}
	tr.setFailure("")
	if code := call("ban", 102); code != 204 {
		t.Fatalf("bot ban: %d", code)
	}
	if code := call("ban", 999); code != 204 {
		t.Fatalf("self ban: %d", code)
	}
	if _, err = fx.pool.Exec(ctx, `CREATE FUNCTION fail_manual_ban_insert() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'injected'; END$$; CREATE TRIGGER fail_manual_ban_insert BEFORE INSERT ON group_ban_records FOR EACH ROW EXECUTE FUNCTION fail_manual_ban_insert()`); err != nil {
		t.Fatal(err)
	}
	if code := call("ban", 104); code != 500 {
		t.Fatalf("database failure: %d", code)
	}
	var ban, unban, failed, partial, botCases int
	if err = fx.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE user_id=101 AND action='ban' AND result='executed'), count(*) FILTER (WHERE user_id=101 AND action='unban' AND result='partial'), count(*) FILTER (WHERE user_id=103 AND action='ban' AND result='failed'), count(*) FILTER (WHERE user_id=104 AND action='ban' AND result='partial'), count(*) FILTER (WHERE user_id IN (102,999)) FROM moderation_cases WHERE source_kind='admin_manual'`).Scan(&ban, &unban, &failed, &partial, &botCases); err != nil {
		t.Fatal(err)
	}
	if ban != 1 || unban != 1 || failed != 1 || partial != 1 || botCases != 0 {
		t.Fatalf("cases ban=%d unban=%d failed=%d partial=%d bot=%d", ban, unban, failed, partial, botCases)
	}
	var unreachable int
	if err = fx.pool.QueryRow(ctx, `SELECT count(*) FROM moderation_notifications WHERE state='unreachable'`).Scan(&unreachable); err != nil || unreachable != 4 {
		t.Fatalf("unreachable %d: %v", unreachable, err)
	}
}

func TestManualModerationIdempotentRetry(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	tr := &trustSyncTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(config.Config{}, zap.NewNop(), svc, nil)
	const key = "moderation-request-1234567890123456"
	call := func(k string, user int, handler echo.HandlerFunc) int {
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/admin/ban?chat_id=-100&user_id=%d", user), nil)
		req.Header.Set("Idempotency-Key", k)
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.Set(adminContextKey, fx.admin)
		if err := srv.manualModerationIdempotency(handler)(c); err != nil {
			t.Fatal(err)
		}
		return rec.Code
	}
	if call(key, 101, srv.handleBan) != 204 || call(key, 101, srv.handleBan) != 204 {
		t.Fatal("successful request did not replay")
	}
	if tr.count("/kickChatMember") != 1 {
		t.Fatalf("repeated Telegram action: %d", tr.count("/kickChatMember"))
	}
	var cases int
	if err = fx.pool.QueryRow(ctx, `SELECT count(*) FROM moderation_cases WHERE source_kind='admin_manual'`).Scan(&cases); err != nil || cases != 1 {
		t.Fatalf("cases=%d err=%v", cases, err)
	}
	if call(key, 102, srv.handleBan) != 409 {
		t.Fatal("rebound operation accepted")
	}
	const interrupted = "interrupted-request-12345678901234"
	if _, err = fx.pool.Exec(ctx, `INSERT INTO manual_moderation_requests(request_key,admin_id,chat_id,user_id,action) VALUES($1,$2,-100,101,'/api/admin/ban')`, interrupted, fx.admin.TelegramID); err != nil {
		t.Fatal(err)
	}
	if call(interrupted, 101, srv.handleBan) != 409 || tr.count("/kickChatMember") != 1 {
		t.Fatal("uncertain action was dispatched again")
	}
	calls := 0
	handler := func(c echo.Context) error { calls++; return c.JSON(500, map[string]string{"error": "fixture failure"}) }
	const failed = "failed-request-1234567890123456789"
	if call(failed, 101, handler) != 500 || call(failed, 101, handler) != 500 || calls != 1 {
		t.Fatal("failed outcome was not replayed safely")
	}
}

// A manual change must retain the subject gate while its post-Telegram DB
// settlement is blocked, or a later action may be overwritten by stale state.
func TestManualModerationGateCoversSettlement(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	tr := &trustSyncTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(config.Config{}, zap.NewNop(), svc, nil)
	_, err = fx.pool.Exec(ctx, `CREATE FUNCTION block_manual_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(739187); RETURN COALESCE(NEW,OLD); END $$; CREATE TRIGGER block_manual_insert BEFORE INSERT ON group_ban_records FOR EACH ROW EXECUTE FUNCTION block_manual_settlement(); CREATE TRIGGER block_manual_delete BEFORE DELETE ON group_ban_records FOR EACH ROW EXECUTE FUNCTION block_manual_settlement()`)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"ban", "unban"} {
		t.Run(action, func(t *testing.T) {
			conn, e := fx.pool.Acquire(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer conn.Release()
			if _, e = conn.Exec(ctx, `SELECT pg_advisory_lock(739187)`); e != nil {
				t.Fatal(e)
			}
			defer conn.Exec(ctx, `SELECT pg_advisory_unlock(739187)`)
			path := "/kickChatMember"
			if action == "unban" {
				path = "/unbanChatMember"
			}
			before := tr.count(path)
			done := make(chan int, 1)
			go func() {
				r := httptest.NewRequest("POST", fmt.Sprintf("/api/admin/%s?chat_id=-100&user_id=101", action), nil)
				w := httptest.NewRecorder()
				c := echo.New().NewContext(r, w)
				c.Set(adminContextKey, fx.admin)
				var e error
				if action == "ban" {
					e = srv.handleBan(c)
				} else {
					e = srv.handleUnban(c)
				}
				if e != nil {
					done <- 0
				} else {
					done <- w.Code
				}
			}()
			deadline := time.Now().Add(3 * time.Second)
			for tr.count(path) == before && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if tr.count(path) == before {
				t.Fatal("telegram action not reached")
			}
			// The handler cannot finish before the advisory lock is released.
			select {
			case code := <-done:
				t.Fatalf("settled unexpectedly: %d", code)
			default:
			}
			wait, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			unlock, e := svc.AcquireModerationReview(wait, -100, 101)
			if e == nil {
				unlock()
				t.Fatal("subject gate released before settlement")
			}
			other, e := svc.AcquireModerationReview(ctx, -100, 102)
			if e != nil {
				t.Fatal(e)
			}
			other()
			if _, e = conn.Exec(ctx, `SELECT pg_advisory_unlock(739187)`); e != nil {
				t.Fatal(e)
			}
			select {
			case code := <-done:
				if code != 204 {
					t.Fatalf("code=%d", code)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("settlement timed out")
			}
		})
	}
}
