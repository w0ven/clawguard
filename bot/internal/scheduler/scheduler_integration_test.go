//go:build integration

package scheduler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

type schedulerTransport struct {
	sends   int
	timeout bool
	cancel  context.CancelFunc
}

func (tr *schedulerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "telegram.test" {
		return nil, fmt.Errorf("unexpected network host")
	}
	body := `{"ok":true,"result":2}`
	if strings.HasSuffix(r.URL.Path, "/sendMessage") {
		tr.sends++
		if tr.cancel != nil {
			tr.cancel()
		}
		if tr.timeout {
			return nil, context.DeadlineExceeded
		}
		body = `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestScheduledPermanentAndAmbiguousFailuresAreNotRetried(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, tc := range []struct{ timeout, cancel bool }{{false, false}, {true, false}, {true, true}} {
		runCtx, cancel := context.WithCancel(ctx)
		tr := &schedulerTransport{timeout: tc.timeout}
		if tc.cancel {
			tr.cancel = cancel
		}
		b, err := tele.NewBot(tele.Settings{Offline: true, Token: "fake", URL: "http://telegram.test", Client: &http.Client{Transport: tr}})
		if err != nil {
			t.Fatal(err)
		}
		q := store.New(pool)
		interval := int32(1)
		msg, err := q.CreateScheduledMessage(ctx, store.CreateScheduledMessageParams{ChatID: -100, Name: "scheduler integration", ScheduleType: "interval", IntervalMinutes: &interval, Timezone: "Asia/Shanghai", Content: "fixture", Enabled: true, Status: "active"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM scheduled_messages WHERE id=$1`, msg.ID) })
		s := New(zap.NewNop(), q, b, nil)
		started := time.Now()
		err = s.run(runCtx, msg.ID, false)
		cancel()
		if err == nil {
			t.Fatal("expected send failure")
		}
		if tr.sends != 1 || time.Since(started) > 5*time.Second {
			t.Fatalf("case=%+v sends=%d duration=%v", tc, tr.sends, time.Since(started))
		}
		saved, err := q.GetScheduledMessage(ctx, msg.ID)
		if err != nil || saved.Status != "active" || !saved.Enabled {
			t.Fatalf("case=%+v saved=%+v error=%v", tc, saved, err)
		}
		var state string
		if err := pool.QueryRow(ctx, `SELECT state FROM scheduled_dispatch_attempts WHERE scheduled_message_id=$1`, msg.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		// Telebot converts unrecognised Telegram 400 descriptions into a
		// generic error, so the safe classification remains uncertain.
		want := "uncertain"
		if state != want {
			t.Fatalf("case=%+v state=%s", tc, state)
		}
		stop := s.Stop()
		<-stop.Done()
	}
}

type successfulTransport struct {
	mu              sync.Mutex
	sends           int
	deletes         []string
	malformedDelete bool
}

func (tr *successfulTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "telegram.test" {
		return nil, fmt.Errorf("unexpected host")
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	body := `{"ok":true,"result":2}`
	if strings.HasSuffix(r.URL.Path, "/sendMessage") {
		tr.sends++
		body = `{"ok":true,"result":{"message_id":123,"chat":{"id":-100,"type":"supergroup"}}}`
	}
	if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
		data, _ := io.ReadAll(r.Body)
		tr.deletes = append(tr.deletes, string(data))
		body = `{"ok":true,"result":true}`
		if tr.malformedDelete {
			body = `broken-json`
		}
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestDispatchSlotRecoveryAndDelete(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	lock, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err = lock.Exec(ctx, `SELECT pg_advisory_lock(781555009)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = lock.Exec(ctx, `SELECT pg_advisory_unlock(781555009)`) }()
	q := store.New(pool)
	interval := int32(1)
	msg, err := q.CreateScheduledMessage(ctx, store.CreateScheduledMessageParams{ChatID: -100, Name: "dispatch regression", ScheduleType: "interval", IntervalMinutes: &interval, Timezone: "Asia/Shanghai", Content: "test", Enabled: true, Status: "active", AutoDeleteSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM scheduled_delete_jobs WHERE attempt_id IN (SELECT id FROM scheduled_dispatch_attempts WHERE scheduled_message_id=$1)`, msg.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM scheduled_dispatch_attempts WHERE scheduled_message_id=$1`, msg.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM scheduled_messages WHERE id=$1`, msg.ID)
	}()
	tr := &successfulTransport{}
	b, err := tele.NewBot(tele.Settings{Offline: true, Token: "fake", URL: "http://telegram.test", Client: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	makeScheduler := func() *Scheduler { return New(zap.NewNop(), q, b, nil) }
	s1, s2 := makeScheduler(), makeScheduler()
	// Reject only this message's settlement, simulating a database failure after Telegram accepts.
	function := fmt.Sprintf("test_dispatch_fail_%d", msg.ID)
	trigger := fmt.Sprintf("test_dispatch_trigger_%d", msg.ID)
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='sent' AND NEW.scheduled_message_id=%d THEN RAISE EXCEPTION 'injected settlement failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE UPDATE ON scheduled_dispatch_attempts FOR EACH ROW EXECUTE FUNCTION %s()`, function, msg.ID, trigger, function))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON scheduled_dispatch_attempts; DROP FUNCTION IF EXISTS %s()`, trigger, function))
	}()
	slot := time.Now().UTC().Truncate(time.Second)
	var wg sync.WaitGroup
	for _, s := range []*Scheduler{s1, s2} {
		wg.Add(1)
		go func(s *Scheduler) { defer wg.Done(); _ = s.runSlot(ctx, msg.ID, false, &slot) }(s)
	}
	wg.Wait()
	tr.mu.Lock()
	sends := tr.sends
	tr.mu.Unlock()
	if sends != 1 {
		t.Fatalf("concurrent sends=%d", sends)
	}
	var state string
	if err = pool.QueryRow(ctx, `SELECT state FROM scheduled_dispatch_attempts WHERE scheduled_message_id=$1 AND scheduled_at=$2`, msg.ID, slot).Scan(&state); err != nil || state != "dispatching" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	_, err = pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER %s ON scheduled_dispatch_attempts; DROP FUNCTION %s()`, trigger, function))
	if err != nil {
		t.Fatal(err)
	}
	// A new scheduler instance after restart cannot resend the same slot.
	s3 := makeScheduler()
	if err = s3.runSlot(ctx, msg.ID, false, &slot); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	sends = tr.sends
	tr.mu.Unlock()
	if sends != 1 {
		t.Fatalf("replayed sends=%d", sends)
	}
	future := slot.Add(time.Minute)
	if err = s3.runSlot(ctx, msg.ID, false, &future); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	sends = tr.sends
	tr.mu.Unlock()
	if sends != 2 {
		t.Fatalf("future sends=%d", sends)
	}
	// Cancellation leaves the job recoverable, then a new worker deletes the
	// exact confirmed chat/message after its deadline.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s3.claimDelete(canceled); err == nil {
		t.Fatal("cancelled claim succeeded")
	}
	time.Sleep(1100 * time.Millisecond)
	claimed, err := s3.claimDelete(ctx)
	if err != nil || !claimed {
		t.Fatalf("delete claimed=%v err=%v", claimed, err)
	}
	tr.mu.Lock()
	deletes := append([]string(nil), tr.deletes...)
	tr.mu.Unlock()
	if len(deletes) != 1 || !strings.Contains(deletes[0], `"chat_id":"-100"`) || !strings.Contains(deletes[0], `"message_id":"123"`) {
		t.Fatalf("delete requests=%v", deletes)
	}
	// Malformed Telegram responses are not proof of deletion; keep a retry.
	attempt, claimed, err := q.ClaimScheduledDispatch(ctx, msg.ID, msg.ChatID, nil)
	if err != nil || !claimed {
		t.Fatalf("manual claim: %v", err)
	}
	id := int64(456)
	if err = q.FinishScheduledDispatch(ctx, attempt, msg.ChatID, &id, 1, "sent", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE scheduled_delete_jobs SET delete_at=now()-interval '1 second' WHERE attempt_id=$1`, attempt); err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	tr.malformedDelete = true
	tr.mu.Unlock()
	if claimed, err = s3.claimDelete(ctx); err != nil || !claimed {
		t.Fatalf("malformed claim=%v err=%v", claimed, err)
	}
	if err = pool.QueryRow(ctx, `SELECT state FROM scheduled_delete_jobs WHERE attempt_id=$1`, attempt).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("malformed state=%s err=%v", state, err)
	}
	// A started scheduler must actually recover the persisted job, not just
	// expose a callable claim helper.
	tr.mu.Lock()
	tr.malformedDelete = false
	tr.mu.Unlock()
	if _, err = pool.Exec(ctx, `UPDATE scheduled_delete_jobs SET delete_at=now()-interval '1 second' WHERE attempt_id=$1`, attempt); err != nil {
		t.Fatal(err)
	}
	s3.Start()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err = pool.QueryRow(ctx, `SELECT state FROM scheduled_delete_jobs WHERE attempt_id=$1`, attempt).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "done" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if state != "done" {
		t.Fatalf("started worker did not finish job: %s", state)
	}
	for _, s := range []*Scheduler{s1, s2, s3} {
		<-s.Stop().Done()
	}
}
