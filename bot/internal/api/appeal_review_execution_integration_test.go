//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

type appealExecutionTransport struct {
	mu       sync.Mutex
	until    int64
	calls    map[int64]int
	failOnce bool
}

func (x *appealExecutionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/getChatMember") {
		var v struct {
			UserID string `json:"user_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&v)
		userID, _ := strconv.ParseInt(v.UserID, 10, 64)
		role := "kicked"
		until := int64(0)
		if userID == 124 || userID == 130 {
			role = "restricted"
			until = x.until
		}
		body := fmt.Sprintf(`{"ok":true,"result":{"user":{"id":%d,"first_name":"fixture"},"status":%q,"is_member":true,"until_date":%d}}`, userID, role, until)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	if strings.HasSuffix(r.URL.Path, "/unbanChatMember") || strings.HasSuffix(r.URL.Path, "/restrictChatMember") {
		var v struct {
			UserID string `json:"user_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&v)
		userID, _ := strconv.ParseInt(v.UserID, 10, 64)
		x.mu.Lock()
		x.calls[userID]++
		fail := userID == 127 && x.failOnce || userID == 130 && x.calls[userID] == 1
		if fail {
			x.failOnce = false
		}
		x.mu.Unlock()
		if fail {
			return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":403,"description":"Forbidden: insufficient rights"}`))}, nil
		}
		if userID == 125 {
			return nil, context.DeadlineExceeded
		}
		if userID == 129 {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`not-json`))}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`))}, nil
	}
	return (exemptTelegramTransport{}).RoundTrip(r)
}
func (x *appealExecutionTransport) count(id int64) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.calls[id]
}
func TestAppealReviewExecutionAndRecovery(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	admin, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 101, Role: "owner", GroupScope: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-100,'test','supergroup')`)
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(10 * time.Minute).Unix()
	tr := &appealExecutionTransport{until: until, calls: map[int64]int{}, failOnce: true}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	cfg := config.Config{BotToken: "123:TEST", JWTSecret: "fixture"}
	svc, err := bot.New(ctx, cfg, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(cfg, zap.NewNop(), svc, nil)
	jwt, err := srv.signAdminJWT(admin)
	if err != nil {
		t.Fatal(err)
	}
	makeCase := func(user int64, action string, owned bool) (int64, int64) {
		var decision int64
		if e := fx.pool.QueryRow(ctx, `INSERT INTO ai_decisions(chat_id,user_id,message_id,model,prompt_version,verdict,confidence,category,action_taken) VALUES(-100,$1,1,'x','x','spam',1,'spam',$2) RETURNING id`, user, action).Scan(&decision); e != nil {
			t.Fatal(e)
		}
		if action == "ban" {
			owner := decision
			if !owned {
				owner = decision + 10000
			}
			if _, e := fx.pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score,banned_reason) VALUES(-100,$1,'banned',0,jsonb_build_object('source','ai','decision_id',$2::bigint))`, user, owner); e != nil {
				t.Fatal(e)
			}
		} else {
			if _, e := fx.pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score) VALUES(-100,$1,'new',0.5)`, user); e != nil {
				t.Fatal(e)
			}
			if e := fx.queries.ReserveAIMute(ctx, -100, user, decision, until, "fixture"); e != nil {
				t.Fatal(e)
			}
			if ok, e := fx.queries.ActivateAIMute(ctx, -100, user, "fixture"); e != nil || !ok {
				t.Fatalf("activate %v %v", ok, e)
			}
		}
		c, e := fx.queries.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: fmt.Sprintf("execution:%d", user), SourceKind: "ai_event", SourceID: fmt.Sprint(user), ChatID: -100, UserID: user, Action: action, Result: "executed", Ownership: []byte(fmt.Sprintf(`{"ai_decision_id":%d}`, decision))})
		if e != nil {
			t.Fatal(e)
		}
		var appeal int64
		e = fx.pool.QueryRow(ctx, `INSERT INTO appeals(case_id,chat_id,user_id,reason) VALUES($1,-100,$2,'fixture') RETURNING id`, c, user).Scan(&appeal)
		if e != nil {
			t.Fatal(e)
		}
		return appeal, c
	}
	ban, caseID := makeCase(123, "ban", true)
	mute, _ := makeCase(124, "mute", true)
	timeout, _ := makeCase(125, "ban", true)
	newer, _ := makeCase(126, "ban", false)
	retryable, _ := makeCase(127, "ban", true)
	uncertainDB, dbCase := makeCase(128, "ban", true)
	malformed, _ := makeCase(129, "ban", true)
	muteRetry, _ := makeCase(130, "mute", true)
	request := func(id int64, kind string, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/admin/appeals/%d/%s", id, kind), strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: adminCookieName, Value: jwt})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "t"})
		r.Header.Set("X-CSRF-Token", "t")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	approve := func(id int64) *httptest.ResponseRecorder {
		return request(id, "decision", `{"decision":"approve","note":"审核说明"}`)
	}
	if _, err = fx.pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION fail_appeal_settle() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.id=%d AND NEW.status='completed' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END$$; CREATE TRIGGER fail_appeal_settle BEFORE UPDATE ON appeals FOR EACH ROW EXECUTE FUNCTION fail_appeal_settle()`, ban)); err != nil {
		t.Fatal(err)
	}
	if w := approve(ban); w.Code != 503 || tr.count(123) != 1 {
		t.Fatalf("settlement failure %d %s calls %d", w.Code, w.Body, tr.count(123))
	}
	if _, err = fx.pool.Exec(ctx, `DROP TRIGGER fail_appeal_settle ON appeals; DROP FUNCTION fail_appeal_settle()`); err != nil {
		t.Fatal(err)
	}
	if w := request(ban, "retry", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"completed"`) || tr.count(123) != 1 {
		t.Fatalf("settlement recovery %d %s calls %d", w.Code, w.Body, tr.count(123))
	}
	if w := approve(ban); w.Code != 200 || tr.count(123) != 1 {
		t.Fatalf("idempotency %d %s", w.Code, w.Body)
	}
	var score float64
	if err = fx.pool.QueryRow(ctx, `SELECT score FROM user_trust WHERE chat_id=-100 AND user_id=123`).Scan(&score); err != nil || score < 0.599 || score > 0.601 {
		t.Fatalf("score %f %v", score, err)
	}
	if w := approve(mute); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"completed"`) || tr.count(124) != 1 {
		t.Fatalf("mute %d %s calls %d", w.Code, w.Body, tr.count(124))
	}
	if w := approve(timeout); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"manual_review"`) || tr.count(125) != 1 {
		t.Fatalf("uncertain %d %s %d", w.Code, w.Body, tr.count(125))
	}
	if w := request(timeout, "retry", `{}`); w.Code != 409 || tr.count(125) != 1 {
		t.Fatalf("uncertain replay %d %s", w.Code, w.Body)
	}
	if w := approve(retryable); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"failed"`) || tr.count(127) != 1 {
		t.Fatalf("explicit failure %d %s", w.Code, w.Body)
	}
	if w := request(retryable, "retry", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"completed"`) || tr.count(127) != 2 {
		t.Fatalf("explicit retry %d %s", w.Code, w.Body)
	}
	var dbDecision int64
	if err = fx.pool.QueryRow(ctx, `SELECT (ownership->>'ai_decision_id')::bigint FROM moderation_cases WHERE id=$1`, dbCase).Scan(&dbDecision); err != nil {
		t.Fatal(err)
	}
	if _, err = fx.pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION fail_dispatch_result() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.decision_id=%d AND NEW.state='telegram_done' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END$$;CREATE TRIGGER fail_dispatch_result BEFORE UPDATE ON ai_unban_review_claims FOR EACH ROW EXECUTE FUNCTION fail_dispatch_result()`, dbDecision)); err != nil {
		t.Fatal(err)
	}
	if w := approve(uncertainDB); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"manual_review"`) || tr.count(128) != 1 {
		t.Fatalf("telegram done db failure %d %s", w.Code, w.Body)
	}
	if _, err = fx.pool.Exec(ctx, `DROP TRIGGER fail_dispatch_result ON ai_unban_review_claims;DROP FUNCTION fail_dispatch_result()`); err != nil {
		t.Fatal(err)
	}
	if w := request(uncertainDB, "retry", `{}`); w.Code != 409 || tr.count(128) != 1 {
		t.Fatalf("db uncertainty replay %d %s", w.Code, w.Body)
	}
	if w := approve(malformed); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"manual_review"`) || tr.count(129) != 1 {
		t.Fatalf("malformed response %d %s", w.Code, w.Body)
	}
	if w := request(malformed, "retry", `{}`); w.Code != 409 || tr.count(129) != 1 {
		t.Fatalf("malformed replay %d %s", w.Code, w.Body)
	}
	if w := approve(muteRetry); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"failed"`) || tr.count(130) != 1 {
		t.Fatalf("mute explicit failure %d %s", w.Code, w.Body)
	}
	if w := request(muteRetry, "retry", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"completed"`) || tr.count(130) != 2 {
		t.Fatalf("mute retry %d %s", w.Code, w.Body)
	}
	if w := approve(newer); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"manual_review"`) || tr.count(126) != 0 {
		t.Fatalf("later punishment %d %s %d", w.Code, w.Body, tr.count(126))
	}
	a, e := fx.queries.AppealForUserCase(ctx, caseID, 123)
	if e != nil || a.Status != "completed" || a.PublicNote != "审核说明" {
		t.Fatalf("own result %+v %v", a, e)
	}
	mini := func(user int64) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"case_id":%d,"init_data":%q}`, caseID, miniSignedData(user, time.Now(), cfg.BotToken))
		r := httptest.NewRequest("POST", "/api/appeal/mini/case", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	if w := mini(123); w.Code != 200 || !strings.Contains(w.Body.String(), "审核说明") || !strings.Contains(w.Body.String(), "原封禁已解除") {
		t.Fatalf("user progress %d %s", w.Code, w.Body)
	}
	if w := mini(124); w.Code != 404 {
		t.Fatalf("user result leaked %d %s", w.Code, w.Body)
	}
	var notifications int
	if err = fx.pool.QueryRow(ctx, `SELECT count(*) FROM moderation_notifications WHERE appeal_id=$1 AND event_kind='appeal_result'`, ban).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("notification %d %v", notifications, err)
	}
}
