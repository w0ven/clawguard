//go:build integration

package worker

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/pressly/goose/v3"
	"go.uber.org/zap"
)

type expiryWorkerTransport struct {
	mu    sync.Mutex
	kicks int
}

func (t *expiryWorkerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.telegram.org" {
		return nil, fmt.Errorf("unexpected network destination")
	}
	body := `{"ok":true,"result":true}`
	if strings.HasSuffix(r.URL.Path, "/getMe") {
		body = `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"fixture","username":"fixture_bot"}}`
	}
	if strings.HasSuffix(r.URL.Path, "/kickChatMember") {
		t.mu.Lock()
		t.kicks++
		t.mu.Unlock()
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
func (t *expiryWorkerTransport) count() int { t.mu.Lock(); defer t.mu.Unlock(); return t.kicks }

func TestVerificationExpiryWorkerSettlementIntegration(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	bootstrap, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("worker_settlement_%d", time.Now().UnixNano())
	if _, err = bootstrap.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = bootstrap.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		bootstrap.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := u.Query()
	params.Set("search_path", schema)
	u.RawQuery = params.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	_, file, _, _ := runtime.Caller(0)
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpContext(ctx, db, filepath.Join(filepath.Dir(file), "..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := store.New(pool)
	if _, err = pool.Exec(ctx, `WITH g AS (INSERT INTO groups(chat_id,title,type) VALUES(-99991,'fixture','supergroup')) INSERT INTO authorized_groups(chat_id,enabled) VALUES(-99991,true)`); err != nil {
		t.Fatal(err)
	}
	transport := &expiryWorkerTransport{}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), q, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	old, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -99991, UserID: 901, Method: "button", Payload: []byte(`{"fail_action":"ban"}`), ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION worker_fail_violation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER worker_fail_violation BEFORE INSERT ON violations FOR EACH ROW EXECUTE FUNCTION worker_fail_violation()`); err != nil {
		t.Fatal(err)
	}
	w := NewVerificationExpiry(zap.NewNop(), q, svc)
	w.processExpired(ctx)
	var results, settled, logs int
	if err = pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE settled),(SELECT count(*) FROM violations WHERE user_id=901) FROM verification_cleanup_settlements WHERE user_id=901`).Scan(&results, &settled, &logs); err != nil || results != 1 || settled != 0 || logs != 0 || transport.count() != 1 {
		t.Fatalf("initial action/results: %d %d %d calls=%d err=%v", results, settled, logs, transport.count(), err)
	}
	// Keep concurrent moderation recovery state for this exact group/user.
	var changedAt time.Time
	if err = pool.QueryRow(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score) VALUES(-99991,901,'banned',0) RETURNING status_changed_at`).Scan(&changedAt); err != nil {
		t.Fatal(err)
	}
	var decisionID int64
	if err = pool.QueryRow(ctx, `INSERT INTO ai_decisions(chat_id,user_id,message_id,model,prompt_version,verdict,confidence,category,action_taken) VALUES(-99991,901,1,'fixture','fixture','spam',1,'spam','mute') RETURNING id`).Scan(&decisionID); err != nil {
		t.Fatal(err)
	}
	if err = q.ReserveAIMute(ctx, -99991, 901, decisionID, time.Now().Add(time.Hour).Unix(), "fixture-owner"); err != nil {
		t.Fatal(err)
	}
	if ok, e := q.ActivateAIMute(ctx, -99991, 901, "fixture-owner"); e != nil || !ok {
		t.Fatalf("mute owner activation: %v %v", ok, e)
	}
	if err = q.ClaimTrustSync(ctx, -99991, 901, store.TrustSync{Token: "fixture-sync", Target: "trusted", Base: "banned", BaseChanged: changedAt, Action: "unban"}); err != nil {
		t.Fatal(err)
	}
	newer, err := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: -99991, UserID: 901, Method: "button", Payload: []byte(`{"new":true}`), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil || newer.ID == old.ID {
		t.Fatalf("replacement: old=%d new=%d err=%v", old.ID, newer.ID, err)
	}
	if _, err = q.UpsertGroupExemptUser(ctx, -99991, 901, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER worker_fail_violation ON violations;DROP FUNCTION worker_fail_violation(); UPDATE verification_cleanup_settlements SET next_attempt_at=now()-interval '1 second' WHERE user_id=901; UPDATE system_state SET frozen=true`); err != nil {
		t.Fatal(err)
	}
	w.processExpired(ctx)
	w.processExpired(ctx)
	current, err := q.GetPendingVerification(ctx, store.GetPendingVerificationParams{ChatID: -99991, UserID: 901})
	if err != nil || current.ID != newer.ID {
		t.Fatalf("new pending lost: %+v %v", current, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE settled),(SELECT count(*) FROM violations WHERE user_id=901) FROM verification_cleanup_settlements WHERE user_id=901`).Scan(&results, &settled, &logs); err != nil || results != 1 || settled != 1 || logs != 1 || transport.count() != 1 {
		t.Fatalf("retry action/results: %d %d %d calls=%d err=%v", results, settled, logs, transport.count(), err)
	}
	var muteState, syncState, status string
	if err = pool.QueryRow(ctx, `SELECT o.state,s.state,u.status FROM ai_mute_ownership o JOIN user_trust_sync s USING(chat_id,user_id) JOIN user_trust u USING(chat_id,user_id) WHERE o.chat_id=-99991 AND o.user_id=901`).Scan(&muteState, &syncState, &status); err != nil || muteState != "active" || syncState != "claimed" || status != "banned" {
		t.Fatalf("unrelated moderation state changed: mute=%q sync=%q trust=%q err=%v", muteState, syncState, status, err)
	}
}
