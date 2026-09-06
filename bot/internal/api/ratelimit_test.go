package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake redis: %v", err)
	}
	store := &apiFakeRedisStore{data: map[string]string{}, expires: map[string]time.Time{}}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleAPIFakeRedisConn(conn, store)
		}
	}()

	t.Cleanup(func() { _ = listener.Close() })
	client := redis.NewClient(&redis.Options{Addr: listener.Addr().String(), Protocol: 2})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type apiFakeRedisStore struct {
	mu      sync.Mutex
	data    map[string]string
	expires map[string]time.Time
}

func handleAPIFakeRedisConn(conn net.Conn, store *apiFakeRedisStore) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		args, err := readAPIRESPArray(reader)
		if err != nil {
			return
		}
		if len(args) == 0 {
			_, _ = writer.WriteString("-ERR empty command\r\n")
			_ = writer.Flush()
			continue
		}
		switch strings.ToUpper(args[0]) {
		case "INCR":
			store.mu.Lock()
			current := 0
			if raw, ok := store.data[args[1]]; ok {
				_, _ = fmt.Sscanf(raw, "%d", &current)
			}
			current++
			store.data[args[1]] = fmt.Sprintf("%d", current)
			store.mu.Unlock()
			_, _ = writer.WriteString(fmt.Sprintf(":%d\r\n", current))
		case "EXPIRE":
			seconds := 0
			_, _ = fmt.Sscanf(args[2], "%d", &seconds)
			store.mu.Lock()
			store.expires[args[1]] = time.Now().Add(time.Duration(seconds) * time.Second)
			store.mu.Unlock()
			_, _ = writer.WriteString(":1\r\n")
		case "TTL":
			store.mu.Lock()
			expiresAt, ok := store.expires[args[1]]
			store.mu.Unlock()
			if !ok {
				_, _ = writer.WriteString(":-1\r\n")
			} else {
				remaining := int(time.Until(expiresAt).Seconds())
				if remaining < 1 {
					remaining = 1
				}
				_, _ = writer.WriteString(fmt.Sprintf(":%d\r\n", remaining))
			}
		case "PING":
			_, _ = writer.WriteString("+PONG\r\n")
		case "QUIT":
			_, _ = writer.WriteString("+OK\r\n")
			_ = writer.Flush()
			return
		default:
			_, _ = writer.WriteString("-ERR unsupported command\r\n")
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func readAPIRESPArray(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) == 0 || line[0] != '*' {
		return nil, fmt.Errorf("unexpected frame %q", line)
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(line[1:]), "%d", &count); err != nil {
		return nil, err
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		var size int
		if _, err := fmt.Sscanf(strings.TrimSpace(header[1:]), "%d", &size); err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}
