//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

func TestAppealMiniIdentityChallengeAndConcurrency(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-100,'Test group','supergroup')`); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 4; i++ {
		id, e := fx.queries.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: fmt.Sprintf("appeal-test:%d", i), SourceKind: "test", SourceID: fmt.Sprint(i), ChatID: -100, UserID: []int64{111, 111, 222, 111}[i], Action: "ban", Result: "executed", Summary: "safe summary", Ownership: []byte(`{"secret":"private prompt"}`)})
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
	}
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	cfg := config.Config{BotToken: "123:TEST", PublicBaseURL: "https://example.com", TurnstileSiteKey: "cf", TurnstileSecret: "secret", HCaptchaSiteKey: "hc", HCaptchaSecret: "secret"}
	svc, e := bot.New(ctx, cfg, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(cfg, zap.NewNop(), svc, nil)
	srv.appealVerifier = func(_ context.Context, token, cf, hc, ip string) error {
		if !strings.HasPrefix(cf, "cf-good") || !strings.HasPrefix(hc, "hc-good") {
			return errors.New("bad provider")
		}
		return nil
	}
	user := miniSignedData(111, time.Now(), cfg.BotToken)
	other := miniSignedData(222, time.Now(), cfg.BotToken)
	call := func(path string, p map[string]any) (int, map[string]any) {
		raw, _ := json.Marshal(p)
		req := httptest.NewRequest("POST", "/api/appeal/mini/"+path, bytes.NewReader(raw))
		rec := httptest.NewRecorder()
		req.Header.Set("Content-Type", "application/json")
		srv.ServeHTTP(rec, req)
		var b map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		return rec.Code, b
	}
	for _, bad := range []string{"", miniSignedData(111, time.Now(), "wrong"), miniSignedData(111, time.Now().Add(-11*time.Minute), cfg.BotToken)} {
		if code, _ := call("list", map[string]any{"init_data": bad}); code != 403 {
			t.Fatalf("bad identity accepted: %d", code)
		}
	}
	if code, b := call("list", map[string]any{"init_data": user}); code != 200 || strings.Contains(fmt.Sprint(b), "private prompt") || len(b["cases"].([]any)) != 3 {
		t.Fatalf("list %d %+v", code, b)
	}
	if code, _ := call("case", map[string]any{"init_data": user, "case_id": ids[2]}); code != 404 {
		t.Fatalf("cross user %d", code)
	}
	challenge := func(id int64) string {
		code, b := call("challenge", map[string]any{"init_data": user, "case_id": id})
		if code != 200 {
			t.Fatalf("challenge %d %+v", code, b)
		}
		return b["token"].(string)
	}
	submit := func(id int64, tok, cf, hc, data string) (int, map[string]any) {
		if cf == "cf-good" {
			cf += tok
		}
		if hc == "hc-good" {
			hc += tok
		}
		return call("submit", map[string]any{"init_data": data, "case_id": id, "token": tok, "reason": "example reason", "reference_url": "https://example.com/proof", "cf_response": cf, "h_response": hc})
	}
	tok := challenge(ids[0])
	if code, _ := submit(ids[1], tok, "cf-good", "hc-good", user); code == 200 {
		t.Fatal("wrong case token accepted")
	}
	if code, _ := submit(ids[0], tok, "cf-good", "hc-good", other); code == 200 {
		t.Fatal("wrong user accepted")
	}
	if code, _ := submit(ids[0], tok, "cf-good", "", user); code != 400 {
		t.Fatalf("missing hCaptcha %d", code)
	}
	if code, _ := submit(ids[0], tok, "", "hc-good", user); code != 400 {
		t.Fatalf("missing Turnstile %d", code)
	}
	if code, _ := submit(ids[0], "12345678-1234-1234-1234-123456789012", "cf-good", "hc-good", user); code == 200 {
		t.Fatal("join challenge accepted for appeal")
	}
	if code, _ := submit(ids[0], tok, "cf-good", "bad", user); code != 400 {
		t.Fatalf("invalid captcha %d", code)
	}
	if code, _ := submit(ids[0], tok, "cf-good", "hc-good", user); code != 409 {
		t.Fatalf("replay after failed provider %d", code)
	}
	tok = challenge(ids[0])
	if code, _ := submit(ids[0], tok, "bad", "hc-good", user); code != 400 {
		t.Fatalf("invalid Turnstile %d", code)
	}
	tok = challenge(ids[0])
	if _, err := fx.pool.Exec(ctx, `UPDATE appeal_challenges SET expires_at=now()-interval '1 second' WHERE token=$1`, tok); err != nil {
		t.Fatal(err)
	}
	if code, _ := submit(ids[0], tok, "cf-good", "hc-good", user); code != 409 {
		t.Fatalf("expired %d", code)
	}
	tok = challenge(ids[0])
	if code, _ := call("submit", map[string]any{"init_data": user, "case_id": ids[0], "token": tok, "reason": "x", "reference_url": "javascript:alert(1)", "cf_response": "cf-good", "h_response": "hc-good"}); code != 400 {
		t.Fatalf("bad url %d", code)
	}
	if code, b := submit(ids[0], tok, "cf-good", "hc-good", user); code != 200 || b["appeal"] == nil {
		t.Fatalf("submit %d %+v", code, b)
	}
	if code, b := submit(ids[0], tok, "cf-good", "hc-good", user); code != 200 || b["appeal"] == nil {
		t.Fatalf("idempotent receipt %d %+v", code, b)
	}
	if code, b := call("case", map[string]any{"init_data": user, "case_id": ids[0]}); code != 200 || b["appeal"] == nil {
		t.Fatalf("receipt %d %+v", code, b)
	}
	a, b := challenge(ids[1]), challenge(ids[1])
	// A response already presented with the first case cannot be replayed on another challenge.
	if code, _ := call("submit", map[string]any{"init_data": user, "case_id": ids[1], "token": a, "reason": "replay", "cf_response": "cf-good" + tok, "h_response": "hc-good" + a}); code != 409 {
		t.Fatalf("Turnstile token replay: %d", code)
	}
	if code, _ := call("submit", map[string]any{"init_data": user, "case_id": ids[1], "token": a, "reason": "replay", "cf_response": "cf-good" + a, "h_response": "hc-good" + tok}); code != 409 {
		t.Fatalf("hCaptcha token replay: %d", code)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, token := range []string{a, b} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			code, _ := submit(ids[1], token, "cf-good", "hc-good", user)
			codes <- code
		}(token)
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("concurrent submission %d", code)
		}
	}
	var count int
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM appeals WHERE case_id=$1`, ids[1]).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count %d %v", count, err)
	}
	// The same challenge cannot run two provider checks concurrently.
	tok = challenge(ids[3])
	entered, release := make(chan struct{}), make(chan struct{})
	srv.appealVerifier = func(_ context.Context, _, _, _, _ string) error { close(entered); <-release; return nil }
	first := make(chan int, 1)
	go func() { code, _ := submit(ids[3], tok, "cf-good", "hc-good", user); first <- code }()
	<-entered
	if code, _ := submit(ids[3], tok, "cf-good", "hc-good", user); code != 409 {
		close(release)
		t.Fatalf("parallel replay %d", code)
	}
	close(release)
	if code := <-first; code != 200 {
		t.Fatalf("first submission %d", code)
	}
	for i := 0; i < 22; i++ {
		_, err := fx.queries.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: fmt.Sprintf("appeal-page:%d", i), SourceKind: "test", SourceID: fmt.Sprint(i), ChatID: -100, UserID: 111, Action: "warn", Result: "executed"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if code, page := call("list", map[string]any{"init_data": user, "offset": 0}); code != 200 || len(page["cases"].([]any)) != 20 || page["has_more"] != true {
		t.Fatalf("first page %d %+v", code, page)
	}
	if code, page := call("list", map[string]any{"init_data": user, "offset": 20}); code != 200 || len(page["cases"].([]any)) != 5 || page["has_more"] != false {
		t.Fatalf("second page %d %+v", code, page)
	}
}
