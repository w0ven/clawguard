//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"go.uber.org/zap"
)

func TestInvalidStoredAdminScopeFailsClosed(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	_, err := fx.pool.Exec(ctx, `INSERT INTO admins(telegram_id,role,group_scope) VALUES(889,'admin','["-101"]')`)
	if err != nil {
		t.Fatal(err)
	}
	a, err := fx.queries.GetAdminByTelegramID(ctx, 889)
	if err != nil {
		t.Fatal(err)
	}
	if adminHasGlobalAccess(a) || adminCanAccessChat(a, -101) {
		t.Fatal("malformed scope interpreted as unrestricted")
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
	srv := NewServer(cfg, zap.NewNop(), svc, nil)
	token, err := srv.signAdminJWT(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/admin/groups", "/api/admin/warnings", "/api/admin/global-config", "/api/admin/appeals"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: adminCookieName, Value: token})
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}
