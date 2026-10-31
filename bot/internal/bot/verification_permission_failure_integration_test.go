//go:build integration

package bot

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

func TestVerificationPolicyRestrictionFailureDoesNotGrantAllIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, _ := settlementFixture(t)
	chat := &tele.Chat{ID: -90001}
	user := &tele.User{ID: 101}
	if _, err := pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score) VALUES(-90001,101,'new',0.5)`); err != nil {
		t.Fatal(err)
	}
	tr := &dualTelegram{role: "restricted", fail: true}
	b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Client: &http.Client{Transport: tr}, Synchronous: true})
	if err != nil {
		t.Fatal(err)
	}
	s.bot = b
	s.sender = b
	policy := testUngraduatedPolicy(true, true)
	s.policySnapshots.Store(chat.ID, policy)
	s.policySnapshotAt.Store(chat.ID, time.Now())
	pending, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chat.ID, UserID: user.ID, Method: "button", Payload: []byte(`{}`), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.completeVerification(ctx, chat, user, pending, "admin_pass"); err == nil {
		t.Fatal("failed restriction reported success")
	}
	if tr.count() != 1 {
		t.Fatalf("restrict count=%d, expected no full grant fallback", tr.count())
	}
	restored, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: chat.ID, UserID: user.ID})
	if err != nil || restored.UserID != user.ID {
		t.Fatalf("pending not restored: %+v %v", restored, err)
	}
}
