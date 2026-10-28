//go:build integration

package api

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/pressly/goose/v3"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

type exemptTelegramTransport struct{}

func (exemptTelegramTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.telegram.org" {
		return nil, fmt.Errorf("unexpected network request: %s", r.URL)
	}
	body := `{"ok":true,"result":true}`
	switch {
	case strings.HasSuffix(r.URL.Path, "/getMe"):
		body = `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"fixture","username":"fixture_bot"}}`
	case strings.HasSuffix(r.URL.Path, "/getChatMember"):
		body = `{"ok":true,"result":{"user":{"id":101,"is_bot":false,"first_name":"fixture"},"status":"administrator"}}`
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		body = `{"ok":true,"result":{"message_id":3,"chat":{"id":-100,"type":"supergroup"}}}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestGroupExemptAPIIntegration(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	owner, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 101, Role: "owner", GroupScope: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 102, Role: "admin", GroupScope: []byte(`[-100]`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-100,'fixture','supergroup'); INSERT INTO authorized_groups(chat_id) VALUES(-100)`); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	t.Cleanup(func() { http.DefaultTransport = old })
	if err != nil {
		t.Fatalf("fixture bot: %v", err)
	}
	t.Cleanup(svc.Stop)
	const secret = "test-only-jwt-secret"
	server := NewServer(config.Config{JWTSecret: secret}, zap.NewNop(), svc, nil)
	token := func(a store.Admin) string {
		value, err := jwt.NewWithClaims(jwt.SigningMethodHS256, adminClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: fmt.Sprint(a.ID), ID: fmt.Sprint(a.ID), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(secret))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	request := func(method, path, body string, a store.Admin, csrf bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: adminCookieName, Value: token(a)})
		if csrf {
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "fixture-csrf"})
			req.Header.Set("X-CSRF-Token", "fixture-csrf")
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	t.Run("identity_index_plan", func(t *testing.T) {
		tx, e := fx.pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if _, e = tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); e != nil {
			t.Fatal(e)
		}
		rows, e := tx.Query(ctx, `EXPLAIN SELECT 1 FROM user_trust WHERE user_id=$1 AND is_bot`, int64(987654))
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		var plan string
		for rows.Next() {
			var line string
			if e = rows.Scan(&line); e != nil {
				t.Fatal(e)
			}
			plan += line + "\n"
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(plan, "idx_user_trust_known_bot_user") || !strings.Contains(plan, "Index Cond") {
			t.Fatalf("known-bot index not used: %s", plan)
		}
		rows.Close()
		rows, e = tx.Query(ctx, `EXPLAIN SELECT EXISTS(SELECT 1 FROM group_exempt_users WHERE chat_id=$2 AND user_id=$1 AND (expires_at IS NULL OR expires_at>now()) AND NOT EXISTS(SELECT 1 FROM user_trust WHERE user_id=$1 AND is_bot)), EXISTS(SELECT 1 FROM user_trust WHERE chat_id=$2 AND user_id=$1 AND status='banned')`, int64(987654), int64(-100))
		if e != nil {
			t.Fatal(e)
		}
		plan = ""
		for rows.Next() {
			var line string
			if e = rows.Scan(&line); e != nil {
				t.Fatal(e)
			}
			plan += line + "\n"
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(plan, "idx_user_trust_known_bot_user") || !strings.Contains(plan, "group_exempt_users_pkey") {
			t.Fatalf("group status index plan: %s", plan)
		}
		rows, e = tx.Query(ctx, `EXPLAIN SELECT count(*) FROM group_exempt_users WHERE chat_id=$1 AND expires_at<=now()`, int64(-100))
		if e != nil {
			t.Fatal(e)
		}
		plan = ""
		for rows.Next() {
			var line string
			if e = rows.Scan(&line); e != nil {
				t.Fatal(e)
			}
			plan += line + "\n"
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(plan, "idx_group_exempt_expiry") || !strings.Contains(plan, "Index Cond") {
			t.Fatalf("expired filter index plan: %s", plan)
		}
	})
	base := "/api/admin/groups/-100/exempt-users"
	if _, err = fx.pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,is_bot) VALUES(-100,987654,true)`); err != nil {
		t.Fatal(err)
	}
	if rec := request("PUT", base+"/987654", `{"note":"bot"}`, scoped, true); rec.Code != 400 {
		t.Fatalf("known bot accepted: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("PUT", base+"/1087968824", `{}`, scoped, true); rec.Code != 400 {
		t.Fatalf("virtual sender accepted: %d %s", rec.Code, rec.Body.String())
	}
	id := "12345678901234567"
	path := base + "/" + id
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		url := path
		if m == "GET" {
			url = base
		}
		url = strings.Replace(url, "/groups/-100/", "/groups/-200/", 1)
		rec := request(m, url, `{"note":"x"}`, scoped, true)
		if rec.Code != 403 {
			t.Fatalf("scoped %s = %d %s", m, rec.Code, rec.Body.String())
		}
	}
	if rec := request("PUT", path, `{"note":"x"}`, owner, false); rec.Code != 403 {
		t.Fatalf("csrf status %d", rec.Code)
	}
	for _, invalid := range []string{"0", "-1", "abc", "9223372036854775808"} {
		if rec := request("PUT", base+"/"+invalid, `{"note":"x"}`, owner, true); rec.Code != 400 {
			t.Fatalf("bad id %s: %d", invalid, rec.Code)
		}
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO global_exempt_users(user_id,note) VALUES($1,'legacy')`, int64(12345678901234567)); err != nil {
		t.Fatal(err)
	}
	if active, _, err := fx.queries.GroupExemptionStatus(ctx, -100, 12345678901234567); err != nil || active {
		t.Fatalf("legacy global record became effective: %v %v", active, err)
	}
	if rec := request("PUT", path, `{"note":"fixture","expires_at":null}`, scoped, true); rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("GET", base, "", owner, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user_id":"12345678901234567"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("GET", base, "", scoped, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("authorized scoped admin cannot read: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("GET", "/api/admin/global-exempt-users", "", owner, false); rec.Code != 404 {
		t.Fatalf("global route still enabled: %d", rec.Code)
	}
	if _, err = fx.pool.Exec(ctx, `INSERT INTO group_exempt_users(chat_id,user_id,note,expires_at) SELECT -100,i,CASE WHEN i=20 THEN 'literal%_' ELSE 'plain' END,CASE WHEN i%2=0 THEN now()-interval '1 hour' ELSE now()+interval '1 day' END FROM generate_series(1,45) i; INSERT INTO group_exempt_users(chat_id,user_id,note) VALUES(-200,20,'literal%_')`); err != nil {
		t.Fatal(err)
	}
	var expired time.Time
	if err = fx.pool.QueryRow(ctx, `SELECT expires_at FROM group_exempt_users WHERE chat_id=-100 AND user_id=20`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"note":"literal%%_ changed","expires_at":%q}`, expired.Format(time.RFC3339Nano))
	if rec := request("PUT", base+"/20", body, scoped, true); rec.Code != 200 {
		t.Fatalf("edit expired unchanged %d %s", rec.Code, rec.Body)
	}
	if rec := request("PUT", base+"/21", body, scoped, true); rec.Code != 400 {
		t.Fatalf("new past expiry accepted %d %s", rec.Code, rec.Body)
	}
	if rec := request("GET", base+"?limit=20&offset=20&status=expired&search=%25_", "", scoped, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":1`) || !strings.Contains(rec.Body.String(), `"users":[]`) {
		t.Fatalf("literal pagination %d %s", rec.Code, rec.Body)
	}
	if rec := request("GET", base+"?limit=20&offset=0&status=expired&search=%25_", "", scoped, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user_id":"20"`) || !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("literal search %d %s", rec.Code, rec.Body)
	}
	if rec := request("GET", base+"?limit=20&offset=20", "", scoped, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":46`) || !strings.Contains(rec.Body.String(), `"has_more":true`) {
		t.Fatalf("page %d %s", rec.Code, rec.Body)
	}
	active, banned, err := store.New(fx.pool).GroupExemptionStatus(ctx, -100, 12345678901234567)
	if err != nil || !active || banned {
		t.Fatalf("persisted: %v %v %v", active, banned, err)
	}
	var preservedDecision int64
	if err = fx.pool.QueryRow(ctx, `INSERT INTO ai_decisions(chat_id,user_id,message_id,model,prompt_version,verdict,confidence,category,action_taken) VALUES(-100,555,88,'fixture','fixture','spam',1,'spam','ban') RETURNING id`).Scan(&preservedDecision); err != nil {
		t.Fatal(err)
	}
	if _, err = fx.pool.Exec(ctx, `INSERT INTO ai_false_positive_reviews(decision_id) VALUES($1)`, preservedDecision); err != nil {
		t.Fatal(err)
	}
	// Rollback deliberately retains this additive table; a later upgrade must
	// therefore recreate no objects and preserve its rows.
	schema := fx.pool.Config().ConnConfig.RuntimeParams["search_path"]
	db, err := sql.Open("pgx", withSearchPath(os.Getenv("TEST_DATABASE_URL"), schema))
	if err != nil {
		t.Fatal(err)
	}
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.DownContext(ctx, db, h1MigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	if err = goose.DownContext(ctx, db, h1MigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	if err = goose.DownContext(ctx, db, h1MigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	if err = goose.DownContext(ctx, db, h1MigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err = db.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != 37 {
		t.Fatalf("expected upgrade from v37: %d %v", version, err)
	}
	if err = goose.UpContext(ctx, db, h1MigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	var oldReview int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM ai_false_positive_reviews WHERE decision_id=$1`, preservedDecision).Scan(&oldReview); err != nil || oldReview != 1 {
		t.Fatalf("039 review data lost: %d %v", oldReview, err)
	}
	var legacy int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM global_exempt_users WHERE user_id=12345678901234567`).Scan(&legacy); err != nil || legacy != 1 {
		t.Fatalf("legacy record lost: %d %v", legacy, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	active, _, err = fx.queries.GroupExemptionStatus(ctx, -100, 12345678901234567)
	if err != nil || !active {
		t.Fatalf("rollback/re-upgrade lost record: active=%v err=%v", active, err)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if rec := request("PUT", path, `{"note":"edited","expires_at":"`+future+`"}`, owner, true); rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE group_exempt_users SET expires_at=now()-interval '1 second' WHERE user_id=$1`, int64(12345678901234567)); err != nil {
		t.Fatal(err)
	}
	active, _, err = fx.queries.GroupExemptionStatus(ctx, -100, 12345678901234567)
	if err != nil || active {
		t.Fatalf("expired: %v %v", active, err)
	}
	if rec := request("DELETE", path, "", owner, true); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("DELETE", path, "", owner, true); rec.Code != 404 {
		t.Fatalf("missing delete: %d", rec.Code)
	}
	var audit int
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM config_audit WHERE action IN ('upsert_group_exempt_user','delete_group_exempt_user')`).Scan(&audit); err != nil || audit != 4 {
		t.Fatalf("audit count=%d err=%v", audit, err)
	}
	// Exercise actual Telegram dispatch and Web API against the same database.
	update := func(id int, text string) {
		t.Helper()
		err := svc.ProcessUpdate(tele.Update{ID: id, Message: &tele.Message{ID: id, Text: text, Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 101, FirstName: "fixture"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	update(891, "/exempt@fixture_bot 555 1d via-command")
	if rec := request("GET", base, "", scoped, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user_id":"555"`) {
		t.Fatalf("command add not visible to web: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("DELETE", base+"/555", "", scoped, true); rec.Code != 204 {
		t.Fatalf("web revoke command entry: %d", rec.Code)
	}
	if rec := request("PUT", base+"/556", `{"note":"web"}`, scoped, true); rec.Code != 200 {
		t.Fatalf("web add: %d", rec.Code)
	}
	update(892, "/unexempt 556")
	if rec := request("GET", base, "", scoped, false); strings.Contains(rec.Body.String(), `"user_id":"556"`) {
		t.Fatalf("command revoke not visible to web: %s", rec.Body.String())
	}
}
