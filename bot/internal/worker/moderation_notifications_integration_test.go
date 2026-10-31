//go:build integration

package worker

import (
	"context"
	"encoding/json"
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
)

type notificationTransport struct {
	mu    sync.Mutex
	calls map[int64]int
}

func (t *notificationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.telegram.org" {
		return nil, fmt.Errorf("unexpected destination")
	}
	var v struct {
		ChatID int64 `json:"chat_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&v)
	t.mu.Lock()
	t.calls[v.ChatID]++
	t.mu.Unlock()
	code, body := 200, `{"ok":true}`
	switch v.ChatID {
	case 7002:
		code, body = 403, `{"ok":false,"error_code":403}`
	case 7003:
		code, body = 500, `{"ok":false,"error_code":500}`
	case 7004:
		code, body = 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":120}}`
	case 7005:
		return nil, context.DeadlineExceeded
	}
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
func (t *notificationTransport) count(id int64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls[id]
}

func TestModerationNotificationsRecoveryAndFairness(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := store.New(pool)
	prefix := fmt.Sprintf("fixture-%d", time.Now().UnixNano())
	defer pool.Exec(ctx, `DELETE FROM moderation_notifications WHERE case_id IN (SELECT id FROM moderation_cases WHERE source_key LIKE $1); DELETE FROM moderation_cases WHERE source_key LIKE $1`, prefix+"%")
	// Users who never started cannot occupy a delivery queue, even in bulk.
	for i := int64(0); i < 200; i++ {
		_, err = q.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: fmt.Sprintf("%s-missing-%d", prefix, i), SourceKind: "test", SourceID: fmt.Sprint(i), ChatID: -42, UserID: 1000000 + i, Action: "delete", Result: "executed"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM moderation_notifications n JOIN moderation_cases c ON c.id=n.case_id WHERE c.source_key LIKE $1 AND n.state='unreachable'`, prefix+"%").Scan(&count); err != nil || count != 200 {
		t.Fatalf("unreachable %d %v", count, err)
	}
	for _, id := range []int64{7001, 7002, 7003, 7004, 7005} {
		if err = q.RecordPrivateStart(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	defer pool.Exec(ctx, `DELETE FROM telegram_private_sessions WHERE user_id BETWEEN 7001 AND 7005`)
	for _, id := range []int64{7001, 7002, 7003, 7004, 7005} {
		v := store.ModerationCaseInput{SourceKey: fmt.Sprintf("%s-%d", prefix, id), SourceKind: "test", SourceID: fmt.Sprint(id), ChatID: -42, UserID: id, Action: "delete", Result: "executed", Summary: "call 13800138000 and email user@example.com"}
		a, e := q.RecordModerationCase(ctx, v)
		if e != nil || a == 0 {
			t.Fatalf("record %d %v", a, e)
		}
		b, e := q.RecordModerationCase(ctx, v)
		if e != nil || b != 0 {
			t.Fatalf("duplicate %d %v", b, e)
		}
	}
	tr := &notificationTransport{calls: map[int64]int{}}
	w := NewModerationNotifications(zap.NewNop(), q, "123:fixture")
	w.client = &http.Client{Transport: tr}
	w.process(ctx)
	state := func(id int64) string {
		var s string
		err := pool.QueryRow(ctx, `SELECT n.state FROM moderation_notifications n JOIN moderation_cases c ON c.id=n.case_id WHERE c.source_key=$1`, fmt.Sprintf("%s-%d", prefix, id)).Scan(&s)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if state(7001) != "sent" || state(7002) != "unreachable" || state(7003) != "sending" || state(7004) != "pending" || state(7005) != "sending" {
		t.Fatalf("states: %s %s %s %s %s", state(7001), state(7002), state(7003), state(7004), state(7005))
	}
	// Simulate lease expiry/restart; existing recovery marks the unknown
	// outcomes terminal without replaying the Telegram request.
	if _, err = pool.Exec(ctx, `UPDATE moderation_notifications n SET lease_until=now()-interval '1 second' FROM moderation_cases c WHERE n.case_id=c.id AND c.source_key IN ($1,$2)`, fmt.Sprintf("%s-%d", prefix, 7003), fmt.Sprintf("%s-%d", prefix, 7005)); err != nil {
		t.Fatal(err)
	}
	w.process(ctx)
	if state(7003) != "uncertain" || state(7005) != "uncertain" {
		t.Fatalf("recovered states: %s %s", state(7003), state(7005))
	}
	if tr.count(7001) != 1 || tr.count(7002) != 1 {
		t.Fatal("wrong deliveries")
	}
	var wait float64
	if err = pool.QueryRow(ctx, `SELECT extract(epoch from n.next_attempt_at-now()) FROM moderation_notifications n JOIN moderation_cases c ON c.id=n.case_id WHERE c.source_key=$1`, fmt.Sprintf("%s-%d", prefix, 7004)).Scan(&wait); err != nil || wait < 115 {
		t.Fatalf("retry-after %v %v", wait, err)
	}
	for i := 0; i < 4; i++ {
		_, err = pool.Exec(ctx, `UPDATE moderation_notifications n SET next_attempt_at=now() FROM moderation_cases c WHERE n.case_id=c.id AND c.source_key=$1`, fmt.Sprintf("%s-%d", prefix, 7004))
		if err != nil {
			t.Fatal(err)
		}
		w.process(ctx)
	}
	if state(7004) != "failed" || tr.count(7004) != 5 {
		t.Fatalf("429 retry cap %s calls=%d", state(7004), tr.count(7004))
	}
	if tr.count(7003) != 1 || tr.count(7005) != 1 {
		t.Fatalf("uncertain outcomes retried: 500=%d timeout=%d", tr.count(7003), tr.count(7005))
	}
	if tr.count(7002) != 1 {
		t.Fatal("retried permanent 403")
	}
	v := store.ModerationCaseInput{SourceKey: prefix + "-blocked-new", SourceKind: "test", SourceID: "blocked", ChatID: -42, UserID: 7002, Action: "ban", Result: "executed"}
	if _, err = q.RecordModerationCase(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT n.state FROM moderation_notifications n JOIN moderation_cases c ON c.id=n.case_id WHERE c.source_key=$1`, v.SourceKey).Scan(&v.Result); err != nil || v.Result != "unreachable" {
		t.Fatalf("blocked user %s %v", v.Result, err)
	}
	if err = q.RecordPrivateStart(ctx, 7002); err != nil {
		t.Fatal(err)
	}
	w.process(ctx)
	if tr.count(7002) != 1 {
		t.Fatal("start replayed terminal notice")
	}
	var summary string
	if err = pool.QueryRow(ctx, `SELECT summary FROM moderation_cases WHERE source_key=$1`, fmt.Sprintf("%s-%d", prefix, 7001)).Scan(&summary); err != nil || strings.Contains(summary, "13800138000") || strings.Contains(summary, "user@example.com") {
		t.Fatalf("summary %q %v", summary, err)
	}
}

func TestNotificationLeaseRecoveryAndUncertainSettlement(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := store.New(pool)
	prefix := fmt.Sprintf("lease-%d", time.Now().UnixNano())
	user := int64(899991)
	defer pool.Exec(ctx, `DELETE FROM moderation_notifications WHERE case_id IN (SELECT id FROM moderation_cases WHERE source_key LIKE $1);DELETE FROM moderation_cases WHERE source_key LIKE $1;DELETE FROM telegram_private_sessions WHERE user_id=$2`, prefix+"%", user)
	if err = q.RecordPrivateStart(ctx, user); err != nil {
		t.Fatal(err)
	}
	_, err = q.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: prefix, SourceKind: "test", SourceID: prefix, ChatID: -42, UserID: user, Action: "delete", Result: "executed"})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := q.ClaimModerationNotifications(ctx, "first", 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("first claim: %v %v", jobs, err)
	}
	_, err = pool.Exec(ctx, `UPDATE moderation_notifications SET lease_until=now()-interval '1 second' WHERE id=$1`, jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.RecoverModerationNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err = q.ClaimModerationNotifications(ctx, "second", 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("reclaim: %v %v", jobs, err)
	}
	if ok, err := q.BeginModerationNotification(ctx, jobs[0].ID, "second"); !ok || err != nil {
		t.Fatalf("begin: %v %v", ok, err)
	}
	// An accepted Telegram request with a failed DB settlement is never called sent.
	_, err = pool.Exec(ctx, `UPDATE moderation_notifications SET lease_until=now()-interval '1 second' WHERE id=$1`, jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := q.SettleModerationNotification(ctx, jobs[0].ID, "second", "sent", time.Now()); ok || err != nil {
		t.Fatalf("expired settlement: %v %v", ok, err)
	}
	if err = q.RecoverModerationNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = pool.QueryRow(ctx, `SELECT state FROM moderation_notifications WHERE id=$1`, jobs[0].ID).Scan(&state); err != nil || state != "uncertain" {
		t.Fatalf("state=%s %v", state, err)
	}
}

func TestStartDoesNotReplayUnsentNotifications(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := store.New(pool)
	user := int64(899992)
	key := fmt.Sprintf("restart-%d", time.Now().UnixNano())
	defer pool.Exec(ctx, `DELETE FROM moderation_notifications WHERE case_id IN (SELECT id FROM moderation_cases WHERE source_key=$1);DELETE FROM moderation_cases WHERE source_key=$1;DELETE FROM telegram_private_sessions WHERE user_id=$2`, key, user)
	if err = q.RecordPrivateStart(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err = q.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: key, SourceKind: "test", SourceID: key, ChatID: -42, UserID: user, Action: "delete", Result: "executed"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.ClaimModerationNotifications(ctx, "one", 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim %v %v", jobs, err)
	}
	if ok, err := q.BeginModerationNotification(ctx, jobs[0].ID, "one"); !ok || err != nil {
		t.Fatalf("begin %v %v", ok, err)
	}
	// A new /start while an old send is in flight must not be overwritten by its 403.
	if err = q.RecordPrivateStart(ctx, user); err != nil {
		t.Fatal(err)
	}
	if ok, err := q.SettleModerationNotification(ctx, jobs[0].ID, "one", "unreachable", time.Now()); !ok || err != nil {
		t.Fatalf("settle %v %v", ok, err)
	}
	var reachable bool
	if err = pool.QueryRow(ctx, `SELECT is_reachable FROM telegram_private_sessions WHERE user_id=$1`, user).Scan(&reachable); err != nil || !reachable {
		t.Fatalf("new authorization lost: %v %v", reachable, err)
	}
}

type blockedNotificationTransport struct {
	started chan struct{}
	release chan struct{}
	fast    chan struct{}
}

func (t *blockedNotificationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var v struct {
		ChatID int64 `json:"chat_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&v)
	if v.ChatID == 899993 {
		close(t.started)
		select {
		case <-t.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	} else {
		select {
		case t.fast <- struct{}{}:
		default:
		}
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
}
func TestSlowNotificationDoesNotBlockOtherUserOrCase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := store.New(pool)
	prefix := fmt.Sprintf("slow-%d", time.Now().UnixNano())
	defer pool.Exec(context.Background(), `DELETE FROM moderation_notifications WHERE case_id IN (SELECT id FROM moderation_cases WHERE source_key LIKE $1);DELETE FROM moderation_cases WHERE source_key LIKE $1;DELETE FROM telegram_private_sessions WHERE user_id BETWEEN 899993 AND 899995`, prefix+"%")
	for _, id := range []int64{899993, 899994, 899995} {
		if err = q.RecordPrivateStart(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	record := func(id int64) {
		_, e := q.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: fmt.Sprintf("%s-%d", prefix, id), SourceKind: "test", SourceID: fmt.Sprint(id), ChatID: -42, UserID: id, Action: "ban", Result: "executed"})
		if e != nil {
			t.Fatal(e)
		}
	}
	record(899993)
	record(899994)
	tr := &blockedNotificationTransport{started: make(chan struct{}), release: make(chan struct{}), fast: make(chan struct{}, 1)}
	w := NewModerationNotifications(zap.NewNop(), q, "123:fixture")
	w.client = &http.Client{Transport: tr}
	done := make(chan struct{})
	go func() { w.process(ctx); close(done) }()
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("slow not started")
	}
	select {
	case <-tr.fast:
	case <-time.After(time.Second):
		t.Fatal("fast user blocked")
	}
	t0 := time.Now()
	record(899995)
	if time.Since(t0) > time.Second {
		t.Fatal("case creation waited for private message")
	}
	close(tr.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestManyBlockedNotificationsBecomeTerminalWithoutHeadBlocking(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := store.New(pool)
	prefix := fmt.Sprintf("bulk403-%d", time.Now().UnixNano())
	defer pool.Exec(ctx, `DELETE FROM moderation_notifications WHERE case_id IN (SELECT id FROM moderation_cases WHERE source_key LIKE $1);DELETE FROM moderation_cases WHERE source_key LIKE $1;DELETE FROM telegram_private_sessions WHERE user_id IN (7001,7002)`, prefix+"%")
	for _, id := range []int64{7001, 7002} {
		if err = q.RecordPrivateStart(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 200; i++ {
		_, err = q.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: fmt.Sprintf("%s-%d", prefix, i), SourceKind: "test", SourceID: fmt.Sprint(i), ChatID: -42, UserID: 7002, Action: "delete", Result: "executed"})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = q.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: prefix + "-good", SourceKind: "test", SourceID: "good", ChatID: -42, UserID: 7001, Action: "delete", Result: "executed"})
	if err != nil {
		t.Fatal(err)
	}
	tr := &notificationTransport{calls: map[int64]int{}}
	w := NewModerationNotifications(zap.NewNop(), q, "123:fixture")
	w.client = &http.Client{Transport: tr}
	w.process(ctx)
	if tr.count(7002) != 1 || tr.count(7001) != 1 {
		t.Fatalf("calls blocked=%d good=%d", tr.count(7002), tr.count(7001))
	}
	for i := 0; i < 3; i++ {
		w.process(ctx)
	}
	var pending, terminal int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE n.state IN ('pending','leased','sending')),count(*) FILTER(WHERE n.state='unreachable') FROM moderation_notifications n JOIN moderation_cases c ON c.id=n.case_id WHERE c.source_key LIKE $1`, prefix+"%").Scan(&pending, &terminal); err != nil || pending != 0 || terminal != 200 {
		t.Fatalf("pending=%d terminal=%d %v", pending, terminal, err)
	}
	if tr.count(7002) != 1 {
		t.Fatal("multiple 403 requests")
	}
}
