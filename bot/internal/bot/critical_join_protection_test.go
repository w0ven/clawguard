package bot

import (
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestJoinedServiceMessageUserUsesJoinedMember(t *testing.T) {
	inviter := &tele.User{ID: 10}
	joined := &tele.User{ID: 20}
	msg := &tele.Message{Sender: inviter, UserJoined: joined}
	if got := joinedServiceMessageUser(msg); got == nil || got.ID != joined.ID {
		t.Fatalf("joinedServiceMessageUser() = %+v, want joined user %d", got, joined.ID)
	}
}

func TestJoinedServiceMessageUserSupportsMembersArray(t *testing.T) {
	msg := &tele.Message{Sender: &tele.User{ID: 10}, UsersJoined: []tele.User{{ID: 20}}}
	if got := joinedServiceMessageUser(msg); got == nil || got.ID != 20 {
		t.Fatalf("joinedServiceMessageUser() = %+v, want array member", got)
	}
}

func TestProcessUpdateReturnsHandlerErrorAndAllowsRetry(t *testing.T) {
	_, client := newJoinProtectionTestRedis(t)
	botClient, _ := newMockTelegramBot(t, "")
	svc := &Service{bot: botClient, redis: client, logger: zap.NewNop()}
	wantErr := errors.New("database unavailable")
	var calls atomic.Int64
	botClient.Handle(tele.OnText, func(c tele.Context) error {
		return svc.runHandler("test", c, func(tele.Context) error {
			if calls.Add(1) == 1 {
				return wantErr
			}
			return nil
		})
	})
	update := testTextUpdate(91001)
	if err := svc.ProcessUpdate(update); !errors.Is(err, wantErr) {
		t.Fatalf("first ProcessUpdate() error = %v, want %v", err, wantErr)
	}
	if err := svc.ProcessUpdate(update); err != nil {
		t.Fatalf("retry ProcessUpdate() error = %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("handler calls = %d, want 2", got)
	}
}

func TestProcessUpdateDeduplicatesCompletedUpdate(t *testing.T) {
	_, client := newJoinProtectionTestRedis(t)
	botClient, _ := newMockTelegramBot(t, "")
	svc := &Service{bot: botClient, redis: client, logger: zap.NewNop()}
	var calls atomic.Int64
	botClient.Handle(tele.OnText, func(c tele.Context) error {
		return svc.runHandler("test", c, func(tele.Context) error {
			calls.Add(1)
			return nil
		})
	})
	update := testTextUpdate(91002)
	if err := svc.ProcessUpdate(update); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProcessUpdate(update); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
}

func TestProcessUpdatePanicReleasesRedisClaim(t *testing.T) {
	server, client := newJoinProtectionTestRedis(t)
	svc := &Service{redis: client, logger: zap.NewNop()}
	update := testTextUpdate(91003)
	if err := svc.ProcessUpdate(update); err == nil {
		t.Fatal("ProcessUpdate() error = nil after panic")
	}
	if server.Exists("clawguard:telegram:update:91003") {
		t.Fatal("panicked update remained marked as processed")
	}
}

func TestTemporaryJoinProtectionActionsUseFixedDeadline(t *testing.T) {
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{bot: botClient, logger: zap.NewNop()}
	chat := &tele.Chat{ID: -100123}
	user := &tele.User{ID: 456}
	deadline := time.Now().Add(time.Hour).Truncate(time.Second)

	if err := svc.temporaryBanJoinFloodUser(chat, user, deadline); err != nil {
		t.Fatal(err)
	}
	assertTelegramRequestParam(t, transport, "kickChatMember", "until_date", strconv.FormatInt(deadline.Unix(), 10))
	assertTelegramRequestParam(t, transport, "kickChatMember", "revoke_messages", "false")

	if err := svc.temporaryRestrictJoinFloodUser(chat, user, deadline); err != nil {
		t.Fatal(err)
	}
	assertTelegramJSONScalar(t, transport, "restrictChatMember", "until_date", strconv.FormatInt(deadline.Unix(), 10))
}

func TestTemporaryJoinProtectionSkipsUnsafeNearExpiryDeadline(t *testing.T) {
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{bot: botClient, logger: zap.NewNop()}
	before := len(transport.Methods())
	deadline := time.Now().Add(20 * time.Second)
	if err := svc.temporaryBanJoinFloodUser(&tele.Chat{ID: -100123}, &tele.User{ID: 456}, deadline); err != nil {
		t.Fatal(err)
	}
	if err := svc.temporaryRestrictJoinFloodUser(&tele.Chat{ID: -100123}, &tele.User{ID: 456}, deadline); err != nil {
		t.Fatal(err)
	}
	if got := len(transport.Methods()); got != before {
		t.Fatalf("telegram calls = %d after near-expiry actions, want %d", got, before)
	}
}

func TestJoinProtectionSubjectSelectionExemptsTrustedMembers(t *testing.T) {
	trusted := joinProtectionSubjectForTrustLookup(store.UserTrust{Status: "trusted"}, nil)
	if !trusted.Trusted || trusted.Candidate || trusted.Action != "" {
		t.Fatalf("trusted member subject = %+v, want trusted bypass", trusted)
	}

	untrusted := joinProtectionSubjectForTrustLookup(store.UserTrust{Status: "new"}, nil)
	if !untrusted.Candidate || untrusted.Trusted || untrusted.Action != joinProtectionActionTemporaryRestrict {
		t.Fatalf("untrusted prior member subject = %+v, want non-destructive candidate", untrusted)
	}

	firstTime := joinProtectionSubjectForTrustLookup(store.UserTrust{}, pgx.ErrNoRows)
	if !firstTime.Candidate || firstTime.Trusted || firstTime.Action != joinProtectionActionTemporaryBan {
		t.Fatalf("first-time member subject = %+v, want temporary-ban candidate", firstTime)
	}

	lookupFailure := joinProtectionSubjectForTrustLookup(store.UserTrust{}, errors.New("database unavailable"))
	if lookupFailure.Candidate || lookupFailure.Trusted || lookupFailure.Action != "" {
		t.Fatalf("lookup failure subject = %+v, want flood-action bypass", lookupFailure)
	}
}

func TestTrustedMemberBypassesEnabledJoinProtection(t *testing.T) {
	policy := config.DefaultPolicy
	policy.JoinProtection.Enabled = true
	policy.Verify.Enabled = true
	db := newOtherBotMockDB(policy)
	db.trust = &store.UserTrust{
		ChatID:          -1001,
		UserID:          77,
		JoinedAt:        db.now.Add(-30 * 24 * time.Hour),
		UpdatedAt:       db.now,
		StatusChangedAt: db.now,
		Status:          "trusted",
		Score:           1,
	}
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	before := len(transport.Methods())

	err := svc.startVerification(
		&tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
		&tele.User{ID: 77, FirstName: "trusted member"},
		nil,
		"trusted-generation",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(transport.Methods()); got != before {
		t.Fatalf("telegram calls = %d after trusted bypass, want %d", got, before)
	}
	if db.trust == nil || db.trust.Status != "trusted" {
		t.Fatalf("trusted status changed after bypass: %+v", db.trust)
	}
	if svc.joinProtector != nil {
		if status := svc.joinProtector.Status(-1001, time.Now()); status.RecentJoins != 0 || status.State != "normal" {
			t.Fatalf("trusted member affected join protection state: %+v", status)
		}
	}
}

func testTextUpdate(id int) tele.Update {
	return tele.Update{
		ID: id,
		Message: &tele.Message{
			ID:     id,
			Text:   "hello",
			Chat:   &tele.Chat{ID: -1001, Type: tele.ChatSuperGroup},
			Sender: &tele.User{ID: 1},
		},
	}
}

func assertTelegramJSONScalar(t *testing.T, transport *telegramMockTransport, method, key, want string) {
	t.Helper()
	transport.mu.Lock()
	methods := append([]string(nil), transport.methods...)
	bodies := append([]string(nil), transport.bodies...)
	transport.mu.Unlock()
	bodyIndex := 0
	for _, gotMethod := range methods {
		body := ""
		if bodyIndex < len(bodies) {
			body = bodies[bodyIndex]
			bodyIndex++
		}
		if gotMethod != method {
			continue
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &values); err != nil {
			t.Fatalf("parse %s body: %v", method, err)
		}
		var got string
		if err := json.Unmarshal(values[key], &got); err != nil {
			t.Fatalf("parse %s %s: %v", method, key, err)
		}
		if got != want {
			t.Fatalf("%s %s = %q, want %q", method, key, got, want)
		}
		return
	}
	t.Fatalf("telegram method %s not called", method)
}
