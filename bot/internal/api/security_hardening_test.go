package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

func TestTrustedClientIPExtractorRejectsUntrustedSpoofingAndPeelsRightmostProxy(t *testing.T) {
	e := echo.New()
	e.IPExtractor = newTrustedClientIPExtractor()

	// Case 1: Direct connection from public IP trying to spoof X-Forwarded-For and X-Real-IP.
	reqDirect := httptest.NewRequest(http.MethodGet, "/api/auth/telegram-login", nil)
	reqDirect.RemoteAddr = "198.51.100.77:52341"
	reqDirect.Header.Set("X-Forwarded-For", "1.1.1.1")
	reqDirect.Header.Set("X-Real-IP", "8.8.8.8")
	cDirect := e.NewContext(reqDirect, httptest.NewRecorder())
	if got := cDirect.RealIP(); got != "198.51.100.77" {
		t.Fatalf("direct public peer RealIP() = %q, want 198.51.100.77 (spoofed headers must be ignored)", got)
	}

	// Case 2: Request arriving via local Caddy / Docker bridge with client-prepended spoofed XFF.
	reqProxied := httptest.NewRequest(http.MethodGet, "/api/auth/telegram-login", nil)
	reqProxied.RemoteAddr = "172.18.0.6:41234"
	reqProxied.Header.Set("X-Forwarded-For", "9.9.9.9, 203.0.113.42, 127.0.0.1")
	cProxied := e.NewContext(reqProxied, httptest.NewRecorder())
	if got := cProxied.RealIP(); got != "203.0.113.42" {
		t.Fatalf("proxied RealIP() = %q, want rightmost non-trusted IP 203.0.113.42", got)
	}
}

func TestMagicExchangeIPRateLimitReturns429(t *testing.T) {
	redisClient := newAPIFakeRedisClient(t)
	limiter := newRateLimiter(redisClient, zap.NewNop())
	server := &Server{}

	e := echo.New()
	e.IPExtractor = newTrustedClientIPExtractor()
	e.POST("/api/public/magic/exchange", func(c echo.Context) error {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
	}, limiter.middleware(server.ipRateLimitKey("auth:magic_exchange_ip"), magicExchangeIPLimit, apiRateLimitWindow))

	for i := 0; i < magicExchangeIPLimit; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/public/magic/exchange", strings.NewReader(`{"token":"bad"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "172.18.0.5:34567"
		req.Header.Set("X-Forwarded-For", "198.51.100.88")
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i+1, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/public/magic/exchange", strings.NewReader(`{"token":"bad"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "172.18.0.5:34567"
	req.Header.Set("X-Forwarded-For", "198.51.100.88")
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-limit status = %d, want 429", rec.Code)
	}
	if rec.Header().Get(echo.HeaderRetryAfter) == "" {
		t.Fatal("missing Retry-After header")
	}
}
