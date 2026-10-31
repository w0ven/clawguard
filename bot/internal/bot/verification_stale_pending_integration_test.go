//go:build integration

package bot

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
	"testing"
	"time"
)

func TestStaleVerificationCannotConsumeOrPunishNewPendingIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	chat := &tele.Chat{ID: -90001}
	user := &tele.User{ID: 101}
	add := func(method string) store.PendingVerification {
		p, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chat.ID, UserID: user.ID, Method: method, Payload: []byte(`{}`), ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := add("button")
	newTask := add("math")
	if newTask.ID == old.ID {
		t.Fatal("replacement has no new identity")
	}
	if err := s.completeVerification(ctx, chat, user, old, "admin_pass"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale approval: %v", err)
	}
	if err := s.failVerificationImmediately(ctx, chat, user, old, "ban", "verify_admin_reject"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale rejection: %v", err)
	}
	still, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID})
	if err != nil || still.ID != newTask.ID {
		t.Fatalf("new task changed: %+v %v", still, err)
	}
	if countString(tr.Methods(), "restrictChatMember") != 0 || countString(tr.Methods(), "kickChatMember") != 0 {
		t.Fatalf("stale task changed permissions: %v", tr.Methods())
	}
}

func TestExpiredVerificationCannotActAfterLockWaitIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 101}
	p, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chat.ID, UserID: user.ID, Method: "button", Payload: []byte(`{}`), ExpiresAt: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.completeVerification(ctx, chat, user, p, "admin_pass"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired pass: %v", err)
	}
	if err := s.failVerificationImmediately(ctx, chat, user, p, "ban", "verify_admin_reject"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired reject: %v", err)
	}
	still, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID})
	if err != nil || still.ID != p.ID {
		t.Fatalf("expired task lost: %+v %v", still, err)
	}
	if countString(tr.Methods(), "kickChatMember") != 0 || countString(tr.Methods(), "restrictChatMember") != 0 {
		t.Fatalf("expired task changed permissions: %v", tr.Methods())
	}
}

func TestObsoleteVerificationCardCallbacksCannotActOnNewTaskIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	chat := &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}
	user := &tele.User{ID: 101}
	currentID := int64(222)
	pending, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chat.ID, UserID: user.ID, Method: "math", Payload: []byte(`{"answer":3}`), JoinMessageID: &currentID, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	cb := func(sender *tele.User, data string) *tele.Callback {
		return &tele.Callback{ID: "callback", Sender: sender, Message: &tele.Message{ID: 111, Chat: chat}, Data: data}
	}
	if err = s.handleVerifyMath(s.bot.NewContext(tele.Update{Callback: cb(user, "101:5")})); err != nil {
		t.Fatal(err)
	}
	admin := &tele.User{ID: 102}
	if err = s.handleVerifyAdmin(s.bot.NewContext(tele.Update{Callback: cb(admin, "101:reject")})); err != nil {
		t.Fatal(err)
	}
	still, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID})
	if err != nil || still.ID != pending.ID {
		t.Fatalf("new task changed: %+v %v", still, err)
	}
	if countString(tr.Methods(), "kickChatMember") != 0 || countString(tr.Methods(), "restrictChatMember") != 0 {
		t.Fatalf("old callback caused punishment: %v", tr.Methods())
	}
}
func TestReplacementVerificationCardGetsNewMessageIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	chat := &tele.Chat{ID: -90001, Type: tele.ChatSuperGroup}
	user := &tele.User{ID: 101}
	oldID := int64(111)
	_, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chat.ID, UserID: user.ID, Method: "math", Payload: []byte(`{"answer":1}`), JoinMessageID: &oldID, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := s.sendOrEditVerificationText(ctx, chat, int(oldID), "new prompt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sent.ID == int(oldID) {
		t.Fatal("reused old card identity")
	}
	if err = s.storePendingVerification(ctx, chat.ID, user, "math", []byte(`{"answer":2}`), sent.ID, 60, "kick"); err != nil {
		t.Fatal(err)
	}
	pending, err := q.GetActivePendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID})
	if err != nil || pending.JoinMessageID == nil || *pending.JoinMessageID != int64(sent.ID) {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if countString(tr.Methods(), "editMessageText") != 0 || countString(tr.Methods(), "deleteMessage") != 1 {
		t.Fatalf("expected fresh card, then old card deletion: %v", tr.Methods())
	}
}

func TestLegacyVerificationCannotClearIndependentRestrictionIntegration(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		banned     bool
	}{{"independent ban", "restricted", true}, {"different member state", "member", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool, q, s, tr := settlementFixture(t)
			tr.memberRole = tc.role
			chat, user := &tele.Chat{ID: -90001}, &tele.User{ID: 101}
			if tc.banned {
				if _, err := pool.Exec(ctx, `INSERT INTO group_ban_records(chat_id,user_id,source) VALUES(-90001,101,'fixture')`); err != nil {
					t.Fatal(err)
				}
			}
			p, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chat.ID, UserID: user.ID, Method: "button", Payload: []byte(`{}`), ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.completeVerification(ctx, chat, user, p, "admin_pass"); !errors.Is(err, ErrDualCaptchaConflict) {
				t.Fatalf("expected manual review, got %v", err)
			}
			got, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID})
			if err != nil || got.ID != p.ID {
				t.Fatalf("pending changed: %+v %v", got, err)
			}
			if countString(tr.Methods(), "restrictChatMember") != 0 {
				t.Fatalf("independent restriction cleared: %v", tr.Methods())
			}
		})
	}
}
