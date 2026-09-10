package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
)

func TestNativeAssistantHTTPRequiresJWTAndGlobalAdmin(t *testing.T) {
	s := NewServer(config.Config{JWTSecret: "assistant-test-fake-secret"}, zap.NewNop(), nil, nil)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/admin/assistant/native"},
		{"PUT", "/api/admin/assistant/native"},
		{"GET", "/api/admin/groups/-1/assistant/native"},
		{"PUT", "/api/admin/groups/-1/assistant/native"},
	} {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("%s %s expected 401, got %d %s", route.method, route.path, rec.Code, rec.Body)
		}
	}
	server := &Server{echo: echo.New(), logger: zap.NewNop()}
	admin := server.echo.Group("/api/admin", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(adminContextKey, store.Admin{ID: 1, TelegramID: 11, Role: "admin", GroupScope: []byte(`[-3001]`)})
			return next(c)
		}
	})
	server.registerNativeAssistantRoutes(admin)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/assistant/native", strings.NewReader(`{"revision":1}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("scoped admin wrote global native config: %d %s", rec.Code, rec.Body)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/admin/groups/-1/assistant/native", nil)
	rec = httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("out-of-scope native group GET: %d %s", rec.Code, rec.Body)
	}
}
