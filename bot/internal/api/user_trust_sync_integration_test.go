//go:build integration

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

type trustSyncTransport struct {
	mu               sync.Mutex
	fail             string
	calls            []string
	started, release chan struct{}
}

func (t *trustSyncTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.calls = append(t.calls, r.URL.Path)
	fail := t.fail != "" && strings.HasSuffix(r.URL.Path, t.fail)
	started, release := t.started, t.release
	t.mu.Unlock()
	if started != nil && strings.HasSuffix(r.URL.Path, "/unbanChatMember") {
		close(started)
		<-release
	}
	if fail {
		return nil, fmt.Errorf("injected telegram failure")
	}
	return (exemptTelegramTransport{}).RoundTrip(r)
}
func (t *trustSyncTransport) count(s string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, x := range t.calls {
		if strings.HasSuffix(x, s) {
			n++
		}
	}
	return n
}
func (t *trustSyncTransport) setFailure(s string) { t.mu.Lock(); defer t.mu.Unlock(); t.fail = s }
func TestTrustStatusSyncIntegration(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-100,'test','supergroup'); INSERT INTO authorized_groups(chat_id) VALUES(-100); INSERT INTO user_trust(chat_id,user_id,status,score) VALUES(-100,101,'banned',0)`); err != nil {
		t.Fatal(err)
	}
	tr := &trustSyncTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	server := NewServer(config.Config{}, zap.NewNop(), svc, nil)
	call := func(status string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/api/admin/user-trust/-100/101", strings.NewReader(fmt.Sprintf(`{"status":%q,"score":0.5}`, status)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.Set(adminContextKey, fx.admin)
		c.SetParamNames("chat_id", "user_id")
		c.SetParamValues("-100", "101")
		if err := server.handleUpdateUserTrust(c); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	tr.setFailure("/unbanChatMember")
	rec := call("trusted")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "telegram_action_error") {
		t.Fatalf("failed unban: %d %s", rec.Code, rec.Body)
	}
	u, err := fx.queries.GetUserTrust(ctx, -100, 101)
	if err != nil || u.Status != "banned" {
		t.Fatalf("status before sync: %+v %v", u, err)
	}
	tr.setFailure("/restrictChatMember")
	rec = call("trusted")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "telegram_permission_error") {
		t.Fatalf("failed restore: %d %s", rec.Code, rec.Body)
	}
	before := tr.count("/unbanChatMember")
	tr.setFailure("")
	rec = call("trusted")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "telegram_") {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	u, err = fx.queries.GetUserTrust(ctx, -100, 101)
	if err != nil || u.Status != "trusted" {
		t.Fatalf("trusted status: %+v %v", u, err)
	}
	rec = call("trusted")
	if rec.Code != 200 || tr.count("/unbanChatMember") != before || tr.count("/restrictChatMember") != 2 {
		t.Fatalf("duplicate external action: %s", rec.Body)
	}
	if _, err = fx.pool.Exec(ctx, `UPDATE user_trust SET status='banned',status_changed_at=now() WHERE chat_id=-100 AND user_id=101; CREATE FUNCTION fail_trust_settle() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'injected'; END$$; CREATE TRIGGER fail_trust_settle BEFORE UPDATE ON user_trust FOR EACH ROW EXECUTE FUNCTION fail_trust_settle()`); err != nil {
		t.Fatal(err)
	}
	rec = call("new")
	if rec.Code != 500 {
		t.Fatalf("settle failure: %d %s", rec.Code, rec.Body)
	}
	if _, err = fx.pool.Exec(ctx, `DROP TRIGGER fail_trust_settle ON user_trust; DROP FUNCTION fail_trust_settle(); UPDATE user_trust_sync SET lease_until=now()-interval '1 second' WHERE chat_id=-100 AND user_id=101`); err != nil {
		t.Fatal(err)
	}
	unbans := tr.count("/unbanChatMember")
	rec = call("new")
	if rec.Code != 200 || tr.count("/unbanChatMember") != unbans {
		t.Fatalf("settle retry: %d %s", rec.Code, rec.Body)
	}
	if _, err = fx.pool.Exec(ctx, `UPDATE user_trust SET status='banned',status_changed_at=now() WHERE chat_id=-100 AND user_id=101`); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	tr.mu.Lock()
	tr.started, tr.release = started, release
	tr.mu.Unlock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call("new") }()
	<-started
	if e := svc.BanChatUser(ctx, -100, 101); e != nil {
		close(release)
		t.Fatal(e)
	}
	close(release)
	rec = <-done
	if rec.Code != 500 {
		t.Fatalf("manual ban conflict: %d %s", rec.Code, rec.Body)
	}
	tr.mu.Lock()
	tr.started, tr.release = nil, nil
	tr.mu.Unlock()
	u, err = fx.queries.GetUserTrust(ctx, -100, 101)
	if err != nil || u.Status != "banned" {
		t.Fatalf("manual ban overwritten: %+v %v", u, err)
	}
	if _, err = fx.pool.Exec(ctx, `UPDATE user_trust SET status='new',status_changed_at=now() WHERE chat_id=-100 AND user_id=101`); err != nil {
		t.Fatal(err)
	}
	tr.setFailure("/kickChatMember")
	rec = call("banned")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "telegram_action_error") {
		t.Fatalf("ban failure: %d %s", rec.Code, rec.Body)
	}
	u, err = fx.queries.GetUserTrust(ctx, -100, 101)
	if err != nil || u.Status != "new" {
		t.Fatalf("failed ban committed: %+v %v", u, err)
	}
	tr.setFailure("")
	rec = call("banned")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "telegram_action_error") {
		t.Fatalf("ban retry %d %s", rec.Code, rec.Body)
	}
	kicks := tr.count("/kickChatMember")
	rec = call("banned")
	if rec.Code != 200 || tr.count("/kickChatMember") != kicks {
		t.Fatalf("ban duplicate %d %s", rec.Code, rec.Body)
	}
	u, err = fx.queries.GetUserTrust(ctx, -100, 101)
	if err != nil || u.Status != "banned" {
		t.Fatalf("ban status %+v %v", u, err)
	}
	tr.setFailure("/unbanChatMember")
	rec = call("new")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "telegram_action_error") {
		t.Fatalf("pending failure %d %s", rec.Code, rec.Body)
	}
	if _, err = fx.pool.Exec(ctx, `UPDATE user_trust SET status='suspicious',status_changed_at=now() WHERE chat_id=-100 AND user_id=101`); err != nil {
		t.Fatal(err)
	}
	tr.setFailure("")
	rec = call("new")
	if rec.Code != 409 {
		t.Fatalf("changed status not detected %d %s", rec.Code, rec.Body)
	}
	rec = call("new")
	if rec.Code != 200 {
		t.Fatalf("conflict could not recover %d %s", rec.Code, rec.Body)
	}
	before = tr.count("/unbanChatMember")
	rec = call("nonsense")
	if rec.Code != 400 || tr.count("/unbanChatMember") != before {
		t.Fatalf("invalid status: %d %s", rec.Code, rec.Body)
	}
}

func TestConcurrentAIDecisionScoringIntegration(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	_, err := fx.pool.Exec(ctx, `INSERT INTO user_trust(chat_id,user_id,status,score) VALUES(-100,104,'new',0.5); INSERT INTO ai_decisions(chat_id,user_id,message_id,model,prompt_version,verdict,confidence,category,action_taken) VALUES(-100,104,3,'x','x','spam',1,'spam','warn'); CREATE FUNCTION delay_override_probe() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_sleep(0.1); RETURN NEW; END$$; CREATE TRIGGER delay_override_probe BEFORE UPDATE ON ai_decisions FOR EACH ROW EXECUTE FUNCTION delay_override_probe()`)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = fx.pool.QueryRow(ctx, `SELECT id FROM ai_decisions WHERE user_id=104`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tr := &trustSyncTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	server := NewServer(config.Config{}, zap.NewNop(), svc, nil)
	call := func(value string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/", strings.NewReader(fmt.Sprintf(`{"admin_override":%q}`, value)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.Set(adminContextKey, fx.admin)
		c.SetParamNames("id")
		c.SetParamValues(fmt.Sprint(id))
		_ = server.handleUpdateAIDecision(c)
		return rec
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); codes[i] = call("confirm").Code }(i)
	}
	wg.Wait()
	var score float64
	if err = fx.pool.QueryRow(ctx, `SELECT score FROM user_trust WHERE chat_id=-100 AND user_id=104`).Scan(&score); err != nil || score < 0.399 || score > 0.401 || codes[0] != 200 || codes[1] != 200 {
		t.Fatalf("score %v statuses %v err=%v", score, codes, err)
	}
	if _, err = fx.pool.Exec(ctx, `CREATE FUNCTION fail_score_probe() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'injected'; END$$; CREATE TRIGGER fail_score_probe BEFORE UPDATE ON user_trust FOR EACH ROW EXECUTE FUNCTION fail_score_probe()`); err != nil {
		t.Fatal(err)
	}
	rec := call("false_negative")
	if rec.Code != 500 {
		t.Fatalf("failed score: %d %s", rec.Code, rec.Body)
	}
	var override string
	if err = fx.pool.QueryRow(ctx, `SELECT admin_override FROM ai_decisions WHERE id=$1`, id).Scan(&override); err != nil || override != "confirm" {
		t.Fatalf("override committed on score failure: %s %v", override, err)
	}
	if _, err = fx.pool.Exec(ctx, `DROP TRIGGER fail_score_probe ON user_trust; DROP FUNCTION fail_score_probe()`); err != nil {
		t.Fatal(err)
	}
	rec = call("false_negative")
	if rec.Code != 200 {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	if err = fx.pool.QueryRow(ctx, `SELECT score FROM user_trust WHERE chat_id=-100 AND user_id=104`).Scan(&score); err != nil || score < 0.199 || score > 0.201 {
		t.Fatalf("retry score %v %v", score, err)
	}
}

func TestScopedAIDecisionLimitIntegration(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	_, err := fx.pool.Exec(ctx, `INSERT INTO ai_decisions(chat_id,user_id,message_id,model,prompt_version,verdict,confidence,category,action_taken,created_at) VALUES(-100,102,1,'x','x','spam',1,'spam','warn',now()-interval '2 minute'),(-300,104,3,'x','x','spam',1,'spam','warn',now()-interval '1 minute'),(-200,103,2,'x','x','spam',1,'spam','warn',now())`)
	if err != nil {
		t.Fatal(err)
	}
	tr := &trustSyncTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	server := NewServer(config.Config{}, zap.NewNop(), svc, nil)
	call := func(query string, admin store.Admin) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/admin/ai-decisions?"+query, nil)
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.Set(adminContextKey, admin)
		if e := server.handleListAIDecisions(c); e != nil {
			t.Fatal(e)
		}
		return rec
	}
	scoped := store.Admin{Role: "admin", GroupScope: []byte(`[-100,-300]`)}
	rec := call("limit=1", scoped)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user_id":104`) || strings.Contains(rec.Body.String(), `"user_id":103`) {
		t.Fatalf("scope=%d %s", rec.Code, rec.Body)
	}
	rec = call("limit=1&chat_id=-100", scoped)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user_id":102`) {
		t.Fatalf("explicit scope=%d %s", rec.Code, rec.Body)
	}
	rec = call("limit=1&chat_id=-200", scoped)
	if rec.Code != 403 {
		t.Fatalf("unprivileged scope=%d %s", rec.Code, rec.Body)
	}
	items, e := fx.queries.ListAIDecisions(ctx, store.ListAIDecisionsParams{Limit: 1, ScopeGlobal: false, ScopeChatIDs: []int64{}})
	if e != nil || len(items) != 0 {
		t.Fatalf("empty SQL scope: %v %v", items, e)
	}
	rec = call("limit=1", store.Admin{Role: "admin", GroupScope: []byte(`[]`)})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user_id":103`) {
		t.Fatalf("empty admin scope retains global semantics: %d %s", rec.Code, rec.Body)
	}
	rec = call("limit=1", fx.admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user_id":103`) {
		t.Fatalf("owner=%d %s", rec.Code, rec.Body)
	}
}

func TestLegacyAIMuteDoesNotAutoUnmute(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	var id int64
	if err := fx.pool.QueryRow(ctx, `INSERT INTO ai_decisions(chat_id,user_id,message_id,model,prompt_version,verdict,confidence,category,action_taken) VALUES(-100,501,1,'x','x','spam',1,'spam','mute') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tr := &trustSyncTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = tr
	t.Cleanup(func() { http.DefaultTransport = old })
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	s := NewServer(config.Config{}, zap.NewNop(), svc, nil)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("PUT", "/", strings.NewReader(`{"admin_override":"false_positive"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.Set(adminContextKey, fx.admin)
		c.SetParamNames("id")
		c.SetParamValues(fmt.Sprint(id))
		if err = s.handleUpdateAIDecision(c); err != nil || rec.Code != 200 || !strings.Contains(rec.Body.String(), "无法确认当前禁言归属") || tr.count("/restrictChatMember") != 0 {
			t.Fatalf("legacy mute %d %s %v", rec.Code, rec.Body, err)
		}
	}
	var score float64
	if err = fx.pool.QueryRow(ctx, `SELECT score FROM user_trust WHERE chat_id=-100 AND user_id=501`).Scan(&score); err != nil || score < 0.599 || score > 0.601 {
		t.Fatalf("legacy repeat score %v %v", score, err)
	}
}
