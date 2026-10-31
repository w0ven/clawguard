//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDualCaptchaClaimUsesPendingInstanceAndLease(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := New(pool)
	chat, user := int64(-98765023), int64(98765024)
	defer pool.Exec(ctx, `DELETE FROM dual_captcha_claims WHERE pending_id IN (SELECT id FROM pending_verifications WHERE chat_id=$1 AND user_id=$2)`, chat, user)
	defer pool.Exec(ctx, `DELETE FROM pending_verifications WHERE chat_id=$1 AND user_id=$2`, chat, user)
	p, err := q.UpsertPendingVerification(ctx, UpsertPendingVerificationParams{ChatID: chat, UserID: user, Method: "turnstile_hcaptcha", Payload: []byte(`{"token":"dual-test-123"}`), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, token := range []string{"one", "two"} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, _, e := q.ClaimDualCaptcha(ctx, "dual-test-123", token)
			results <- e
		}(token)
	}
	wg.Wait()
	close(results)
	wins, busy := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrDualCaptchaBusy) {
			busy++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || busy != 1 {
		t.Fatalf("wins=%d busy=%d", wins, busy)
	}
	var claim string
	if err := pool.QueryRow(ctx, `SELECT token FROM dual_captcha_claims WHERE pending_id=$1`, p.ID).Scan(&claim); err != nil {
		t.Fatal(err)
	}
	if ok, err := q.UpdateDualCaptchaClaim(ctx, p.ID, claim, "validating", "verified"); !ok || err != nil {
		t.Fatalf("mark verified: %v", err)
	}
	if err := q.ReleaseDualCaptchaClaim(ctx, p.ID, claim); err != nil {
		t.Fatal(err)
	}
	_, state, err := q.ClaimDualCaptcha(ctx, "dual-test-123", "retry")
	if err != nil || state != "verified" {
		t.Fatalf("retry state=%q err=%v", state, err)
	}
	newPending, err := q.UpsertPendingVerification(ctx, UpsertPendingVerificationParams{ChatID: chat, UserID: user, Method: "turnstile_hcaptcha", Payload: []byte(`{"token":"dual-test-new"}`), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || p.ID == newPending.ID {
		t.Fatalf("new pending: %v", err)
	}
	if ok, err := q.UpdateDualCaptchaClaim(ctx, p.ID, "retry", "verified", "applying"); err != nil || ok {
		t.Fatalf("old claim advanced: %v", err)
	}
	if _, _, err := q.ClaimDualCaptcha(ctx, "dual-test-123", "old"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old token = %v", err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM dual_captcha_claims WHERE pending_id=$1`, p.ID)
}
