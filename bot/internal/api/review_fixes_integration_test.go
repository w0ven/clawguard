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
	mu           sync.Mutex
	methods      []string
	failUnban    bool
	blockStarted chan struct{}
	blockRelease chan struct{}
}

func (x *reviewRecordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	x.mu.Lock()
	x.methods = append(x.methods, r.URL.Path)
	isUnban := strings.HasSuffix(r.URL.Path, "/unbanChatMember")
	fail := x.failUnban && isUnban
	started, release := x.blockStarted, x.blockRelease
	x.mu.Unlock()
	if isUnban && started != nil {
		close(started)
		<-release
	}
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
	t.Run("scoped_dashboard_counts", func(t *testing.T) {
		_, e := fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-300,'allowed','supergroup'),(-200,'denied','supergroup');
        INSERT INTO pending_verifications(chat_id,user_id,method,expires_at) VALUES(-100,100,'button',now()+interval '1 day'),(-300,101,'button',now()+interval '1 day'),(-300,102,'button',now()-interval '1 day'),(-200,103,'button',now()+interval '1 day');
        INSERT INTO violations(chat_id,user_id,rule,action,created_at) VALUES(-100,100,'test','warn',date_trunc('day',now())),(-300,101,'test','warn',now()),(-300,102,'test','warn',date_trunc('day',now())-interval '1 microsecond'),(-200,103,'test','warn',now());`)
		if e != nil {
			t.Fatal(e)
		}
		rec := request("GET", "/api/admin/stats", "", scoped)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"groups_count":2`) || !strings.Contains(rec.Body.String(), `"active_verifications":2`) || !strings.Contains(rec.Body.String(), `"today_violations":2`) {
			t.Fatalf("scoped stats %d %s", rec.Code, rec.Body)
		}
		stats, e := fx.queries.GetScopedAdminStats(ctx, nil)
		if e != nil || stats.GroupsCount != 0 || stats.TodayViolations != 0 || stats.ActiveVerifications != 0 {
			t.Fatalf("empty scope %+v %v", stats, e)
		}
	})
	t.Run("web_unban_clears_group_ban_and_does_not_reban", func(t *testing.T) {
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
	t.Run("group_bans_preserve_legacy_and_other_group", func(t *testing.T) {
		_, e := fx.pool.Exec(ctx, `INSERT INTO banned_users(user_id,source,reason) VALUES(600,'manual','legacy global'); INSERT INTO group_ban_records(chat_id,user_id,source,reason) VALUES(-300,600,'manual','group B')`)
		if e != nil {
			t.Fatal(e)
		}
		rec := request("POST", "/api/admin/ban?chat_id=-100&user_id=600", "", scoped)
		if rec.Code != 204 {
			t.Fatalf("group ban %d %s", rec.Code, rec.Body)
		}
		var legacy string
		var count int
		if e = fx.pool.QueryRow(ctx, `SELECT reason FROM banned_users WHERE user_id=600`).Scan(&legacy); e != nil || legacy != "legacy global" {
			t.Fatalf("legacy overwritten: %q %v", legacy, e)
		}
		if e = fx.pool.QueryRow(ctx, `SELECT count(*) FROM group_ban_records WHERE user_id=600`).Scan(&count); e != nil || count != 2 {
			t.Fatalf("group bans count %d %v", count, e)
		}
		rec = request("POST", "/api/admin/unban?chat_id=-100&user_id=600", "", scoped)
		if rec.Code != 204 {
			t.Fatalf("group unban %d %s", rec.Code, rec.Body)
		}
		if e = fx.pool.QueryRow(ctx, `SELECT count(*) FROM group_ban_records WHERE user_id=600 AND chat_id=-300`).Scan(&count); e != nil || count != 1 {
			t.Fatalf("other group lost %d %v", count, e)
		}
		if e = fx.pool.QueryRow(ctx, `SELECT reason FROM banned_users WHERE user_id=600`).Scan(&legacy); e != nil || legacy != "legacy global" {
			t.Fatalf("legacy deleted: %q %v", legacy, e)
		}
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
		callsBeforeRetry := len(transport.calls())
		rec = call(id)
		for _, method := range transport.calls()[callsBeforeRetry:] {
			if strings.HasSuffix(method, "/unbanChatMember") {
				t.Fatal("resend after Telegram succeeded and settlement failed")
			}
		}
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
		for _, priorOverride := range []string{`"confirm"`, `null`} {
			setRec := request("PUT", fmt.Sprintf("/api/admin/ai-decisions/%d", id), `{"admin_override":`+priorOverride+`}`, owner)
			if setRec.Code != 200 {
				t.Fatalf("set override %s: %d %s", priorOverride, setRec.Code, setRec.Body)
			}
			setDecision, setErr := fx.queries.GetAIDecisionByID(ctx, id)
			if setErr != nil || (priorOverride == `null` && setDecision.AdminOverride != nil) || (priorOverride == `"confirm"` && (setDecision.AdminOverride == nil || *setDecision.AdminOverride != "confirm")) {
				t.Fatalf("initial override %s not persisted: %v %v", priorOverride, setDecision.AdminOverride, setErr)
			}
			priorTrust, err := fx.queries.GetUserTrust(ctx, -100, 777)
			if err != nil {
				t.Fatal(err)
			}
			callsBefore := len(transport.calls())
			rec = call(id)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"admin_override":"false_positive"`) || !strings.Contains(rec.Body.String(), `"unban_performed":false`) || !strings.Contains(rec.Body.String(), "后续封禁不会自动解除") {
				t.Fatalf("completed %s -> false_positive: %d %s", priorOverride, rec.Code, rec.Body)
			}
			current, err := fx.queries.GetAIDecisionByID(ctx, id)
			if err != nil || current.AdminOverride == nil || *current.AdminOverride != "false_positive" {
				t.Fatalf("override not saved: %+v %v", current.AdminOverride, err)
			}
			afterTrust, err := fx.queries.GetUserTrust(ctx, -100, 777)
			if err != nil || priorTrust.Score != afterTrust.Score {
				t.Fatalf("repeat review changed score: %v -> %v %v", priorTrust.Score, afterTrust.Score, err)
			}
			for _, method := range transport.calls()[callsBefore:] {
				if strings.HasSuffix(method, "/unbanChatMember") {
					t.Fatal("completed review resent unban")
				}
			}
		}
		manualResult := request("POST", "/api/admin/ban?chat_id=-100&user_id=777", "", owner)
		if manualResult.Code != 204 {
			t.Fatalf("later manual ban=%d %s", manualResult.Code, manualResult.Body)
		}
		setRec := request("PUT", fmt.Sprintf("/api/admin/ai-decisions/%d", id), `{"admin_override":"confirm"}`, owner)
		if setRec.Code != 200 {
			t.Fatalf("confirm after manual ban: %d %s", setRec.Code, setRec.Body)
		}
		priorTrust, err := fx.queries.GetUserTrust(ctx, -100, 777)
		if err != nil {
			t.Fatal(err)
		}
		callsBefore := len(transport.calls())
		rec = call(id)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"admin_override":"false_positive"`) || !strings.Contains(rec.Body.String(), `"unban_performed":false`) || !strings.Contains(rec.Body.String(), "后续封禁不会自动解除") {
			t.Fatalf("manual ban then completed review: %d %s", rec.Code, rec.Body)
		}
		for _, method := range transport.calls()[callsBefore:] {
			if strings.HasSuffix(method, "/unbanChatMember") {
				t.Fatal("completed review removed later manual ban")
			}
		}
		afterTrust, err := fx.queries.GetUserTrust(ctx, -100, 777)
		if err != nil || priorTrust.Score != afterTrust.Score {
			t.Fatalf("manual ban review changed score: %v -> %v %v", priorTrust.Score, afterTrust.Score, err)
		}
		var manualBanCount int
		if err = fx.pool.QueryRow(ctx, `SELECT count(*) FROM group_ban_records WHERE chat_id=-100 AND user_id=777 AND source='manual'`).Scan(&manualBanCount); err != nil || manualBanCount != 1 {
			t.Fatalf("manual ban record changed: %d %v", manualBanCount, err)
		}
		stale := makeDecision(784, true)
		if _, e = fx.pool.Exec(ctx, `INSERT INTO ai_unban_review_claims(decision_id,token,state,lease_until) VALUES($1,'interrupted','claimed',now()-interval '1 second')`, stale); e != nil {
			t.Fatal(e)
		}
		rec = call(stale)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unban_performed":true`) {
			t.Fatalf("expired claim retry: %d %s", rec.Code, rec.Body)
		}
		blocked := makeDecision(781, true)
		started, release := make(chan struct{}), make(chan struct{})
		transport.mu.Lock()
		transport.blockStarted, transport.blockRelease = started, release
		transport.mu.Unlock()
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- call(blocked) }()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("telegram was not reached")
		}
		rowCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, e = fx.pool.Exec(rowCtx, `UPDATE user_trust SET notes='concurrent access' WHERE chat_id=-100 AND user_id=781`)
		cancel()
		if e != nil {
			close(release)
			t.Fatalf("user row remained locked over Telegram: %v", e)
		}
		rec = call(blocked)
		if rec.Code != 409 {
			close(release)
			t.Fatalf("concurrent claim =%d %s", rec.Code, rec.Body)
		}
		close(release)
		rec = <-result
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unban_performed":true`) {
			t.Fatalf("blocked review=%d %s", rec.Code, rec.Body)
		}
		transport.mu.Lock()
		transport.blockStarted, transport.blockRelease = nil, nil
		transport.mu.Unlock()
		conflicting := makeDecision(782, true)
		started, release = make(chan struct{}), make(chan struct{})
		transport.mu.Lock()
		transport.blockStarted, transport.blockRelease = started, release
		transport.mu.Unlock()
		result = make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- call(conflicting) }()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("telegram was not reached")
		}
		if e = svc.BanChatUser(ctx, -100, 782); e != nil {
			close(release)
			t.Fatal(e)
		}
		close(release)
		rec = <-result
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "冲突") || strings.Contains(rec.Body.String(), `"unban_performed":true`) {
			t.Fatalf("manual ban conflict=%d %s", rec.Code, rec.Body)
		}
		assertStatus(-100, 782, "banned")
		var override string
		if e = fx.pool.QueryRow(ctx, `SELECT admin_override FROM ai_decisions WHERE id=$1`, conflicting).Scan(&override); e != nil || override != "false_positive" {
			t.Fatalf("conflict review not saved: %q %v", override, e)
		}
		transport.mu.Lock()
		transport.blockStarted, transport.blockRelease = nil, nil
		transport.mu.Unlock()
		beforeConflict := len(transport.calls())
		rec = call(conflicting)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "冲突") {
			t.Fatalf("conflict retry=%d %s", rec.Code, rec.Body)
		}
		for _, method := range transport.calls()[beforeConflict:] {
			if strings.HasSuffix(method, "/unbanChatMember") {
				t.Fatal("conflict retried telegram")
			}
		}
		already := makeDecision(785, true)
		if _, e = fx.pool.Exec(ctx, `WITH inserted AS (INSERT INTO ai_false_positive_reviews(decision_id) VALUES($1)) UPDATE ai_decisions SET admin_override='false_positive' WHERE id=$1`, already); e != nil {
			t.Fatal(e)
		}
		beforeLegacy := len(transport.calls())
		rec = call(already)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "无法确认当前封禁归属") {
			t.Fatalf("legacy idempotency=%d %s", rec.Code, rec.Body)
		}
		for _, method := range transport.calls()[beforeLegacy:] {
			if strings.HasSuffix(method, "/unbanChatMember") {
				t.Fatal("legacy review dispatched unban")
			}
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
	t.Run("scoped_audit_filters_before_limit", func(t *testing.T) {
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
