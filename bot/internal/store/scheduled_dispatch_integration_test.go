//go:build integration

package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestScheduledDispatchLeaseAndBoundedDelete(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// This test and the scheduler integration test share one CI database and
	// both claim global due jobs. Serialize just these fixtures.
	lock, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err = lock.Exec(ctx, `SELECT pg_advisory_lock(781555009)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = lock.Exec(ctx, `SELECT pg_advisory_unlock(781555009)`) }()
	q := New(pool)
	interval := int32(1)
	msg, err := q.CreateScheduledMessage(ctx, CreateScheduledMessageParams{ChatID: -87654, Name: "lease test", ScheduleType: "interval", IntervalMinutes: &interval, Timezone: "Asia/Shanghai", Content: "x", Enabled: true, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM scheduled_delete_jobs WHERE attempt_id IN (SELECT id FROM scheduled_dispatch_attempts WHERE scheduled_message_id=$1)`, msg.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM scheduled_dispatch_attempts WHERE scheduled_message_id=$1`, msg.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM scheduled_messages WHERE id=$1`, msg.ID)
	}()
	slot := time.Now().UTC().Truncate(time.Second)
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	var attempt int64
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, ok, e := q.ClaimScheduledDispatch(ctx, msg.ID, msg.ChatID, &slot)
			if e != nil {
				t.Error(e)
				return
			}
			if ok {
				mu.Lock()
				winners++
				attempt = id
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	id := int64(5678)
	if err = q.FinishScheduledDispatch(ctx, attempt, msg.ChatID, &id, 1, "sent", ""); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE scheduled_delete_jobs SET delete_at=now()-interval '1 second' WHERE attempt_id=$1`, attempt)
	if err != nil {
		t.Fatal(err)
	}
	// Cancellation before the external call releases without using retry budget.
	cancelled, err := q.ClaimScheduledDelete(ctx, "cancelled")
	if err != nil || cancelled == nil {
		t.Fatalf("cancelled claim: %+v %v", cancelled, err)
	}
	if err = q.ReleaseScheduledDeleteBeforeCall(ctx, *cancelled); err != nil {
		t.Fatal(err)
	}
	// Claimed job survives restart; stale token cannot finish another owner's lease.
	first, err := q.ClaimScheduledDelete(ctx, "first")
	if err != nil || first == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if first.ChatID != msg.ChatID || first.MessageID != id {
		t.Fatalf("wrong target %+v", first)
	}
	if second, e := q.ClaimScheduledDelete(ctx, "second"); e != nil || second != nil {
		t.Fatalf("premature lease %+v %v", second, e)
	}
	_, err = pool.Exec(ctx, `UPDATE scheduled_delete_jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.ClaimScheduledDelete(ctx, "second")
	if err != nil || second == nil {
		t.Fatalf("recovery %+v %v", second, err)
	}
	if err = q.SettleScheduledDelete(ctx, *first, true, false, ""); err == nil {
		t.Fatal("stale owner settled")
	}
	if err = q.SettleScheduledDelete(ctx, *second, false, false, "temporary"); err != nil {
		t.Fatal(err)
	}
	for i := 3; i <= 5; i++ {
		_, err = pool.Exec(ctx, `UPDATE scheduled_delete_jobs SET delete_at=now()-interval '1 second' WHERE id=$1`, first.ID)
		if err != nil {
			t.Fatal(err)
		}
		j, e := q.ClaimScheduledDelete(ctx, fmt.Sprint(i))
		if e != nil || j == nil || int(j.Attempts) != i {
			t.Fatalf("attempt %d: %+v %v", i, j, e)
		}
		if e = q.SettleScheduledDelete(ctx, *j, false, false, "temporary"); e != nil {
			t.Fatal(e)
		}
	}
	var state string
	var attempts int
	if err = pool.QueryRow(ctx, `SELECT state,attempts FROM scheduled_delete_jobs WHERE id=$1`, first.ID).Scan(&state, &attempts); err != nil || state != "failed" || attempts != 5 {
		t.Fatalf("state=%s attempts=%d err=%v", state, attempts, err)
	}
}
