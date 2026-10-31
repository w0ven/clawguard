package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/store"
)

func TestRestrictedAdminCannotPreviewGlobalRules(t *testing.T) {
	s := &Server{}
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/admin/ai-prompt-preview", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		c := echo.New().NewContext(r, w)
		c.Set(adminContextKey, store.Admin{Role: "admin", GroupScope: []byte(`[-101]`)})
		if err := s.handleAIPromptPreview(c); err != nil {
			t.Fatal(err)
		}
		return w
	}
	if w := call(`{"scene":"message"}`); w.Code != 403 {
		t.Fatalf("global preview %d %s", w.Code, w.Body)
	}
	if w := call(`{"scene":"message","rules_override":"own rule"}`); w.Code != 200 || !strings.Contains(w.Body.String(), "own rule") {
		t.Fatalf("own preview %d %s", w.Code, w.Body)
	}
}
