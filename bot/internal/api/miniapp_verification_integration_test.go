//go:build integration

package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

func miniSignedData(user int64, at time.Time, secret string) string {
	v := url.Values{"auth_date": {fmt.Sprint(at.Unix())}, "user": {fmt.Sprintf(`{"id":%d,"first_name":"Test"}`, user)}}
	keys := make([]string, 0, len(v))
	for key := range v {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		parts = append(parts, key+"="+v.Get(key))
	}
	key := hmac.New(sha256.New, []byte("WebAppData"))
	key.Write([]byte(secret))
	sig := hmac.New(sha256.New, key.Sum(nil))
	sig.Write([]byte(strings.Join(parts, "\n")))
	v.Set("hash", hex.EncodeToString(sig.Sum(nil)))
	return v.Encode()
}
func TestMiniAppIdentityAndLegacyIsolation(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	_, err := fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-100,'Fixture group','supergroup'); INSERT INTO authorized_groups(chat_id,enabled) VALUES(-100,true)`)
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	cfg := config.Config{BotToken: "123:TEST", PublicBaseURL: "https://example.com", TurnstileSiteKey: "cf", TurnstileSecret: "secret", HCaptchaSiteKey: "hc", HCaptchaSecret: "secret"}
	svc, err := bot.New(ctx, cfg, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(cfg, zap.NewNop(), svc, nil)
	token := "12345678-1234-1234-1234-123456789012"
	p, err := fx.queries.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -100, UserID: 222, Method: "turnstile_hcaptcha", Payload: []byte(fmt.Sprintf(`{"token":%q,"miniapp":true,"membership_generation":"g1"}`, token)), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	data := miniSignedData(222, time.Now(), cfg.BotToken)
	body := func(s string) string { return fmt.Sprintf(`{"token":%q,"init_data":%q}`, token, s) }
	ok := request("/api/verify/mini/config", body(data))
	if ok.Code != 200 || !strings.Contains(ok.Body.String(), "Fixture group") {
		t.Fatalf("valid %d %s", ok.Code, ok.Body.String())
	}
	for name, signed := range map[string]string{"missing": "", "other_user": miniSignedData(223, time.Now(), cfg.BotToken), "expired": miniSignedData(222, time.Now().Add(-time.Hour), cfg.BotToken), "forged": miniSignedData(222, time.Now(), "other")} {
		t.Run(name, func(t *testing.T) {
			w := request("/api/verify/mini/config", body(signed))
			if w.Code != 403 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	if w := request("/api/verify/mini/config", body(miniSignedData(222, time.Now().Add(-11*time.Minute), cfg.BotToken))); w.Code != 403 || !strings.Contains(w.Body.String(), "关闭页面") {
		t.Fatalf("expired identity: %d %s", w.Code, w.Body.String())
	}
	for name, signed := range map[string]string{"missing": "", "other": miniSignedData(223, time.Now(), cfg.BotToken), "expired": miniSignedData(222, time.Now().Add(-11*time.Minute), cfg.BotToken), "valid_unverified": data} {
		if w := request("/api/verify/mini/recover", body(signed)); w.Code == 200 {
			t.Fatalf("%s recovery bypass", name)
		}
	}
	if _, err = fx.pool.Exec(ctx, `INSERT INTO dual_captcha_claims(pending_id,token,state,lease_until) VALUES($1,'fixture','conflict',now()) ON CONFLICT(pending_id) DO UPDATE SET state='conflict',lease_until=now()`, p.ID); err != nil {
		t.Fatal(err)
	}
	if w := request("/api/verify/mini/config", body(data)); w.Code != 409 || !strings.Contains(w.Body.String(), "联系群管理员") {
		t.Fatalf("conflict: %d %s", w.Code, w.Body.String())
	}
	if w := request("/api/verify/mini/recover", body(data)); w.Code != 409 {
		t.Fatalf("conflict recovery: %d %s", w.Code, w.Body.String())
	}
	if _, err = fx.pool.Exec(ctx, `DELETE FROM dual_captcha_claims WHERE pending_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	badSubmit := request("/api/verify/mini/submit", fmt.Sprintf(`{"token":%q,"init_data":%q,"cf_response":"x","h_response":"y"}`, token, miniSignedData(223, time.Now(), cfg.BotToken)))
	if badSubmit.Code == 200 {
		t.Fatal("other user submitted")
	}
	if _, err = fx.pool.Exec(ctx, `UPDATE pending_verifications SET expires_at=now()-interval '1 second' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if w := request("/api/verify/mini/config", body(data)); w.Code == 200 {
		t.Fatal("expired task accepted")
	}
	if _, err = fx.pool.Exec(ctx, `UPDATE pending_verifications SET expires_at=now()+interval '1 minute' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	legacy := request("/api/verify/dual/config", body(""))
	if legacy.Code == 200 {
		t.Fatal("old config accepts mini task")
	}
	bypass := request("/api/verify/dual/submit", fmt.Sprintf(`{"token":%q,"cf_response":"x","h_response":"y"}`, token))
	if bypass.Code == 200 {
		t.Fatal("old submit accepts mini task")
	}
	_, _, err = fx.queries.ClaimDualCaptchaForUser(ctx, token, "bad", 223)
	if err == nil {
		t.Fatal("other user claimed pending")
	}
	_, _, err = fx.queries.ClaimDualCaptcha(ctx, token, "bad")
	if err == nil {
		t.Fatal("legacy claimed mini pending")
	}
	newToken := "22345678-1234-1234-1234-123456789012"
	replacement, err := fx.queries.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -100, UserID: 222, Method: "turnstile_hcaptcha", Payload: []byte(fmt.Sprintf(`{"token":%q,"miniapp":true,"membership_generation":"g2"}`, newToken)), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || replacement.ID == p.ID {
		t.Fatalf("replacement: %v", err)
	}
	if w := request("/api/verify/mini/config", body(data)); w.Code == 200 {
		t.Fatal("old instance usable")
	}
}
