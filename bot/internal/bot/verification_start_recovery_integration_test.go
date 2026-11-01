//go:build integration

package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

type verificationStartFaultTransport struct {
	mu            sync.Mutex
	base          *telegramMockTransport
	restrictCalls int
	mode, role    string
	userID        int64
	afterDial     func()
	afterSuccess  func()
}

func (tr *verificationStartFaultTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	method := path.Base(req.URL.Path)
	if method == "restrictChatMember" {
		tr.mu.Lock()
		tr.restrictCalls++
		call, mode, hook := tr.restrictCalls, tr.mode, tr.afterDial
		tr.mu.Unlock()
		if ((mode == "dial" || mode == "dial-then-read") && call == 1) || mode == "dial-always" {
			if hook != nil {
				hook()
			}
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "i/o timeout", Name: "telegram.test", IsTimeout: true}}
		}
		if mode == "dial-slow" {
			time.Sleep(30 * time.Millisecond)
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded}
		}
		if mode == "read" || (mode == "dial-then-read" && call > 1) {
			return nil, &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}
		}
		if mode == "eof" {
			return nil, io.EOF
		}
		if tr.afterSuccess != nil {
			tr.afterSuccess()
		}
	}
	if method == "getChatMember" {
		role := tr.role
		if role == "" {
			role = "member"
		}
		body := fmt.Sprintf(`{"ok":true,"result":{"user":{"id":%d,"is_bot":false,"first_name":"test"},"status":%q,"is_member":true}}`, tr.userID, role)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	return tr.base.RoundTrip(req)
}
func (tr *verificationStartFaultTransport) calls() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.restrictCalls
}

func setupStartRecovery(t *testing.T, mode, role string, id int64) (*Service, *store.Queries, *verificationStartFaultTransport, *telegramMockTransport) {
	t.Helper()
	_, q, s, base := settlementFixture(t)
	p := keywordReplyPolicy("", 0)
	p.Verify.Enabled, p.Verify.Method, p.JoinProtection.Enabled = true, "button", false
	s.policySnapshots.Store(int64(-90001), p)
	s.policySnapshotAt.Store(int64(-90001), time.Now())
	tr := &verificationStartFaultTransport{base: base, mode: mode, role: role, userID: id}
	b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Offline: true, Client: &http.Client{Transport: tr, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		t.Fatal(err)
	}
	s.verificationStartBot = b
	return s, q, tr, base
}

func TestVerificationStartTypedDialRecoveryIntegration(t *testing.T) {
	ctx := context.Background()
	s, q, tr, _ := setupStartRecovery(t, "dial", "member", 201)
	chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 201, FirstName: "test"}
	if err := s.startVerificationAt(chat, user, nil, "u201", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if tr.calls() != 2 {
		t.Fatalf("restrict calls=%d", tr.calls())
	}
	state, result, err := q.CleanupDispatchState(ctx, membershipCleanupKey("verification_start", chat.ID, user.ID, "u201"))
	if err != nil || state != "completed" || result != "restricted" {
		t.Fatalf("dispatch %s %s %v", state, result, err)
	}
	pending, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID})
	if err != nil || pending.UserID != user.ID {
		t.Fatalf("pending=%+v %v", pending, err)
	}
}

func TestVerificationStartDialProtectsStateIntegration(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		newer      bool
	}{
		{"left", "left", false}, {"admin", "administrator", false}, {"independent restriction", "restricted", false}, {"new generation", "member", true}, {"new exemption", "member", false}, {"group ban record", "member", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, q, tr, _ := setupStartRecovery(t, "dial", tc.role, 202)
			chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 202, FirstName: "test"}
			if tc.name == "group ban record" {
				tr.afterDial = func() {
					if err := q.UpsertGroupBan(ctx, chat.ID, user.ID, nil, "manual", nil); err != nil {
						t.Error(err)
					}
				}
			}
			if tc.name == "new exemption" {
				tr.afterDial = func() {
					if _, err := q.UpsertGroupExemptUser(ctx, chat.ID, user.ID, "test", nil); err != nil {
						t.Error(err)
					}
				}
			}
			if tc.newer {
				tr.afterDial = func() {
					ok, err := s.claimVerificationMembershipSession(ctx, chat.ID, user.ID, "u203")
					if err != nil || !ok {
						t.Errorf("new session %v %v", ok, err)
					}
				}
			}
			if err := s.startVerificationAt(chat, user, nil, "u202", time.Time{}); err != nil {
				t.Fatal(err)
			}
			if tr.calls() != 1 {
				t.Fatalf("unexpected repeat: %d", tr.calls())
			}
			state, result, err := q.CleanupDispatchState(ctx, membershipCleanupKey("verification_start", chat.ID, user.ID, "u202"))
			if err != nil || state != "completed" || result != verificationStartNotSentResult {
				t.Fatalf("dispatch %s %s %v", state, result, err)
			}
			if _, err = q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID}); err == nil {
				t.Fatal("unexpected pending verification")
			}
			if tc.newer {
				current, err := s.claimVerificationMembershipSession(ctx, chat.ID, user.ID, "u202")
				if err != nil || current {
					t.Fatalf("stale session replaced newer: %v %v", current, err)
				}
			}
		})
	}
}

func TestVerificationStartUnknownNeverRetriesIntegration(t *testing.T) {
	for _, mode := range []string{"read", "eof", "dial-then-read"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, q, tr, base := setupStartRecovery(t, mode, "member", 204)
			chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 204}
			if err := s.startVerificationAt(chat, user, nil, "u204", time.Time{}); err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			if mode == "dial-then-read" {
				wantCalls = 2
			}
			if tr.calls() != wantCalls {
				t.Fatalf("calls=%d", tr.calls())
			}
			state, _, err := q.CleanupDispatchState(ctx, membershipCleanupKey("verification_start", chat.ID, user.ID, "u204"))
			if err != nil || state != "dispatched" {
				t.Fatalf("dispatch %s %v", state, err)
			}
			restarted := restartedCleanupService(ctx, s)
			restarted.verificationStartBot = s.verificationStartBot
			p := keywordReplyPolicy("", 0)
			p.Verify.Enabled, p.Verify.Method, p.JoinProtection.Enabled = true, "button", false
			restarted.policySnapshots.Store(chat.ID, p)
			if err := restarted.startVerificationAt(chat, user, nil, "u204", time.Time{}); err != nil {
				t.Fatal(err)
			}
			if tr.calls() != wantCalls || countString(base.Methods(), "sendMessage") != 1 {
				t.Fatalf("calls %d / %v", tr.calls(), base.Methods())
			}
		})
	}
}

func TestVerificationConfirmedButGenerationChangedNeedsReviewIntegration(t *testing.T) {
	ctx := context.Background()
	s, q, tr, _ := setupStartRecovery(t, "success", "member", 211)
	chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 211}
	tr.afterSuccess = func() {
		if ok, err := s.claimVerificationMembershipSession(ctx, chat.ID, user.ID, "u212"); err != nil || !ok {
			t.Errorf("new session: %v %v", ok, err)
		}
	}
	if err := s.startVerificationAt(chat, user, nil, "u211", time.Time{}); err != nil {
		t.Fatal(err)
	}
	key := membershipCleanupKey("verification_start", chat.ID, user.ID, "u211")
	state, result, err := q.CleanupDispatchState(ctx, key)
	if err != nil || state != "completed" || result != verificationStartOwnershipUnknownResult {
		t.Fatalf("state=%s result=%s err=%v", state, result, err)
	}
	if _, err := s.guardedCleanup(ctx, key, chat.ID, user.ID, "verification_start", func() (string, error) { t.Fatal("replayed"); return "", nil }); !errors.Is(err, errCleanupManualReview) {
		t.Fatalf("not blocked: %v", err)
	}
	if _, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID}); err == nil {
		t.Fatal("old generation incorrectly started a prompt")
	}
}

func TestNewGenerationCanStartAfterOldDialFailureIntegration(t *testing.T) {
	ctx := context.Background()
	s, q, tr, _ := setupStartRecovery(t, "dial", "member", 207)
	chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 207, FirstName: "test"}
	tr.afterDial = func() {
		if ok, err := s.claimVerificationMembershipSession(ctx, chat.ID, user.ID, "u208"); err != nil || !ok {
			t.Errorf("new session %v %v", ok, err)
		}
	}
	if err := s.startVerificationAt(chat, user, nil, "u207", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := s.startVerificationAt(chat, user, nil, "u208", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if tr.calls() != 2 {
		t.Fatalf("old/new restrictions: %d", tr.calls())
	}
	if _, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID}); err != nil {
		t.Fatalf("new generation missing pending: %v", err)
	}
	if err := s.startVerificationAt(chat, user, nil, "u207", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if tr.calls() != 2 {
		t.Fatalf("stale event replayed: %d", tr.calls())
	}
}

func TestVerificationStartStaleAndUnorderedGenerationIntegration(t *testing.T) {
	ctx := context.Background()
	s, q, tr, base := setupStartRecovery(t, "dial", "member", 206)
	_, redis := newJoinProtectionTestRedis(t)
	s.redis = redis
	chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 206}
	ok, err := s.claimVerificationMembershipSession(ctx, chat.ID, user.ID, "u210")
	if err != nil || !ok {
		t.Fatalf("new session %v %v", ok, err)
	}
	if err := s.startVerificationAt(chat, user, nil, "u209", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if tr.calls() != 0 {
		t.Fatalf("stale update restricted member")
	}
	if cur, err := s.membershipSessionCurrent(ctx, chat.ID, user.ID, "u210"); err != nil || !cur {
		t.Fatalf("new session overwritten: %v %v", cur, err)
	}
	if err := s.startVerificationAt(chat, user, nil, "t123-1", time.Time{}); err != nil {
		t.Fatal(err)
	}
	state, result, err := q.CleanupDispatchState(ctx, membershipCleanupKey("verification_start", chat.ID, user.ID, "t123-1"))
	if err != nil || state != "completed" || result != verificationStartNotSentResult || tr.calls() != 0 || countString(base.Methods(), "sendMessage") != 2 {
		t.Fatalf("unordered dispatch %s %s %v calls=%d messages=%v", state, result, err, tr.calls(), base.Methods())
	}
}

func TestVerificationStartDialTimeoutBoundedIntegration(t *testing.T) {
	ctx := context.Background()
	s, q, tr, _ := setupStartRecovery(t, "dial-slow", "member", 209)
	b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Offline: true, Client: &http.Client{Transport: tr, Timeout: 100 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		t.Fatal(err)
	}
	s.verificationStartBot = b
	chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 209}
	started := time.Now()
	if err := s.startVerificationAt(chat, user, nil, "u209", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("unbounded retries: %v", elapsed)
	}
	state, result, err := q.CleanupDispatchState(ctx, membershipCleanupKey("verification_start", chat.ID, user.ID, "u209"))
	if err != nil || state != "completed" || result != verificationStartNotSentResult || tr.calls() != 3 {
		t.Fatalf("state %s %s %v calls %d", state, result, err, tr.calls())
	}
}

func TestVerificationStartDialExhaustionIsTerminalIntegration(t *testing.T) {
	ctx := context.Background()
	s, q, tr, base := setupStartRecovery(t, "dial-always", "member", 205)
	chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 205}
	if err := s.startVerificationAt(chat, user, nil, "u205", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if tr.calls() != 3 {
		t.Fatalf("restrict calls=%d", tr.calls())
	}
	key := membershipCleanupKey("verification_start", chat.ID, user.ID, "u205")
	state, result, err := q.CleanupDispatchState(ctx, key)
	if err != nil || state != "completed" || result != verificationStartNotSentResult {
		t.Fatalf("dispatch %s %s %v", state, result, err)
	}
	if _, err := s.guardedCleanup(ctx, key, chat.ID, user.ID, "verification_start", func() (string, error) { t.Fatal("replayed"); return "", nil }); !errors.Is(err, errCleanupManualReview) || !errors.Is(err, errCleanupNotDispatched) {
		t.Fatalf("terminal result %v", err)
	}
	if err := s.startVerificationAt(chat, user, nil, "u205", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if tr.calls() != 3 || countString(base.Methods(), "sendMessage") != 1 {
		t.Fatalf("calls %d / %v", tr.calls(), base.Methods())
	}
}
