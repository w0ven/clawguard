package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostDefaultMiniAppMenuButton(t *testing.T) {
	const miniAppURL = "https://example.com/miniapp"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
			t.Fatalf("content-type = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		var button struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			WebApp struct {
				URL string `json:"url"`
			} `json:"web_app"`
		}
		if err := json.Unmarshal([]byte(r.FormValue("menu_button")), &button); err != nil {
			t.Fatalf("decode menu_button: %v", err)
		}
		if button.Type != "web_app" || button.Text != "管理面板" || button.WebApp.URL != miniAppURL {
			t.Fatalf("unexpected menu button: %+v", button)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	if err := postDefaultMiniAppMenuButton(context.Background(), server.Client(), server.URL, miniAppURL); err != nil {
		t.Fatalf("post menu button: %v", err)
	}
}

func TestPostDefaultMiniAppMenuButtonRejectsTelegramError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request"}`))
	}))
	defer server.Close()

	err := postDefaultMiniAppMenuButton(context.Background(), server.Client(), server.URL, "https://example.com/miniapp")
	if err == nil || !strings.Contains(err.Error(), "Bad Request") {
		t.Fatalf("error = %v, want Telegram rejection", err)
	}
}
