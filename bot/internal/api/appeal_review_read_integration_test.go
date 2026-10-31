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

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

func TestAppealReviewReadScope(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	_, err := fx.pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES(-101,'group one','supergroup'),(-102,'group two','supergroup'); INSERT INTO admins(telegram_id,role,group_scope) VALUES(99,'admin','[-101]')`)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := fx.queries.GetAdminByTelegramID(ctx, 99)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for i, chat := range []int64{-101, -102} {
		id, e := fx.queries.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: fmt.Sprintf("test-read-%d", i), SourceKind: "test", SourceID: fmt.Sprint(i), ChatID: chat, UserID: 101, Action: "ban", Result: "partial", Explanation: "only deleted", Ownership: []byte(`{"secret":"private"}`)})
		if e != nil {
			t.Fatal(e)
		}
		var aid int64
		e = fx.pool.QueryRow(ctx, `INSERT INTO appeals(case_id,chat_id,user_id,reason) VALUES($1,$2,101,'reason') RETURNING id`, id, chat).Scan(&aid)
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, aid)
	}
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	cfg := config.Config{BotToken: "123:TEST", JWTSecret: "fixture-only"}
	svc, err := bot.New(ctx, cfg, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	srv := NewServer(cfg, zap.NewNop(), svc, nil)
	token, err := srv.signAdminJWT(admin)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, withToken bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if withToken {
			r.AddCookie(&http.Cookie{Name: adminCookieName, Value: token})
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	if w := get("/api/admin/appeals", false); w.Code != 401 {
		t.Fatalf("missing auth %d", w.Code)
	}
	w := get("/api/admin/appeals", true)
	var data struct {
		Appeals []map[string]any `json:"appeals"`
		Pending int              `json:"pending_count"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data.Appeals) != 1 || data.Pending != 1 || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	if w = get(fmt.Sprintf("/api/admin/appeals/%d", ids[0]), true); w.Code != 200 || !strings.Contains(w.Body.String(), "only deleted") {
		t.Fatalf("detail %d %s", w.Code, w.Body.String())
	}
	if w = get(fmt.Sprintf("/api/admin/appeals/%d", ids[1]), true); w.Code != 404 {
		t.Fatalf("cross scope detail %d", w.Code)
	}
	if w = get("/api/admin/appeals?chat_id=-102", true); w.Code != 403 {
		t.Fatalf("cross scope filter %d", w.Code)
	}
	post := func(id int64, body string, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/admin/appeals/%d/decision", id), strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: adminCookieName, Value: token})
		if csrf {
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "t"})
			r.Header.Set("X-CSRF-Token", "t")
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	if w = post(ids[0], `{"decision":"uphold","note":"note"}`, false); w.Code != 403 {
		t.Fatalf("csrf %d %s", w.Code, w.Body)
	}
	if w = post(ids[1], `{"decision":"uphold","note":"note"}`, true); w.Code != 404 {
		t.Fatalf("cross write %d %s", w.Code, w.Body)
	}
	if w = post(ids[0], `{"decision":"uphold","note":" "}`, true); w.Code != 400 {
		t.Fatalf("missing note %d %s", w.Code, w.Body)
	}
	if w = post(ids[0], `{"decision":"approve","note":"approved"}`, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"manual_review"`) {
		t.Fatalf("partial approval %d %s", w.Code, w.Body)
	}
	if w = post(ids[0], `{"decision":"uphold","note":"different"}`, true); w.Code != 409 {
		t.Fatalf("override %d %s", w.Code, w.Body)
	}
	var notes int
	if err = fx.pool.QueryRow(ctx, `SELECT count(*) FROM moderation_notifications WHERE appeal_id=$1 AND event_kind='appeal_result' AND state='unreachable'`, ids[0]).Scan(&notes); err != nil || notes != 1 {
		t.Fatalf("event %d %v", notes, err)
	}
	caseID, e := fx.queries.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: "concurrent-decision", SourceKind: "test", SourceID: "concurrent", ChatID: -101, UserID: 102, Action: "ban", Result: "executed"})
	if e != nil {
		t.Fatal(e)
	}
	var aid int64
	if e = fx.pool.QueryRow(ctx, `INSERT INTO appeals(case_id,chat_id,user_id,reason) VALUES($1,-101,102,'reason') RETURNING id`, caseID).Scan(&aid); e != nil {
		t.Fatal(e)
	}
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, body := range []string{`{"decision":"approve","note":"yes"}`, `{"decision":"uphold","note":"no"}`} {
		wg.Add(1)
		go func(b string) { defer wg.Done(); codes <- post(aid, b, true).Code }(body)
	}
	wg.Wait()
	close(codes)
	seen := map[int]int{}
	for code := range codes {
		seen[code]++
	}
	if seen[200] != 1 || seen[409] != 1 {
		t.Fatalf("concurrent decisions %v", seen)
	}
	var audits int
	if e = fx.pool.QueryRow(ctx, `SELECT count(*) FROM config_audit WHERE scope='appeal' AND action='appeal_decision' AND after->>'appeal_id'=$1`, fmt.Sprint(aid)).Scan(&audits); e != nil || audits != 1 {
		t.Fatalf("audit %d %v", audits, e)
	}
	var warnID, otherWarn int64
	if e = fx.pool.QueryRow(ctx, `INSERT INTO warnings(chat_id,user_id) VALUES(-101,103) RETURNING id`).Scan(&warnID); e != nil {
		t.Fatal(e)
	}
	if e = fx.pool.QueryRow(ctx, `INSERT INTO warnings(chat_id,user_id) VALUES(-101,103) RETURNING id`).Scan(&otherWarn); e != nil {
		t.Fatal(e)
	}
	warnCase, e := fx.queries.RecordModerationCase(ctx, store.ModerationCaseInput{SourceKey: "warning-review", SourceKind: "test", SourceID: "warning", ChatID: -101, UserID: 103, Action: "warn", Result: "executed", Ownership: []byte(fmt.Sprintf(`{"warning_id":%d}`, warnID))})
	if e != nil {
		t.Fatal(e)
	}
	var warnAppeal int64
	if e = fx.pool.QueryRow(ctx, `INSERT INTO appeals(case_id,chat_id,user_id,reason) VALUES($1,-101,103,'reason') RETURNING id`, warnCase).Scan(&warnAppeal); e != nil {
		t.Fatal(e)
	}
	if w = post(warnAppeal, `{"decision":"approve","note":"warning review"}`, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"completed"`) {
		t.Fatalf("warning approval %d %s", w.Code, w.Body)
	}
	var targetConsumed, otherConsumed bool
	if e = fx.pool.QueryRow(ctx, `SELECT (SELECT consumed_at IS NOT NULL FROM warnings WHERE id=$1),(SELECT consumed_at IS NOT NULL FROM warnings WHERE id=$2)`, warnID, otherWarn).Scan(&targetConsumed, &otherConsumed); e != nil || !targetConsumed || otherConsumed {
		t.Fatalf("warning isolation %v %v %v", targetConsumed, otherConsumed, e)
	}
}
