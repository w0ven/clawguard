//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

type reviewRecordingTransport struct {
	mu        sync.Mutex
	methods   []string
	failUnban bool
}

func (x *reviewRecordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	x.mu.Lock()
	x.methods = append(x.methods, r.URL.Path)
	fail := x.failUnban && strings.HasSuffix(r.URL.Path, "/unbanChatMember")
	x.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("simulated telegram failure")
	}
	return (exemptTelegramTransport{}).RoundTrip(r)
}
func (x *reviewRecordingTransport) calls() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]string{}, x.methods...)
}

func TestReviewUnbanAndScopedAudit(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	owner, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 101, Role: "owner", GroupScope: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 102, Role: "admin", GroupScope: []byte(`[-100,-300]`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-100,'fixture','supergroup'); INSERT INTO authorized_groups(chat_id) VALUES(-100); INSERT INTO user_trust(chat_id,user_id,status,score) VALUES(-100,555,'banned',0); INSERT INTO group_exempt_users(chat_id,user_id) VALUES(-100,555)`)
	if err != nil {
		t.Fatal(err)
	}
	transport := &reviewRecordingTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	const secret = "read-only-review-dummy-jwt"
	server := NewServer(config.Config{JWTSecret: secret}, zap.NewNop(), svc, nil)
	request := func(method, path, body string, a store.Admin) *httptest.ResponseRecorder {
		value, e := jwt.NewWithClaims(jwt.SigningMethodHS256, adminClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: fmt.Sprint(a.ID), ID: fmt.Sprint(a.ID), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(secret))
		if e != nil {
			t.Fatal(e)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: adminCookieName, Value: value})
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "review-csrf"})
		req.Header.Set("X-CSRF-Token", "review-csrf")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	t.Run("web_unban_keeps_banned_and_next_message_rebans", func(t *testing.T) {
		rec := request("POST", "/api/admin/unban?chat_id=-100&user_id=555", "", owner)
		if rec.Code != 204 {
			t.Fatalf("unban=%d %s", rec.Code, rec.Body.String())
		}
		trust, e := fx.queries.GetUserTrust(ctx, -100, 555)
		if e != nil {
			t.Fatal(e)
		}
		if trust.Status == "banned" {
			t.Fatalf("unban did not clear group status=%s", trust.Status)
		}
		before := len(transport.calls())
		e = svc.ProcessUpdate(tele.Update{ID: 9001, Message: &tele.Message{ID: 9001, Text: "hello", Chat: &tele.Chat{ID: -100, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 555, FirstName: "Fixture"}}})
		if e != nil {
			t.Fatal(e)
		}
		methods := transport.calls()[before:]
		found := false
		for _, m := range methods {
			if strings.HasSuffix(m, "/kickChatMember") {
				found = true
			}
		}
		if found {
			t.Fatalf("unbanned user was rebanned: calls=%v", methods)
		}
		t.Logf("unban HTTP=%d, trust=%s, next message calls=%v", rec.Code, trust.Status, methods)
	})
	t.Run("ai_review_ownership_retry_and_isolation", func(t *testing.T) {
		makeDecision := func(user int64, owned bool) int64 {
			t.Helper()
			var id int64
			err := fx.pool.QueryRow(ctx, `INSERT INTO ai_decisions(chat_id,user_id,message_id,model,prompt_version,verdict,confidence,category,action_taken) VALUES(-100,$1,9001,'fixture','fixture','spam',1,'spam','ban') RETURNING id`, user).Scan(&id)
			if err != nil {
				t.Fatal(err)
			}
			reason := `{}`
			if owned {
				reason = fmt.Sprintf(`{"source":"ai","decision_id":%d}`, id)
			}
			_, err = fx.pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score,banned_reason) VALUES(-100,$1,'banned',0,$2::jsonb),(-200,$1,'banned',0,'{}'::jsonb)`, user, reason)
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		call := func(id int64) *httptest.ResponseRecorder {
			return request("PUT", fmt.Sprintf("/api/admin/ai-decisions/%d", id), `{"admin_override":"false_positive"}`, owner)
		}
		assertStatus := func(chat, user int64, want string) {
			t.Helper()
			trust, e := fx.queries.GetUserTrust(ctx, chat, user)
			if e != nil || trust.Status != want {
				t.Fatalf("trust %d/%d=%+v err=%v", chat, user, trust, e)
			}
		}
		id := makeDecision(777, true)
		transport.failUnban = true
		rec := call(id)
		if rec.Code != 500 {
			t.Fatalf("telegram failure=%d %s", rec.Code, rec.Body)
		}
		transport.failUnban = false
		assertStatus(-100, 777, "banned")
		_, err := fx.pool.Exec(ctx, `CREATE FUNCTION fail_review_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture'; END $$; CREATE TRIGGER fail_review BEFORE INSERT ON ai_false_positive_reviews FOR EACH ROW EXECUTE FUNCTION fail_review_insert()`)
		if err != nil {
			t.Fatal(err)
		}
		rec = call(id)
		if rec.Code != 500 {
			t.Fatalf("database failure=%d %s", rec.Code, rec.Body)
		}
		assertStatus(-100, 777, "banned")
		if _, err = fx.pool.Exec(ctx, `DROP TRIGGER fail_review ON ai_false_positive_reviews; DROP FUNCTION fail_review_insert()`); err != nil {
			t.Fatal(err)
		}
		rec = call(id)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unban_performed":true`) {
			t.Fatalf("retry=%d %s", rec.Code, rec.Body)
		}
		assertStatus(-100, 777, "new")
		assertStatus(-200, 777, "banned")
		trust, e := fx.queries.GetUserTrust(ctx, -100, 777)
		if e != nil {
			t.Fatal(e)
		}
		score := trust.Score
		rec = call(id)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), `"unban_performed":true`) {
			t.Fatalf("duplicate=%d %s", rec.Code, rec.Body)
		}
		trust, e = fx.queries.GetUserTrust(ctx, -100, 777)
		if e != nil || trust.Score != score {
			t.Fatalf("duplicate changed score: %v %v", trust.Score, e)
		}
		legacy := makeDecision(778, false)
		before := len(transport.calls())
		rec = call(legacy)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "无法确认当前封禁归属") || strings.Contains(rec.Body.String(), `"unban_performed":true`) {
			t.Fatalf("legacy=%d %s", rec.Code, rec.Body)
		}
		for _, method := range transport.calls()[before:] {
			if strings.HasSuffix(method, "/unbanChatMember") {
				t.Fatal("legacy auto-unbanned")
			}
		}
		assertStatus(-100, 778, "banned")
		manual := makeDecision(779, true)
		rec = request("POST", "/api/admin/ban?chat_id=-100&user_id=779", "", owner)
		if rec.Code != 204 {
			t.Fatalf("manual ban=%d %s", rec.Code, rec.Body)
		}
		rec = call(manual)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), `"unban_performed":true`) {
			t.Fatalf("manual ban overwritten: %d %s", rec.Code, rec.Body)
		}
		assertStatus(-100, 779, "banned")
		prior := makeDecision(780, true)
		_, err = fx.pool.Exec(ctx, `INSERT INTO ai_decisions(chat_id,user_id,message_id,model,prompt_version,verdict,confidence,category,action_taken) VALUES(-100,780,9002,'other','fixture','spam',1,'spam','ban')`)
		if err != nil {
			t.Fatal(err)
		}
		if err = svc.BanChatUser(ctx, -100, 780); err != nil {
			t.Fatal(err)
		}
		rec = call(prior)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), `"unban_performed":true`) {
			t.Fatalf("later independent ban overwritten: %d %s", rec.Code, rec.Body)
		}
		assertStatus(-100, 780, "banned")
	})
	t.Run("scoped_audit_limit_precedes_scope", func(t *testing.T) {
		_, e := fx.pool.Exec(ctx, `INSERT INTO config_audit(scope,chat_id,admin_id,action,diff,created_at) VALUES('group',-100,$1,'visible-target','{}',now()-interval '1 minute'),('group',-200,$1,'other-group','{}',now())`, owner.ID)
		if e != nil {
			t.Fatal(e)
		}
		rec := request("GET", "/api/admin/audit?limit=1", "", scoped)
		if rec.Code != 200 {
			t.Fatalf("audit=%d %s", rec.Code, rec.Body.String())
		}
		var data struct {
			Audit []json.RawMessage `json:"audit"`
		}
		if e = json.Unmarshal(rec.Body.Bytes(), &data); e != nil {
			t.Fatal(e)
		}
		if len(data.Audit) != 1 || !strings.Contains(rec.Body.String(), "visible-target") {
			t.Fatalf("scoped audit omitted authorized item: %s", rec.Body.String())
		}
		explicit := request("GET", "/api/admin/audit?chat_id=-100&limit=1", "", scoped)
		if !strings.Contains(explicit.Body.String(), "visible-target") {
			t.Fatalf("target fixture not accessible: %s", explicit.Body.String())
		}
		_, e = fx.pool.Exec(ctx, `INSERT INTO config_audit(scope,chat_id,admin_id,action,diff,created_at) VALUES ('group',-300,$1,'second-allowed','{}',now()-interval '2 minutes'),('global',NULL,$1,'secret-global','{}',now()+interval '1 minute')`, owner.ID)
		if e != nil {
			t.Fatal(e)
		}
		rec = request("GET", "/api/admin/audit?limit=2", "", scoped)
		if strings.Contains(rec.Body.String(), "other-group") || strings.Contains(rec.Body.String(), "secret-global") || !strings.Contains(rec.Body.String(), "visible-target") || !strings.Contains(rec.Body.String(), "second-allowed") {
			t.Fatalf("scoped multi-group limit/leak: %s", rec.Body.String())
		}
		explicit = request("GET", "/api/admin/audit?chat_id=-200&limit=1", "", scoped)
		if strings.Contains(explicit.Body.String(), "other-group") {
			t.Fatalf("out of scope leak: %s", explicit.Body.String())
		}
		rec = request("GET", "/api/admin/audit?limit=1", "", owner)
		if !strings.Contains(rec.Body.String(), "secret-global") {
			t.Fatalf("owner global audit missing: %s", rec.Body.String())
		}
	})
}
