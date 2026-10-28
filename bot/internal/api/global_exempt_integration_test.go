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
)

type exemptTelegramTransport struct{}

func (exemptTelegramTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.telegram.org" || !strings.HasSuffix(r.URL.Path, "/getMe") {
		return nil, fmt.Errorf("unexpected network request: %s", r.URL)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"fixture"}}`))}, nil
}

func TestGlobalExemptAPIIntegration(t *testing.T) {
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
	old := http.DefaultTransport
	http.DefaultTransport = exemptTelegramTransport{}
	svc, err := bot.New(ctx, config.Config{BotToken: "123:TEST"}, zap.NewNop(), fx.queries, nil, nil, nil, nil)
	http.DefaultTransport = old
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
	base := "/api/admin/global-exempt-users"
	id := "12345678901234567"
	path := base + "/" + id
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		url := path
		if m == "GET" {
			url = base
		}
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
	if rec := request("PUT", path, `{"note":"fixture","expires_at":null}`, owner, true); rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("GET", base, "", owner, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user_id":"12345678901234567"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("GET", "/api/admin/audit", "", scoped, false); rec.Code != 200 || strings.Contains(rec.Body.String(), id) {
		t.Fatalf("scoped audit leaked global exemption: %d %s", rec.Code, rec.Body.String())
	}
	active, banned, err := store.New(fx.pool).GlobalExemptionStatus(ctx, -100, 12345678901234567)
	if err != nil || !active || banned {
		t.Fatalf("persisted: %v %v %v", active, banned, err)
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
	if err = goose.UpContext(ctx, db, h1MigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	active, _, err = fx.queries.GlobalExemptionStatus(ctx, -100, 12345678901234567)
	if err != nil || !active {
		t.Fatalf("rollback/re-upgrade lost record: active=%v err=%v", active, err)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if rec := request("PUT", path, `{"note":"edited","expires_at":"`+future+`"}`, owner, true); rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE global_exempt_users SET expires_at=now()-interval '1 second' WHERE user_id=$1`, int64(12345678901234567)); err != nil {
		t.Fatal(err)
	}
	active, _, err = fx.queries.GlobalExemptionStatus(ctx, -100, 12345678901234567)
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
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM config_audit WHERE action IN ('upsert_global_exempt_user','delete_global_exempt_user')`).Scan(&audit); err != nil || audit != 3 {
		t.Fatalf("audit count=%d err=%v", audit, err)
	}
}
