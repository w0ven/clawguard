package api

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
	"go.uber.org/zap"
)

type countingReader struct {
	r io.Reader
	n int
}

func (r *countingReader) Read(p []byte) (int, error) { n, e := r.r.Read(p); r.n += n; return n, e }

func TestAnonymousRequestBodiesAreBounded(t *testing.T) {
	cfg := config.Config{WebhookSecret: "test-secret"}
	srv := NewServer(cfg, zap.NewNop(), nil, nil)
	for _, tc := range []struct {
		path, contentType string
		limit             int
	}{
		{"/api/public/magic/exchange", "application/json", 65536},
		{"/api/auth/miniapp", "application/json", 65536},
		{"/api/auth/web-challenge", "application/json", 4096},
		{"/api/auth/web-approve", "application/json", 65536},
		{"/api/auth/web-poll", "application/json", 4096},
		{"/api/auth/telegram-login", "application/x-www-form-urlencoded", 65536},
		{"/api/verify/turnstile/test-token", "application/json", 16384},
		{"/webhook/" + cfg.WebhookPathToken(), "application/json", 4 << 20},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body := strings.Repeat("x", tc.limit*3)
			if strings.Contains(tc.path, "/turnstile/") {
				body = `{"cf_response":"` + body + `"}`
			}
			raw := &countingReader{r: strings.NewReader(body)}
			req := httptest.NewRequest("POST", tc.path, raw)
			req.ContentLength = -1
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("X-Telegram-Bot-Api-Secret-Token", cfg.WebhookHeaderToken())
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code < 400 {
				t.Fatalf("oversize accepted: %d", w.Code)
			}
			if raw.n > tc.limit+32768 {
				t.Fatalf("read %d beyond limit %d", raw.n, tc.limit)
			}
		})
	}
}
