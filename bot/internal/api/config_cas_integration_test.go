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
)

func TestConfigCASConcurrentAndScope(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	_, err := fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type,config) VALUES(-100,'one','supergroup','{"ai":{"message_rules":"before"}}'),(-200,'two','supergroup','{}'); INSERT INTO authorized_groups(chat_id,enabled) VALUES(-100,true),(-200,true)`)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 98981, Role: "owner", GroupScope: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 98982, Role: "admin", GroupScope: []byte(`[-100]`)})
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	cfg := config.Config{BotToken: "123:TEST", JWTSecret: "fixture-jwt"}
	svc, err := bot.New(ctx, cfg, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(cfg, zap.NewNop(), svc, nil)
	token := func(id int64) string {
		v, e := jwt.NewWithClaims(jwt.SigningMethodHS256, adminClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: fmt.Sprint(id), ID: fmt.Sprint(id), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(cfg.JWTSecret))
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	ownerToken, scopedToken := token(owner.ID), token(scoped.ID)
	request := func(method, path, body, tok string, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: adminCookieName, Value: tok})
		if csrf {
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf"})
			r.Header.Set("X-CSRF-Token", "csrf")
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	get := func(id int64) string {
		w := request("GET", fmt.Sprintf("/api/admin/groups/%d", id), "", ownerToken, true)
		if w.Code != 200 {
			t.Fatalf("get %d %s", w.Code, w.Body)
		}
		var v struct {
			Revision string `json:"config_revision"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil || len(v.Revision) != 64 {
			t.Fatalf("revision %q %v", v.Revision, e)
		}
		return v.Revision
	}
	path := "/api/admin/groups/-100/config"
	rev := get(-100)
	body := func(r, v string) string {
		return fmt.Sprintf(`{"expected_config_revision":%q,"config":{"ai":{"message_rules":%q}}}`, r, v)
	}
	if w := request("PUT", path, `{"config":{}}`, ownerToken, true); w.Code != 400 {
		t.Fatalf("missing revision %d", w.Code)
	}
	if w := request("PUT", path, body(rev, "x"), ownerToken, false); w.Code != 403 {
		t.Fatalf("csrf %d", w.Code)
	}
	if w := request("PUT", "/api/admin/groups/-200/config", body(get(-200), "forbidden"), scopedToken, true); w.Code != 403 {
		t.Fatalf("scope %d", w.Code)
	}
	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = request("PUT", path, body(rev, fmt.Sprintf("winner-%d", i)), ownerToken, true)
		}(i)
	}
	wg.Wait()
	a, b := results[0].Code, results[1].Code
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatalf("concurrent %d/%d %s %s", a, b, results[0].Body, results[1].Body)
	}
	group, e := fx.queries.GetGroupByChatID(ctx, -100)
	if e != nil {
		t.Fatal(e)
	}
	winner := "winner-0"
	if b == 200 {
		winner = "winner-1"
	}
	if !strings.Contains(string(group.Config), winner) {
		t.Fatalf("winner lost: %s", group.Config)
	}
	var saved struct {
		Revision string `json:"config_revision"`
	}
	success := results[0]
	if b == 200 {
		success = results[1]
	}
	if err := json.Unmarshal(success.Body.Bytes(), &saved); err != nil || saved.Revision != get(-100) || saved.Revision == rev {
		t.Fatalf("saved revision %q err %v", saved.Revision, err)
	}
	if w := request("PUT", path, body(rev, "stale"), ownerToken, true); w.Code != 409 {
		t.Fatalf("stale %d %s", w.Code, w.Body)
	}
	after, e := fx.queries.GetGroupByChatID(ctx, -100)
	if e != nil || string(after.Config) != string(group.Config) {
		t.Fatalf("stale wrote config: %s %v", after.Config, e)
	}
	// A legacy section writer changes the same document and invalidates a
	// previously loaded whole-document revision.
	beforeJoin := get(-100)
	if _, e = fx.queries.UpdateGroupJoinProtection(ctx, store.UpdateGroupJoinProtectionParams{ChatID: -100, JoinProtection: []byte(`{"enabled":true}`)}); e != nil {
		t.Fatal(e)
	}
	if w := request("PUT", path, body(beforeJoin, "stale-after-join"), ownerToken, true); w.Code != 409 {
		t.Fatalf("join writer conflict %d %s", w.Code, w.Body)
	}
	// Hold -100's row and show an unrelated group can save without waiting.
	tx, e := fx.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = store.New(tx).GetGroupByChatIDForUpdate(ctx, -100); e != nil {
		t.Fatal(e)
	}
	rev2 := get(-200)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request("PUT", "/api/admin/groups/-200/config", body(rev2, "other"), ownerToken, true) }()
	select {
	case w := <-done:
		if w.Code != 200 {
			t.Fatalf("other group %d %s", w.Code, w.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("different group blocked")
	}
}

func TestGlobalSectionCASConcurrent(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	cfg := config.Config{BotToken: "123:TEST", JWTSecret: "fixture"}
	svc, err := bot.New(ctx, cfg, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(cfg, zap.NewNop(), svc, nil)
	admin, err := fx.queries.UpsertAdmin(ctx, store.UpsertAdminParams{TelegramID: 98983, Role: "owner", GroupScope: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, adminClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: fmt.Sprint(admin.ID), ID: fmt.Sprint(admin.ID), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/admin/global-config", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: adminCookieName, Value: token})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf"})
		r.Header.Set("X-CSRF-Token", "csrf")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	global, err := fx.queries.GetGlobalConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(`{"section":"prompt","config":{"ai":{"message_rules":"x"}}}`); w.Code != 400 {
		t.Fatalf("missing version %d %s", w.Code, w.Body)
	}
	var wg sync.WaitGroup
	res := make([]*httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res[i] = call(fmt.Sprintf(`{"section":"prompt","expected_version":%d,"config":{"ai":{"message_rules":"choice-%d"}}}`, global.Version, i))
		}(i)
	}
	wg.Wait()
	a, b := res[0].Code, res[1].Code
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatalf("section concurrent %d/%d %s %s", a, b, res[0].Body, res[1].Body)
	}
	after, err := fx.queries.GetGlobalConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	winner := "choice-0"
	if b == 200 {
		winner = "choice-1"
	}
	if after.Version != global.Version+1 || !strings.Contains(string(after.Config), winner) {
		t.Fatalf("lost winner version=%d config=%s", after.Version, after.Config)
	}
	success := res[0]
	if b == 200 {
		success = res[1]
	}
	var saved struct {
		Version int64 `json:"version"`
	}
	if err := json.Unmarshal(success.Body.Bytes(), &saved); err != nil || saved.Version != after.Version {
		t.Fatalf("saved version %d err %v", saved.Version, err)
	}
}

func TestModelCleanupRereadsLockedGroupConfig(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	_, err := fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type,config) VALUES(-100,'one','supergroup','{"ai":{"primary_model_ref":"newapi:glm-5","message_rules":"old"}}')`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fx.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = store.New(tx).GetGroupByChatIDForUpdate(ctx, -100); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runCleanupInDeleteTx(ctx, fx.queries, fx.admin, fx.target) }()
	// Cleanup is forced to wait on the row even if it has already enumerated it.
	time.Sleep(150 * time.Millisecond)
	if _, err = tx.Exec(ctx, `UPDATE groups SET config='{"ai":{"primary_model_ref":"newapi:glm-5","message_rules":"new admin value"}}' WHERE chat_id=-100`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	g, err := fx.queries.GetGroupByChatID(ctx, -100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(g.Config), "new admin value") || strings.Contains(string(g.Config), "newapi:glm-5") {
		t.Fatalf("cleanup lost concurrent edit or retained model: %s", g.Config)
	}
}
