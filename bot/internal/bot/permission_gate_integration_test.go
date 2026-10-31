//go:build integration

package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

func TestTurnstileWaitsForModerationGateIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	tr.memberRole = "restricted"
	s.cfg.TurnstileSecret = "fixture-secret"
	verified := make(chan struct{}, 4)
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verified <- struct{}{}
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer cf.Close()
	restoreTurnstileSiteverify(t, cf.URL, cf.Client())
	const token = "fixture-turnstile-token"
	pending, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -90001, UserID: 101, Method: "turnstile", Payload: []byte(fmt.Sprintf(`{"token":%q}`, token)), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.AcquireModerationReview(ctx, -90001, 101)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.VerifyTurnstileToken(ctx, token, "response", "") }()
	select {
	case <-verified:
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("provider was not called")
	}
	select {
	case err := <-done:
		release()
		t.Fatalf("verification bypassed review gate: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	if got := countString(tr.Methods(), "restrictChatMember"); got != 0 {
		release()
		t.Fatalf("permission changed while review held: %d", got)
	}
	still, err := q.GetPendingVerificationByToken(ctx, token)
	if err != nil || still.ID != pending.ID {
		release()
		t.Fatalf("pending consumed while locked: %+v %v", still, err)
	}
	// Another subject can restore permissions while this subject is waiting.
	trust := store.UserTrust{ChatID: -90001, UserID: 102, Status: "trusted"}
	otherDone := make(chan error, 1)
	go func() { otherDone <- s.RestoreTrustedChatPermissions(ctx, trust) }()
	select {
	case err := <-otherDone:
		if err != nil {
			release()
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("other user blocked")
	}
	before := countString(tr.Methods(), "restrictChatMember")
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("turnstile failed to resume")
	}
	if got := countString(tr.Methods(), "restrictChatMember"); got != before+1 {
		t.Fatalf("expected one permission restoration: %d to %d", before, got)
	}
	if _, err = q.GetPendingVerificationByToken(ctx, token); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("pending not consumed: %v", err)
	}
}

func TestTurnstileWaitCancellationAndReplacementIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	s.cfg.TurnstileSecret = "fixture-secret"
	verified := make(chan struct{}, 4)
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verified <- struct{}{}
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer cf.Close()
	restoreTurnstileSiteverify(t, cf.URL, cf.Client())
	const token = "fixture-old"
	p, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -90001, UserID: 101, Method: "turnstile", Payload: []byte(fmt.Sprintf(`{"token":%q}`, token)), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.AcquireModerationReview(ctx, -90001, 101)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.VerifyTurnstileToken(cctx, token, "response", "") }()
	<-verified
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		release()
		t.Fatalf("cancel err: %v", err)
	}
	still, err := q.GetPendingVerificationByToken(ctx, token)
	if err != nil || still.ID != p.ID {
		release()
		t.Fatalf("cancel consumed task: %v", err)
	}
	go func() { done <- s.VerifyTurnstileToken(ctx, token, "response", "") }()
	<-verified
	_, err = q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -90001, UserID: 101, Method: "turnstile", Payload: []byte(`{"token":"fixture-new"}`), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		release()
		t.Fatal(err)
	}
	release()
	if err = <-done; !errors.Is(err, ErrTurnstileTokenNotFound) {
		t.Fatalf("replacement err=%v", err)
	}
	if got := countString(tr.Methods(), "restrictChatMember"); got != 0 {
		t.Fatalf("stale task altered permissions: %d", got)
	}
	if _, err = q.GetPendingVerificationByToken(ctx, "fixture-new"); err != nil {
		t.Fatalf("new task lost: %v", err)
	}
}

func TestTrustAndUngraduatedPermissionsRespectGateIntegration(t *testing.T) {
	ctx := context.Background()
	_, _, s, tr := settlementFixture(t)
	chat := &tele.Chat{ID: -90001}
	user := &tele.User{ID: 101}
	trust := store.UserTrust{ChatID: chat.ID, UserID: user.ID, Status: "trusted"}
	release, err := s.AcquireModerationReview(ctx, chat.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.RestoreTrustedChatPermissions(ctx, trust) }()
	select {
	case e := <-done:
		release()
		t.Fatalf("trust sync bypassed gate: %v", e)
	case <-time.After(75 * time.Millisecond):
	}
	if countString(tr.Methods(), "restrictChatMember") != 0 {
		release()
		t.Fatal("trust sync wrote before gate")
	}
	// The explicitly locked API is non-reentrant and intended for callers that already hold the gate.
	lockedDone := make(chan error, 1)
	go func() { lockedDone <- s.RestoreTrustedChatPermissionsLocked(ctx, trust) }()
	select {
	case e := <-lockedDone:
		if e != nil {
			release()
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("locked helper deadlocked")
	}
	release()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("trust sync did not resume")
	}
	release, err = s.AcquireModerationReview(ctx, chat.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancelDone := make(chan error, 1)
	beforeCancel := countString(tr.Methods(), "restrictChatMember")
	go func() { cancelDone <- s.RestoreTrustedChatPermissions(cancelCtx, trust) }()
	cancel()
	select {
	case err := <-cancelDone:
		if !errors.Is(err, context.Canceled) {
			release()
			t.Fatalf("trust cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("trust cancellation did not unblock")
	}
	if countString(tr.Methods(), "restrictChatMember") != beforeCancel {
		release()
		t.Fatal("cancelled trust sync wrote permissions")
	}
	policy := keywordReplyPolicy("", 0)
	policy.Filter.NewUser.Enabled = true
	policy.Filter.NewUser.NoMedia = true
	go func() { done <- s.restrictUngraduatedPermissions(ctx, chat, user, policy, "fixture") }()
	select {
	case e := <-done:
		release()
		t.Fatalf("ungraduated restriction bypassed gate: %v", e)
	case <-time.After(75 * time.Millisecond):
	}
	release()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restriction did not resume")
	}
}

func TestAlreadyHeldVerificationAndAISyncDoNotReenterIntegration(t *testing.T) {
	ctx := context.Background()
	_, q, s, tr := settlementFixture(t)
	tr.memberRole = "restricted"
	chat := &tele.Chat{ID: -90001}
	user := &tele.User{ID: 101}
	pending, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chat.ID, UserID: user.ID, Method: "button", Payload: []byte(`{}`), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.completeVerification(ctx, chat, user, pending) }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("completeVerification reentered subject gate")
	}
	release, err := s.AcquireModerationReview(ctx, chat.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	heldCtx := context.WithValue(ctx, moderationHeldKey{}, moderationKey{chat.ID, user.ID})
	policy := keywordReplyPolicy("", 0)
	policy.Filter.NewUser.Enabled = true
	policy.Filter.NewUser.NoMedia = true
	completed := make(chan struct{})
	before := countString(tr.Methods(), "restrictChatMember")
	go func() {
		s.applyUngraduatedPermissionRestriction(heldCtx, chat, user, policy, store.UserTrust{ChatID: chat.ID, UserID: user.ID, Status: "new"}, "fixture")
		s.restoreTrustedUserPermissions(heldCtx, store.UserTrust{ChatID: chat.ID, UserID: user.ID, Status: "trusted"}, "fixture")
		close(completed)
	}()
	select {
	case <-completed:
	case <-time.After(3 * time.Second):
		t.Fatal("held-context sync reentered subject gate")
	}
	if after := countString(tr.Methods(), "restrictChatMember"); after != before+2 {
		t.Fatalf("sync count %d before %d", after, before)
	}
}

func TestAIGraduationTrustAndPermissionSerializedIntegration(t *testing.T) {
	ctx := context.Background()
	pool, q, s, tr := settlementFixture(t)
	if _, err := pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score,messages_clean) VALUES(-90001,101,'new',0.8,3)`); err != nil {
		t.Fatal(err)
	}
	trust, err := q.GetUserTrust(ctx, -90001, 101)
	if err != nil {
		t.Fatal(err)
	}
	policy := keywordReplyPolicy("", 0).AI
	policy.GraduateAfterMessages = 1
	release, err := s.AcquireModerationReview(ctx, -90001, 101)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.maybeGraduateUser(ctx, trust, policy) }()
	select {
	case e := <-done:
		release()
		t.Fatalf("graduation bypassed gate: %v", e)
	case <-time.After(75 * time.Millisecond):
	}
	current, err := q.GetUserTrust(ctx, -90001, 101)
	if err != nil || current.Status != "new" {
		release()
		t.Fatalf("trust changed before gate: %s %v", current.Status, err)
	}
	if countString(tr.Methods(), "restrictChatMember") != 0 {
		release()
		t.Fatal("permissions changed before gate")
	}
	release()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("graduation failed to resume")
	}
	current, err = q.GetUserTrust(ctx, -90001, 101)
	if err != nil || current.Status != "trusted" {
		t.Fatalf("graduation failed: %s %v", current.Status, err)
	}
	if countString(tr.Methods(), "restrictChatMember") != 1 {
		t.Fatal("permissions not restored")
	}
}
