//go:build integration

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

func TestDualCaptchaAPIConfigAndBypass(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	admin, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 222, Role: "owner", GroupScope: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-100,'fixture','supergroup'); INSERT INTO authorized_groups(chat_id,enabled) VALUES(-100,true)`); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	cfg := config.Config{BotToken: "123:TEST", JWTSecret: "fixture-jwt", PublicBaseURL: "https://example.com"}
	svc, err := bot.New(ctx, cfg, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(cfg, zap.NewNop(), svc, nil)
	jwtToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, adminClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: fmt.Sprint(admin.ID), ID: fmt.Sprint(admin.ID), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if auth {
			r.AddCookie(&http.Cookie{Name: adminCookieName, Value: jwtToken})
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf"})
			r.Header.Set("X-CSRF-Token", "csrf")
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	group := request("PUT", "/api/admin/groups/-100/config", `{"verify":{"method":"turnstile_hcaptcha"}}`, true)
	if group.Code != 400 || !strings.Contains(group.Body.String(), "不可用") {
		t.Fatalf("group missing keys: %d %s", group.Code, group.Body.String())
	}
	global, err := fx.queries.GetGlobalConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	globalBody := fmt.Sprintf(`{"section":"document","version":%d,"config":{"verify":{"method":"turnstile_hcaptcha"}}}`, global.Version)
	globalResult := request("PUT", "/api/admin/global-config", globalBody, true)
	if globalResult.Code != 400 || !strings.Contains(globalResult.Body.String(), "不可用") {
		t.Fatalf("global missing keys: %d %s", globalResult.Code, globalResult.Body.String())
	}
	keys := request("GET", "/api/verify/capabilities", "", false)
	if keys.Code != 200 || !strings.Contains(keys.Body.String(), `"dual_captcha_available":false`) {
		t.Fatalf("capabilities %d %s", keys.Code, keys.Body.String())
	}
	token := "12345678-1234-1234-1234-123456789012"
	_, err = fx.queries.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -100, UserID: 222, Method: "turnstile_hcaptcha", Payload: []byte(fmt.Sprintf(`{"token":%q}`, token)), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fx.queries.DeletePendingVerificationByToken(ctx, token); err == nil {
		t.Fatal("legacy turnstile endpoint could consume dual pending")
	}
	srv.cfg.TurnstileSiteKey = "cf-public"
	srv.cfg.TurnstileSecret = "cf-secret-must-not-leak"
	srv.cfg.HCaptchaSiteKey = "h-public"
	srv.cfg.HCaptchaSecret = "h-secret-must-not-leak"
	public := request("POST", "/api/verify/dual/config", fmt.Sprintf(`{"token":%q}`, token), false)
	if public.Code != 200 || !strings.Contains(public.Body.String(), "cf-public") || !strings.Contains(public.Body.String(), "h-public") || strings.Contains(public.Body.String(), "secret") || public.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("public sitekeys response: %d %s", public.Code, public.Body.String())
	}
	missing := request("POST", "/api/verify/dual/submit", fmt.Sprintf(`{"token":%q,"cf_response":"fake"}`, token), false)
	if missing.Code != 400 {
		t.Fatalf("missing hcaptcha: %d", missing.Code)
	}
	oldEndpoint := request("POST", "/api/verify/turnstile/"+token, `{"cf_response":"fake"}`, false)
	if oldEndpoint.Code == 200 {
		t.Fatal("legacy turnstile endpoint passed dual pending")
	}
	if _, err = fx.queries.GetActiveDualCaptcha(ctx, token); err != nil {
		t.Fatalf("pending consumed: %v", err)
	}
}
