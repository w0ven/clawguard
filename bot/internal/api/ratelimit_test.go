package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func TestVerifyRateLimitReturns429WithRetryAfter(t *testing.T) {
	redisClient := newAPIFakeRedisClient(t)
	limiter := newRateLimiter(redisClient, zap.NewNop())
	e := echo.New()
	e.POST("/api/verify/turnstile/:token", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, limiter.middleware(func(c echo.Context) string {
		return "test:verify:" + c.Param("token")
	}, turnstileVerifyTokenLimit, apiRateLimitWindow))

	for i := 0; i < turnstileVerifyTokenLimit; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/verify/turnstile/tok", strings.NewReader(`{"cf_response":"x"}`))
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i+1, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/verify/turnstile/tok", strings.NewReader(`{"cf_response":"x"}`))
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-limit status = %d, want 429", rec.Code)
	}
	if rec.Header().Get(echo.HeaderRetryAfter) == "" {
		t.Fatal("Retry-After header is empty")
	}
	if !strings.Contains(rec.Body.String(), "rate limited") {
		t.Fatalf("body = %q, want rate limited error", rec.Body.String())
	}
}

func TestTelegramLoginGETRateLimitReturns429WithRetryAfter(t *testing.T) {
	redisClient := newAPIFakeRedisClient(t)
	limiter := newRateLimiter(redisClient, zap.NewNop())
	server := &Server{}
	okHandler := func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}

	e := echo.New()
	e.GET("/api/auth/telegram-login", okHandler,
		limiter.middleware(server.ipRateLimitKey("auth:telegram_login_ip"), telegramLoginIPLimit, apiRateLimitWindow),
		limiter.middleware(server.telegramLoginUserRateLimitKey, telegramLoginUserLimit, apiRateLimitWindow),
	)

	for i := 0; i < telegramLoginUserLimit; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/auth/telegram-login?id=42", nil)
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET request %d status = %d, want 200", i+1, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/telegram-login?id=42", nil)
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-limit GET status = %d, want 429", rec.Code)
	}
	if rec.Header().Get(echo.HeaderRetryAfter) == "" {
		t.Fatal("Retry-After header is empty")
	}
	if !strings.Contains(rec.Body.String(), "rate limited") {
		t.Fatalf("body = %q, want rate limited error", rec.Body.String())
	}
}

func TestTelegramLoginGETSharesRateLimitKeysWithPOST(t *testing.T) {
	redisClient := newAPIFakeRedisClient(t)
	limiter := newRateLimiter(redisClient, zap.NewNop())
	server := &Server{}
	okHandler := func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}
	ipLimit := limiter.middleware(server.ipRateLimitKey("auth:telegram_login_ip"), telegramLoginIPLimit, apiRateLimitWindow)
	userLimit := limiter.middleware(server.telegramLoginUserRateLimitKey, telegramLoginUserLimit, apiRateLimitWindow)

	e := echo.New()
	e.GET("/api/auth/telegram-login", okHandler, ipLimit, userLimit)
	e.POST("/api/auth/telegram-login", okHandler, ipLimit, userLimit)

	for i := 0; i < telegramLoginUserLimit; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/telegram-login?id=42", nil)
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST request %d status = %d, want 200", i+1, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/telegram-login?id=42", nil)
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("GET after POST quota status = %d, want 429 (shared key)", rec.Code)
	}
	if rec.Header().Get(echo.HeaderRetryAfter) == "" {
		t.Fatal("Retry-After header is empty")
	}
}

func TestRateLimitFailOpenWhenRedisDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	redisClient := redis.NewClient(&redis.Options{Addr: addr, Protocol: 2})
	defer redisClient.Close()

	limiter := newRateLimiter(redisClient, zap.NewNop())
	allowed, err := limiter.Allow(context.Background(), "test:down", 1, time.Minute)
	if !allowed {
		t.Fatal("allowed = false, want fail-open true")
	}
	if err == nil {
		t.Fatal("expected redis connection error")
	}
}

func newAPIFakeRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRateLimitCounterExpiresWithoutExtendingWindow(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = client.Close() })
	limiter := newRateLimiter(client, zap.NewNop())
	ctx := context.Background()
	key := "test:window"
	if allowed, err := limiter.Allow(ctx, key, 1, time.Minute); err != nil || !allowed {
		t.Fatalf("first request: allowed=%v err=%v", allowed, err)
	}
	if ttl := server.TTL(key); ttl != time.Minute {
		t.Fatalf("initial ttl = %s", ttl)
	}
	server.FastForward(20 * time.Second)
	if allowed, err := limiter.Allow(ctx, key, 1, time.Minute); err != nil || allowed {
		t.Fatalf("over-limit request: allowed=%v err=%v", allowed, err)
	}
	if ttl := server.TTL(key); ttl != 40*time.Second {
		t.Fatalf("request extended window: ttl = %s", ttl)
	}
	server.FastForward(40 * time.Second)
	if allowed, err := limiter.Allow(ctx, key, 1, time.Minute); err != nil || !allowed {
		t.Fatalf("new window: allowed=%v err=%v", allowed, err)
	}
}

func TestRateLimitRepairsCounterWithoutExpiry(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = client.Close() })
	key := "test:legacy-counter"
	if err := server.Set(key, "10"); err != nil {
		t.Fatal(err)
	}
	limiter := newRateLimiter(client, zap.NewNop())
	if allowed, err := limiter.Allow(context.Background(), key, 1, time.Minute); err != nil || allowed {
		t.Fatalf("legacy counter: allowed=%v err=%v", allowed, err)
	}
	if ttl := server.TTL(key); ttl != time.Minute {
		t.Fatalf("missing expiry not repaired: ttl = %s", ttl)
	}
	server.FastForward(time.Minute)
	if allowed, err := limiter.Allow(context.Background(), key, 1, time.Minute); err != nil || !allowed {
		t.Fatalf("recovered request: allowed=%v err=%v", allowed, err)
	}
}

func TestTurnstileRateLimitKeyDoesNotExposeCredential(t *testing.T) {
	const secret = "fixture-live-verification-token"
	s := &Server{}
	e := echo.New()
	c := e.NewContext(httptest.NewRequest("POST", "/", nil), httptest.NewRecorder())
	c.SetParamNames("token")
	c.SetParamValues(secret)
	key := s.turnstileTokenRateLimitKey(c)
	if key == "" || strings.Contains(key, secret) {
		t.Fatalf("unsafe limiter key: %s", key)
	}
	if key != s.turnstileTokenRateLimitKey(c) {
		t.Fatal("unstable limiter key")
	}
}
