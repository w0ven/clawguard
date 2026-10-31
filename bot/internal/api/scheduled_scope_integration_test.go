//go:build integration

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/scheduler"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

func TestScheduledDeleteCannotRemoveOtherGroupsRuntimeJob(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	_, err := fx.pool.Exec(ctx, `INSERT INTO admins(telegram_id,role,group_scope) VALUES(99,'admin','[-101]'); INSERT INTO groups(chat_id,title,type) VALUES(-101,'one','supergroup'),(-102,'two','supergroup')`)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = fx.pool.QueryRow(ctx, `INSERT INTO scheduled_messages(chat_id,name,schedule_type,interval_minutes,content,enabled,status) VALUES(-102,'target','interval',5,'fixture',true,'active') RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	cfg := config.Config{BotToken: "123:TEST", JWTSecret: "fixture-only"}
	svc, err := bot.New(ctx, cfg, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	sched := scheduler.New(zap.NewNop(), fx.queries, svc.TelegramBot(), nil)
	sched.Start()
	t.Cleanup(func() { <-sched.Stop().Done() })
	msg, err := fx.queries.GetScheduledMessage(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = sched.Add(msg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100 && sched.NextRun(id) == nil; i++ {
		time.Sleep(time.Millisecond * 5)
	}
	if sched.NextRun(id) == nil {
		t.Fatal("job not registered")
	}
	admin, err := fx.queries.GetAdminByTelegramID(ctx, 99)
	if err != nil {
		t.Fatal(err)
	}
	token, err := (&Server{cfg: cfg}).signAdminJWT(admin)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cfg, zap.NewNop(), svc, sched)
	r := httptest.NewRequest("DELETE", fmt.Sprintf("/api/admin/groups/-101/scheduled-messages/%d", id), nil)
	r.AddCookie(&http.Cookie{Name: adminCookieName, Value: token})
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "t"})
	r.Header.Set("X-CSRF-Token", "t")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-group delete status=%d body=%s", w.Code, w.Body)
	}
	if _, err = fx.queries.GetScheduledMessage(ctx, id); err != nil {
		t.Fatalf("target row lost: %v", err)
	}
	if sched.NextRun(id) == nil {
		t.Fatal("target runtime job removed")
	}
}

func TestConcurrentScheduledCreatesRespectPerChatLimit(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 30)
	start := make(chan struct{})
	minutes := int32(5)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := fx.queries.CreateScheduledMessageLimited(ctx, store.CreateScheduledMessageParams{ChatID: -101, Name: "fixture", ScheduleType: "interval", IntervalMinutes: &minutes, Timezone: "Asia/Shanghai", Content: "fixture", Buttons: []byte(`[]`), Status: "paused"}, 20)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	ok, rejected := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, store.ErrScheduledMessageLimit) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	count, err := fx.queries.CountScheduledMessagesByChat(ctx, -101)
	if err != nil || count != 20 || ok != 20 || rejected != 10 {
		t.Fatalf("count=%d success=%d rejected=%d err=%v", count, ok, rejected, err)
	}
}
