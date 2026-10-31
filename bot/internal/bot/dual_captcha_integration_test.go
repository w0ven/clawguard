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

	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

type dualTelegram struct {
	mu      sync.Mutex
	calls   int
	fail    bool
	role    string
	rights  *tele.Rights
	started chan struct{}
	release chan struct{}
}

func (t *dualTelegram) RoundTrip(r *http.Request) (*http.Response, error) {
	m := path.Base(r.URL.Path)
	t.mu.Lock()
	defer t.mu.Unlock()
	body := `{"ok":true,"result":true}`
	switch m {
	case "getMe":
		body = `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"fixture"}}`
	case "getChatMember":
		if t.rights != nil {
			raw, _ := json.Marshal(t.rights)
			body = fmt.Sprintf(`{"ok":true,"result":{"user":{"id":101,"first_name":"fixture"},"status":%q,"is_member":true,"until_date":0,%s}}`, t.role, strings.Trim(string(raw), "{}"))
		} else {
			body = fmt.Sprintf(`{"ok":true,"result":{"user":{"id":101,"first_name":"fixture"},"status":%q,"is_member":true,"until_date":0}}`, t.role)
		}
	case "restrictChatMember":
		t.calls++
		if t.fail {
			return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":400,"description":"fixture failure"}`))}, nil
		}
		if t.started != nil {
			started, release := t.started, t.release
			t.mu.Unlock()
			started <- struct{}{}
			<-release
			t.mu.Lock()
		}
		t.role = "member"
	case "sendMessage":
		body = `{"ok":true,"result":{"message_id":999,"chat":{"id":-90001,"type":"supergroup"}}}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
func (t *dualTelegram) count() int { t.mu.Lock(); defer t.mu.Unlock(); return t.calls }

func TestDualCaptchaIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, _ := settlementFixture(t)
	token := "12345678-1234-1234-1234-123456789012"
	s.cfg = config.Config{PublicBaseURL: "https://example.com", TurnstileSiteKey: "cf-site", TurnstileSecret: "cf-secret", HCaptchaSiteKey: "h-site", HCaptchaSecret: "h-secret"}
	tr := &dualTelegram{role: "restricted"}
	b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Client: &http.Client{Transport: tr}, Synchronous: true})
	if err != nil {
		t.Fatal(err)
	}
	s.bot = b
	s.sender = b
	if _, err = pool.Exec(ctx, `UPDATE groups SET config='{"verify":{"enabled":true,"method":"turnstile_hcaptcha","welcome_message":{"enabled":false}}}' WHERE chat_id=-90001`); err != nil {
		t.Fatal(err)
	}
	if err = s.rememberMembershipSession(ctx, -90001, 101, "generation-one"); err != nil {
		t.Fatal(err)
	}
	makePending := func() store.PendingVerification {
		t.Helper()
		p, e := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -90001, UserID: 101, Method: "turnstile_hcaptcha", Payload: []byte(fmt.Sprintf(`{"token":%q,"membership_generation":"generation-one"}`, token)), ExpiresAt: time.Now().Add(time.Minute)})
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	p := makePending()
	var providerMu sync.Mutex
	failH := true
	var started, release chan struct{}
	prev := dualCaptchaHTTPClient
	dualCaptchaHTTPClient = &http.Client{Timeout: time.Second, Transport: dualRoundTrip(func(r *http.Request) (*http.Response, error) {
		providerMu.Lock()
		h := failH
		ch := started
		gate := release
		providerMu.Unlock()
		if r.URL.String() == dualTurnstileURL && ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
			<-gate
		}
		body := fmt.Sprintf(`{"success":true,"hostname":"example.com","action":"clawguard_dual","cdata":%q}`, token)
		if r.URL.String() == dualHCaptchaURL {
			body = `{"success":true,"hostname":"example.com"}`
			if h {
				body = `{"success":false}`
			}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() { dualCaptchaHTTPClient = prev })
	if err = s.VerifyDualCaptcha(ctx, token, "cf", "h", ""); err == nil {
		t.Fatal("one provider failure passed")
	}
	if tr.count() != 0 {
		t.Fatal("Telegram called when hCaptcha failed")
	}
	if _, err = q.GetPendingVerificationByToken(ctx, token); err != pgx.ErrNoRows {
		t.Fatalf("old endpoint can see dual pending: %v", err)
	}
	providerMu.Lock()
	failH = false
	started = make(chan struct{}, 1)
	release = make(chan struct{})
	providerMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.VerifyDualCaptcha(ctx, token, "cf", "h", "") }()
	<-started
	if err = s.VerifyDualCaptcha(ctx, token, "cf", "h", ""); err != store.ErrDualCaptchaBusy {
		t.Fatalf("concurrent claim: %v", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatalf("both valid: %v", err)
	}
	if tr.count() != 1 {
		t.Fatalf("restore calls=%d", tr.count())
	}
	if _, err = q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: -90001, UserID: 101}); err != pgx.ErrNoRows {
		t.Fatalf("pending remains: %v", err)
	}
	if err = s.VerifyDualCaptcha(ctx, token, "cf", "h", ""); err != pgx.ErrNoRows {
		t.Fatalf("replay: %v", err)
	}
	providerMu.Lock()
	started = nil
	release = nil
	providerMu.Unlock()
	tr.mu.Lock()
	tr.role = "restricted"
	tr.fail = true
	tr.mu.Unlock()
	p = makePending()
	if err = s.VerifyDualCaptcha(ctx, token, "cf2", "h2", ""); err == nil {
		t.Fatal("Telegram failure reported success")
	}
	if tr.count() != 2 {
		t.Fatalf("first failure count=%d", tr.count())
	}
	if _, err = pool.Exec(ctx, `UPDATE pending_verifications SET expires_at=now()-interval '1 second', next_attempt_at=now()-interval '1 second' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.ClaimExpiredPendingVerifications(ctx, store.ClaimExpiredPendingVerificationsParams{LeaseSeconds: 60, LeaseOwner: "expiry", BatchLimit: 100})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("expiry claimed a passed challenge: %+v %v", jobs, err)
	}
	if _, err = q.GetActiveDualCaptcha(ctx, token); err != nil {
		t.Fatalf("passed challenge not retryable after deadline: %v", err)
	}
	tr.mu.Lock()
	tr.fail = false
	tr.mu.Unlock()
	if err = s.VerifyDualCaptcha(ctx, token, "cf3", "h3", ""); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if tr.count() != 3 {
		t.Fatalf("retry count=%d", tr.count())
	}
	if p.ID == 0 {
		t.Fatal("missing pending identity")
	}

	// A new independent group ban arriving during the external checks wins.
	tr.mu.Lock()
	tr.role = "restricted"
	tr.mu.Unlock()
	makePending()
	providerMu.Lock()
	started = make(chan struct{}, 1)
	release = make(chan struct{})
	providerMu.Unlock()
	done = make(chan error, 1)
	go func() { done <- s.VerifyDualCaptcha(ctx, token, "cf", "h", "") }()
	<-started
	if err = q.UpsertGroupBan(ctx, -90001, 101, nil, "manual", nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != ErrDualCaptchaConflict {
		t.Fatalf("new ban: %v", err)
	}
	if tr.count() != 3 {
		t.Fatalf("new ban was overridden, calls=%d", tr.count())
	}
	if err = q.DeleteGroupBan(ctx, -90001, 101); err != nil {
		t.Fatal(err)
	}
	if err = s.VerifyDualCaptcha(ctx, token, "cf", "h", ""); err != ErrDualCaptchaConflict {
		t.Fatalf("unban revived old claim: %v", err)
	}

	// Replacing the pending while providers are blocked must not consume or
	// restore permissions for the new membership instance.
	makePending()
	providerMu.Lock()
	started = make(chan struct{}, 1)
	release = make(chan struct{})
	providerMu.Unlock()
	done = make(chan error, 1)
	go func() { done <- s.VerifyDualCaptcha(ctx, token, "cf", "h", "") }()
	<-started
	newPending, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -90001, UserID: 101, Method: "turnstile_hcaptcha", Payload: []byte(`{"token":"other","membership_generation":"generation-two"}`), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != ErrDualCaptchaConflict {
		t.Fatalf("superseded pending: %v", err)
	}
	current, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: -90001, UserID: 101})
	if err != nil || current.ID != newPending.ID || tr.count() != 3 {
		t.Fatalf("new pending changed: %v", err)
	}

	// An unvalidated challenge expiring while a provider is slow cannot pass.
	p = makePending()
	providerMu.Lock()
	started = make(chan struct{}, 1)
	release = make(chan struct{})
	providerMu.Unlock()
	done = make(chan error, 1)
	go func() { done <- s.VerifyDualCaptcha(ctx, token, "cf", "h", "") }()
	<-started
	if _, err = pool.Exec(ctx, `UPDATE pending_verifications SET expires_at=now()-interval '1 second' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != ErrDualCaptchaConflict {
		t.Fatalf("expired while verifying: %v", err)
	}
	if tr.count() != 3 {
		t.Fatalf("expired verification restored permission")
	}

	// Telegram finished after an independent ban was written: do not claim
	// success or consume the pending; leave the conflict for manual recovery.
	makePending()
	providerMu.Lock()
	started, release = nil, nil
	providerMu.Unlock()
	tr.mu.Lock()
	tr.role = "restricted"
	tr.started, tr.release = make(chan struct{}, 1), make(chan struct{})
	tgStarted, tgRelease := tr.started, tr.release
	tr.mu.Unlock()
	done = make(chan error, 1)
	go func() { done <- s.VerifyDualCaptcha(ctx, token, "cf", "h", "") }()
	<-tgStarted
	if err = q.UpsertGroupBan(ctx, -90001, 101, nil, "manual", nil); err != nil {
		t.Fatal(err)
	}
	close(tgRelease)
	if err = <-done; err != ErrDualCaptchaConflict {
		t.Fatalf("ban during Telegram request: %v", err)
	}
	if _, err = q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: -90001, UserID: 101}); err != nil {
		t.Fatalf("conflict consumed pending: %v", err)
	}
	if err = s.VerifyDualCaptcha(ctx, token, "cf", "h", ""); err != ErrDualCaptchaConflict {
		t.Fatalf("ban retry did not remain conflicted: %v", err)
	}
	if tr.count() != 4 {
		t.Fatalf("conflict repeated Telegram action: %d", tr.count())
	}

	// Missing keys must be detected before the initial restriction; they must
	// not create a prompt-failure cleanup that punishes the joining member.
	tr.mu.Lock()
	tr.started, tr.release = nil, nil
	tr.mu.Unlock()
	policy := config.DefaultPolicy
	policy.Verify.Enabled = true
	policy.Verify.Method = "turnstile_hcaptcha"
	policy.JoinProtection.Enabled = false
	s.policySnapshots.Store(int64(-90001), policy)
	s.policySnapshotAt.Store(int64(-90001), time.Now())
	s.cfg.HCaptchaSecret = ""
	before := tr.count()
	if err = s.startVerification(&tele.Chat{ID: -90001}, &tele.User{ID: 303, FirstName: "fixture"}, nil, "generation-three"); err != nil {
		t.Fatalf("missing key join: %v", err)
	}
	if tr.count() != before {
		t.Fatalf("missing keys restricted the joiner")
	}
	if _, err = q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: -90001, UserID: 303}); err != pgx.ErrNoRows {
		t.Fatalf("missing keys created cleanup/pending: %v", err)
	}
}

func TestDualCaptchaSettlementRecoveryUngraduated(t *testing.T) {
	ctx := context.Background()
	pool, q, s, _ := settlementFixture(t)
	token := "12345678-1234-1234-1234-123456789012"
	s.cfg = config.Config{PublicBaseURL: "https://example.com", TurnstileSiteKey: "cf-site", TurnstileSecret: "cf-secret", HCaptchaSiteKey: "h-site", HCaptchaSecret: "h-secret"}
	tr := &dualTelegram{role: "restricted"}
	b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Client: &http.Client{Transport: tr}, Synchronous: true})
	if err != nil {
		t.Fatal(err)
	}
	s.bot = b
	s.sender = b
	_, err = pool.Exec(ctx, `UPDATE groups SET config='{"verify":{"enabled":true,"method":"turnstile_hcaptcha","welcome_message":{"enabled":false}},"filter":{"new_user":{"enabled":true,"no_media":true}}}' WHERE chat_id=-90001`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.UpsertUserTrust(ctx, store.UpsertUserTrustParams{ChatID: -90001, UserID: 101, JoinedAt: time.Now(), Status: "new", Score: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.rememberMembershipSession(ctx, -90001, 101, "generation-one"); err != nil {
		t.Fatal(err)
	}
	pending, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -90001, UserID: 101, Method: "turnstile_hcaptcha", Payload: []byte(fmt.Sprintf(`{"token":%q,"membership_generation":"generation-one"}`, token)), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	old := dualCaptchaHTTPClient
	dualCaptchaHTTPClient = &http.Client{Transport: dualRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"success":true,"hostname":"example.com"}`
		if r.URL.String() == dualTurnstileURL {
			body = fmt.Sprintf(`{"success":true,"hostname":"example.com","action":"clawguard_dual","cdata":%q}`, token)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() { dualCaptchaHTTPClient = old })
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_dual_delete() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture'; END$$; CREATE TRIGGER fail_dual_delete BEFORE DELETE ON pending_verifications FOR EACH ROW EXECUTE FUNCTION fail_dual_delete()`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.VerifyDualCaptcha(ctx, token, "cf", "h", ""); err == nil {
		t.Fatal("DB failure reported successful verification")
	}
	if tr.count() != 1 {
		t.Fatalf("first restore count %d", tr.count())
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER fail_dual_delete ON pending_verifications; DROP FUNCTION fail_dual_delete()`)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = pool.QueryRow(ctx, `SELECT target_rights FROM dual_captcha_claims WHERE pending_id=$1`, pending.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var target dualPermissionTarget
	if err = json.Unmarshal(raw, &target); err != nil || !target.Restricted {
		t.Fatalf("target %s: %v", raw, err)
	}
	tr.mu.Lock()
	tr.role = "restricted"
	tr.rights = &target.Rights
	tr.mu.Unlock()
	if err = s.VerifyDualCaptcha(ctx, token, "cf2", "h2", ""); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if tr.count() != 1 {
		t.Fatalf("replayed successful Telegram action: %d", tr.count())
	}
	if _, err = q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: -90001, UserID: 101}); err != pgx.ErrNoRows {
		t.Fatalf("pending: %v", err)
	}
}
