//go:build integration

package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/pressly/goose/v3"
	"go.uber.org/zap"
)

func TestAppealResultForbiddenIsTerminal(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv("TEST_DATABASE_URL")
	bootstrap, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("appeal_worker_%d", time.Now().UnixNano())
	if _, err = bootstrap.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = bootstrap.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); bootstrap.Close() }()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	v := u.Query()
	v.Set("search_path", schema)
	u.RawQuery = v.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
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
	defer pool.Close()
	q := store.New(pool)
	key := fmt.Sprintf("appeal-worker-%d", time.Now().UnixNano())
	user := int64(88000001)
	defer func() {
		pool.Exec(ctx, `DELETE FROM moderation_notifications WHERE case_id IN(SELECT id FROM moderation_cases WHERE source_key=$1);DELETE FROM appeals WHERE case_id IN(SELECT id FROM moderation_cases WHERE source_key=$1);DELETE FROM moderation_cases WHERE source_key=$1;DELETE FROM telegram_private_sessions WHERE user_id=$2`, key, user)
	}()
	caseID, err := q.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: key, SourceKind: "test", SourceID: key, ChatID: -44, UserID: user, Action: "ban", Result: "executed"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.RecordPrivateStart(ctx, user); err != nil {
		t.Fatal(err)
	}
	var appeal int64
	err = pool.QueryRow(ctx, `INSERT INTO appeals(case_id,chat_id,user_id,reason,status,decision,public_note,execution_result,review_version) VALUES($1,-44,$2,'fixture','upheld','uphold','公开说明','审核后维持原处理',1) RETURNING id`, caseID, user).Scan(&appeal)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.QueueAppealResult(ctx, appeal); err != nil {
		t.Fatal(err)
	}
	if err = q.QueueAppealResult(ctx, appeal); err != nil {
		t.Fatal(err)
	}
	var text string
	var markup any
	calls := 0
	w := NewModerationNotifications(zap.NewNop(), q, "fake", "https://example.test")
	w.client = &http.Client{Transport: appealNotificationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		text, _ = p["text"].(string)
		markup = p["reply_markup"]
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":403}`))}, nil
	})}
	w.process(ctx)
	var state, decision string
	var count int
	err = pool.QueryRow(ctx, `SELECT n.state,a.decision,(SELECT count(*) FROM moderation_notifications WHERE appeal_id=$1) FROM moderation_notifications n JOIN appeals a ON a.id=n.appeal_id WHERE n.appeal_id=$1`, appeal).Scan(&state, &decision, &count)
	if err != nil || state != "unreachable" || decision != "uphold" || count != 1 || calls != 1 || markup != nil || !strings.Contains(text, "公开说明") || !strings.Contains(text, "维持原处理") {
		t.Fatalf("state=%s decision=%s count=%d calls=%d markup=%v text=%q err=%v", state, decision, count, calls, markup, text, err)
	}
	w.process(ctx)
	if calls != 1 {
		t.Fatal("403 retried")
	}

	// Verify approved + completed appeal through SQL QueueAppealResult + ClaimModerationNotifications attaches join link button and group title.
	_, err = pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type,enabled) VALUES(-44,'测试官方群','supergroup',true) ON CONFLICT(chat_id) DO UPDATE SET title=EXCLUDED.title`)
	if err != nil {
		t.Fatal(err)
	}
	user2 := int64(88000002)
	key2 := key + "-approved"
	caseID2, err := q.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: key2, SourceKind: "test", SourceID: key2, ChatID: -44, UserID: user2, Action: "ban", Result: "executed"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.RecordPrivateStart(ctx, user2); err != nil {
		t.Fatal(err)
	}
	var approvedAppeal int64
	err = pool.QueryRow(ctx, `INSERT INTO appeals(case_id,chat_id,user_id,reason,status,decision,public_note,execution_result,review_version) VALUES($1,-44,$2,'fixture2','completed','approve','管理员通过 Bot 私信审核通过','审核通过，原封禁已解除；如有已删除的消息，无法恢复',2) RETURNING id`, caseID2, user2).Scan(&approvedAppeal)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.QueueAppealResult(ctx, approvedAppeal); err != nil {
		t.Fatal(err)
	}
	var approvedText string
	var approvedMarkup map[string]any
	w.client = &http.Client{Transport: appealNotificationTransport(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getChat"):
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"username":"rfchost"}}`))}, nil
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			approvedText, _ = p["text"].(string)
			approvedMarkup, _ = p["reply_markup"].(map[string]any)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
			return nil, nil
		}
	})}
	w.process(ctx)
	if !strings.Contains(approvedText, "✅ 申诉审核通过 · APL-") || !strings.Contains(approvedText, "<b>群聊：</b>测试官方群") || !strings.Contains(approvedText, "重新加入群聊") || approvedMarkup == nil {
		t.Fatalf("approvedText=%q approvedMarkup=%+v", approvedText, approvedMarkup)
	}
}
