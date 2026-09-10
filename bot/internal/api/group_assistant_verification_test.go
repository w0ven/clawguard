package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

var assistantVerificationRoutes = []struct{ method, path string }{{"GET", ""}, {"PUT", ""}, {"GET", "/model-pool"}, {"PUT", "/model-pool"}, {"GET", "/status"}, {"GET", "/tools"}, {"GET", "/memories"}, {"POST", "/memories"}, {"GET", "/memories/1"}, {"PUT", "/memories/1"}, {"POST", "/memories/1/forget"}, {"GET", "/memories/1/versions"}, {"GET", "/conflicts"}, {"POST", "/conflicts/1/resolve"}, {"GET", "/history"}, {"GET", "/dispatches"}, {"GET", "/prompts"}, {"PUT", "/prompts"}, {"GET", "/native"}, {"PUT", "/native"}, {"GET", "/native/memories"}, {"POST", "/native/memories"}}

func TestAssistantVerificationAPINoJWT(t *testing.T) {
	s := NewServer(config.Config{JWTSecret: "assistant-test-fake-secret"}, zap.NewNop(), nil, nil)
	for _, route := range assistantVerificationRoutes {
		for _, token := range []string{"", "not-a-valid-jwt"} {
			t.Run(route.method+route.path+token, func(t *testing.T) {
				req := httptest.NewRequest(route.method, "/api/admin/groups/-1/assistant"+route.path, strings.NewReader(`{}`))
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				rec := httptest.NewRecorder()
				s.echo.ServeHTTP(rec, req)
				if rec.Code != 401 {
					t.Fatalf("expected JWT refusal: %d %s", rec.Code, rec.Body)
				}
				var out map[string]any
				if e := json.Unmarshal(rec.Body.Bytes(), &out); e != nil || out["error"] == nil {
					t.Fatalf("nonstructured error %s", rec.Body)
				}
			})
		}
	}
}

func TestAssistantGlobalVerificationAPINoJWT(t *testing.T) {
	s := NewServer(config.Config{JWTSecret: "assistant-test-fake-secret"}, zap.NewNop(), nil, nil)
	routes := []struct{ method, path string }{
		{"GET", "/api/admin/assistant/global"},
		{"PUT", "/api/admin/assistant/global"},
		{"GET", "/api/admin/assistant/prompts"},
		{"PUT", "/api/admin/assistant/prompts"},
		{"GET", "/api/admin/assistant/native"},
		{"PUT", "/api/admin/assistant/native"},
	}
	for _, route := range routes {
		for _, token := range []string{"", "not-a-valid-jwt"} {
			t.Run(route.method+route.path+token, func(t *testing.T) {
				req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				rec := httptest.NewRecorder()
				s.echo.ServeHTTP(rec, req)
				if rec.Code != 401 {
					t.Fatalf("expected JWT refusal: %d %s", rec.Code, rec.Body)
				}
				var out map[string]any
				if e := json.Unmarshal(rec.Body.Bytes(), &out); e != nil || out["error"] == nil {
					t.Fatalf("nonstructured error %s", rec.Body)
				}
			})
		}
	}
}
func TestAssistantVerificationAPIOutOfScopeAllRoutes(t *testing.T) {
	s := &Server{echo: echo.New(), logger: zap.NewNop()}
	g := s.echo.Group("/api/admin", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(adminContextKey, store.Admin{ID: 1, TelegramID: 11, Role: "admin", GroupScope: []byte(`[-2]`)})
			return next(c)
		}
	})
	s.registerGroupAssistantRoutes(g)
	s.registerNativeAssistantRoutes(g)
	for _, route := range assistantVerificationRoutes {
		t.Run(route.method+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, "/api/admin/groups/-1/assistant"+route.path, strings.NewReader(`{}`))
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			if rec.Code != 403 {
				t.Fatalf("scope must refuse before service/database: %d %s", rec.Code, rec.Body)
			}
		})
	}
}
func TestAssistantVerificationAPICSRF(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name, method, auth, cookie, header string
		want                               int
	}{{"cookie missing csrf", "PUT", "", "session", "", 403}, {"cookie mismatched csrf", "POST", "", "session", "wrong", 403}, {"cookie valid csrf", "PUT", "", "session", "token", 204}, {"bearer cookie still csrf", "PUT", "Bearer fake", "session", "", 403}, {"bearer-only existing exemption", "PUT", "Bearer fake", "", "", 204}, {"read only", "GET", "", "session", "", 204}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/admin/groups/-1/assistant", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: adminCookieName, Value: tc.cookie})
			}
			if tc.header != "" {
				req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token"})
				req.Header.Set("X-CSRF-Token", tc.header)
			}
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(req, rec)
			if e := s.requireCSRF(func(c echo.Context) error { return c.NoContent(204) })(c); e != nil {
				t.Fatal(e)
			}
			if rec.Code != tc.want {
				t.Fatalf("CSRF %d want %d", rec.Code, tc.want)
			}
		})
	}
}
func TestAssistantVerificationAPIStrictJSON(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		target     func() any
	}{{"trailing policy object", `{} {}`, func() any { return &groupAssistantPolicyRequest{} }}, {"trailing memory object", `{"subject":"x"} {"admin_id":2}`, func() any { return &assistantMemoryRequest{} }}, {"spoof policy chat", `{"chat_id":-2}`, func() any { return &groupAssistantPolicyRequest{} }}, {"spoof policy provider", `{"base_url":"http://other","api_key":"fake"}`, func() any { return &groupAssistantPolicyRequest{} }}, {"spoof memory admin", `{"source_operator_id":2}`, func() any { return &assistantMemoryRequest{} }}, {"spoof memory authority", `{"authority_level":"pinned_announcement"}`, func() any { return &assistantMemoryRequest{} }}, {"spoof memory chat", `{"chat_id":-2}`, func() any { return &assistantMemoryRequest{} }}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/", strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			c := echo.New().NewContext(req, httptest.NewRecorder())
			if e := bindAssistantJSON(c, tc.target()); e == nil {
				t.Fatal("unsafe/extra JSON accepted")
			}
		})
	}
}
