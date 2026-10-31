//go:build integration

package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"go.uber.org/zap"
	"net/http"
)

func TestProfileCleanupRejectsDaysOverflowWithoutDeleting(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	_, err := fx.pool.Exec(ctx, `INSERT INTO profile_check_logs(chat_id,user_id,check_mode,result,created_at) VALUES(-100,1,'bio','ok',now()-interval '100 days'),(-100,2,'bio','ok',now())`)
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(config.Config{}, zap.NewNop(), svc, nil)
	for _, days := range []string{"2147483648", "4294967295", "0", "-1"} {
		r := httptest.NewRequest("DELETE", "/api/admin/profile-check-logs?days="+days, nil)
		w := httptest.NewRecorder()
		c := echo.New().NewContext(r, w)
		c.Set(adminContextKey, fx.admin)
		if err = srv.handleDeleteOldProfileCheckLogs(c); err != nil || w.Code != 400 {
			t.Fatalf("days=%s code=%d err=%v", days, w.Code, err)
		}
	}
	if _, err = fx.queries.DeleteOldProfileCheckLogs(ctx, -1); err == nil {
		t.Fatal("store accepted negative retention")
	}
	var count int
	err = fx.pool.QueryRow(ctx, `SELECT count(*) FROM profile_check_logs`).Scan(&count)
	if err != nil || count != 2 {
		t.Fatal(fmt.Sprintf("logs lost: count=%d err=%v", count, err))
	}
	r := httptest.NewRequest("DELETE", "/api/admin/profile-check-logs?days=30", nil)
	w := httptest.NewRecorder()
	c := echo.New().NewContext(r, w)
	c.Set(adminContextKey, fx.admin)
	if err = srv.handleDeleteOldProfileCheckLogs(c); err != nil || w.Code != 200 {
		t.Fatalf("normal cleanup %d %v", w.Code, err)
	}
	if err = fx.pool.QueryRow(ctx, `SELECT count(*) FROM profile_check_logs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("normal count=%d err=%v", count, err)
	}
}
