package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/config"
)

func TestVerifyTurnstileWithCloudflareClientTimeout(t *testing.T) {
	server, release := newBlockedHeaderServer(t)
	defer release()

	restoreTurnstileSiteverify(t, server.URL, &http.Client{
		Timeout:   50 * time.Millisecond,
		Transport: testResponseHeaderTimeoutTransport(time.Second),
	})

	service := &Service{
		cfg:    config.Config{TurnstileSecret: "secret"},
		logger: zap.NewNop(),
	}

	start := time.Now()
	_, err := service.verifyTurnstileWithCloudflare(context.Background(), "cf-response", "127.0.0.1")
	if err == nil {
		t.Fatal("expected siteverify timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %s, want under 1s", elapsed)
	}
}

func TestVerifyTurnstileWithCloudflareReusesIdempotencyKey(t *testing.T) {
	redisClient := newFakeRedisClient(t)

	var mu sync.Mutex
	keys := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		key := r.FormValue("idempotency_key")
		if key == "" {
			http.Error(w, "missing idempotency_key", http.StatusBadRequest)
			return
		}
		mu.Lock()
		keys = append(keys, key)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer server.Close()

	restoreTurnstileSiteverify(t, server.URL, &http.Client{
		Timeout:   time.Second,
		Transport: testResponseHeaderTimeoutTransport(time.Second),
	})

	service := &Service{
		cfg:    config.Config{TurnstileSecret: "secret"},
		logger: zap.NewNop(),
		redis:  redisClient,
	}

	for i := 0; i < 2; i++ {
		payload, err := service.verifyTurnstileWithCloudflare(context.Background(), "same-cf-response", "")
		if err != nil {
			t.Fatalf("verify %d: %v", i+1, err)
		}
		if !payload.Success {
			t.Fatalf("verify %d success = false", i+1)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 {
		t.Fatalf("captured %d keys, want 2", len(keys))
	}
	if keys[0] != keys[1] {
		t.Fatalf("idempotency keys differ: %q != %q", keys[0], keys[1])
	}
}

func restoreTurnstileSiteverify(t *testing.T, url string, client *http.Client) {
	t.Helper()
	previousURL := turnstileSiteverifyURL
	previousClient := turnstileSiteverifyClient
	turnstileSiteverifyURL = url
	turnstileSiteverifyClient = client
	t.Cleanup(func() {
		turnstileSiteverifyURL = previousURL
		turnstileSiteverifyClient = previousClient
		if transport, ok := client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	})
}
