//go:build integration

package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

type miniCapture struct {
	base             *dualTelegram
	mu               sync.Mutex
	sent             []string
	failNotification bool
}

func (m *miniCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	m.mu.Lock()
	blocked := m.failNotification && r.URL.Host == "api.telegram.org"
	m.mu.Unlock()
	if blocked {
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false}`))}, nil
	}
	if path.Base(r.URL.Path) == "sendMessage" {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		m.mu.Lock()
		m.sent = append(m.sent, string(raw))
		m.mu.Unlock()
	}
	return m.base.RoundTrip(r)
}
func (m *miniCapture) messages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.sent...)
}

func TestMiniAppVerificationStartAndSettlement(t *testing.T) {
	ctx := context.Background()
	pool, q, s, _ := settlementFixture(t)
	s.lifecycleCtx, s.lifecycleCancel = context.WithCancel(context.Background())
	s.cfg = config.Config{BotToken: "123:TEST", BotUsername: "fixture_bot", PublicBaseURL: "https://example.com", TurnstileSiteKey: "cf", TurnstileSecret: "cf-secret", HCaptchaSiteKey: "hc", HCaptchaSecret: "hc-secret"}
	tr := &miniCapture{base: &dualTelegram{role: "restricted"}}
	b, err := tele.NewBot(tele.Settings{Token: s.cfg.BotToken, URL: "http://telegram.test", Client: &http.Client{Transport: tr}, Synchronous: true})
	if err != nil {
		t.Fatal(err)
	}
	s.bot = b
	s.sender = b
	b.Handle("/start", func(c tele.Context) error { return s.runCommandHandler("start", c, s.handleStartCommand) })
	oldTransport := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { s.Stop(); http.DefaultTransport = oldTransport })
	_, err = pool.Exec(ctx, `UPDATE groups SET config='{"verify":{"enabled":true,"method":"turnstile_hcaptcha","welcome_message":{"enabled":false}}}' WHERE chat_id=-90001`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.rememberMembershipSession(ctx, -90001, 101, "generation"); err != nil {
		t.Fatal(err)
	}
	policy := config.DefaultPolicy
	policy.Verify.Enabled = true
	policy.Verify.Method = "turnstile_hcaptcha"
	policy.Verify.TimeoutSeconds = 180
	chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 101, FirstName: "Test"}
	joinStarted := time.Now().Add(-30 * time.Second)
	promptCtx := context.WithValue(ctx, dualCaptchaGenerationKey{}, "generation")
	promptCtx = context.WithValue(promptCtx, dualCaptchaStartTimeKey{}, joinStarted)
	if err = s.startDualCaptchaVerification(promptCtx, chat, user, policy); err != nil {
		t.Fatal(err)
	}
	pending, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if delta := pending.ExpiresAt.Sub(joinStarted.Add(180 * time.Second)); delta < -time.Millisecond || delta > time.Millisecond {
		t.Fatalf("deadline reset: %v", delta)
	}
	var payload dualCaptchaPayload
	if json.Unmarshal(pending.Payload, &payload) != nil || !payload.MiniApp {
		t.Fatal("missing miniapp ownership")
	}
	sent := tr.messages()
	if len(sent) == 0 || !strings.Contains(sent[0], "t.me/fixture_bot?start=v_") || strings.Contains(sent[0], "example.com") {
		t.Fatalf("group prompt %v", sent)
	}
	sendStart := func(id int64) {
		update := tele.Update{ID: int(id + 1000), Message: &tele.Message{Chat: &tele.Chat{ID: id, Type: tele.ChatPrivate}, Sender: &tele.User{ID: id, FirstName: "Test"}, Text: "/start v_" + payload.Token}}
		if err := s.ProcessUpdate(update); err != nil {
			t.Fatal(err)
		}
	}
	sendStart(102)
	sent = tr.messages()
	if strings.Contains(sent[len(sent)-1], "web_app") {
		t.Fatal("wrong user got Mini App")
	}
	sendStart(101)
	sent = tr.messages()
	if !strings.Contains(sent[len(sent)-1], "web_app") || !strings.Contains(sent[len(sent)-1], "/verify/mini#") {
		t.Fatalf("private card: %s", sent[len(sent)-1])
	}
	var starts int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM telegram_private_sessions WHERE user_id=101`).Scan(&starts); err != nil || starts != 1 {
		t.Fatalf("start record %d %v", starts, err)
	}
	oldProvider := dualCaptchaHTTPClient
	dualCaptchaHTTPClient = &http.Client{Transport: dualRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"success":true,"hostname":"example.com"}`
		if r.URL.String() == dualTurnstileURL {
			body = fmt.Sprintf(`{"success":true,"hostname":"example.com","action":"clawguard_dual","cdata":%q}`, payload.Token)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() { dualCaptchaHTTPClient = oldProvider })
	if err = s.VerifyDualCaptcha(ctx, payload.Token, "cf", "hc", ""); err == nil {
		t.Fatal("legacy bypass succeeded")
	}
	if err = s.VerifyMiniAppDualCaptcha(ctx, payload.Token, "cf", "hc", "", 102); err == nil {
		t.Fatal("wrong identity succeeded")
	}
	if err = s.VerifyMiniAppDualCaptcha(ctx, payload.Token, "cf", "hc", "", 101); err != nil {
		t.Fatalf("verification: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if pool.QueryRow(ctx, `SELECT status FROM miniapp_verification_notifications WHERE pending_id=$1`, pending.ID).Scan(&state) == nil && state == "sent" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tr.base.count() != 1 {
		t.Fatalf("restore calls %d", tr.base.count())
	}
	var status string
	if err = pool.QueryRow(ctx, `SELECT status FROM miniapp_verification_notifications WHERE pending_id=$1`, pending.ID).Scan(&status); err != nil || status != "sent" {
		t.Fatalf("notification %s %v", status, err)
	}
	if err = s.VerifyMiniAppDualCaptcha(ctx, payload.Token, "cf", "hc", "", 101); err == nil {
		t.Fatal("replay succeeded")
	}
	tr.mu.Lock()
	tr.failNotification = true
	tr.mu.Unlock()
	tr.base.mu.Lock()
	tr.base.role = "restricted"
	tr.base.mu.Unlock()
	if err = s.startDualCaptchaVerification(context.WithValue(ctx, dualCaptchaGenerationKey{}, "generation"), chat, user, policy); err != nil {
		t.Fatal(err)
	}
	pending, err = q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(pending.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if err = s.VerifyMiniAppDualCaptcha(ctx, payload.Token, "cf", "hc", "", 101); err != nil {
		t.Fatalf("blocked notification must not fail verification: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pool.QueryRow(ctx, `SELECT status FROM miniapp_verification_notifications WHERE pending_id=$1`, pending.ID).Scan(&status) == nil && status == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "failed" {
		t.Fatalf("notification status %q", status)
	}
	if tr.base.count() != 2 {
		t.Fatalf("unexpected restore count %d", tr.base.count())
	}
}
