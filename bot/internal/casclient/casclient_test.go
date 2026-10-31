package casclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestHTTPClient(t *testing.T, serverURL string) *http.Client {
	t.Helper()

	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	return &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			cloned := req.Clone(req.Context())
			cloned.URL.Scheme = target.Scheme
			cloned.URL.Host = target.Host
			return http.DefaultTransport.RoundTrip(cloned)
		}),
	}
}

func TestIsBannedRecordNotFoundReturnsClean(t *testing.T) {
	for _, description := range []string{
		"Record not found",
		"Record not found.",
		"  record NOT found.  ",
	} {
		t.Run(description, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":false,"description":` + strconv.Quote(description) + `}`))
			}))
			defer server.Close()

			client := New(newTestHTTPClient(t, server.URL), nil, "test-key")

			banned, result, err := client.IsBanned(context.Background(), 12345)
			if err != nil {
				t.Fatalf("IsBanned returned error: %v", err)
			}
			if banned {
				t.Fatalf("IsBanned returned banned=true, want false")
			}
			if result != nil {
				t.Fatalf("IsBanned returned result=%+v, want nil", result)
			}
		})
	}
}

func TestIsBannedOtherAPIErrorsStillFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"description":"upstream exploded"}`))
	}))
	defer server.Close()

	client := New(newTestHTTPClient(t, server.URL), nil, "test-key")

	banned, result, err := client.IsBanned(context.Background(), 67890)
	if err == nil {
		t.Fatal("IsBanned returned nil error, want non-nil")
	}
	if banned {
		t.Fatalf("IsBanned returned banned=true, want false")
	}
	if result != nil {
		t.Fatalf("IsBanned returned result=%+v, want nil", result)
	}
}

func TestCASUnauthorizedCircuitAndRecovery(t *testing.T) {
	var count atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if !healthy.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"ok":false,"description":"Record not found"}`)
	}))
	defer server.Close()
	c := New(newTestHTTPClient(t, server.URL), nil, "test-key")
	if _, _, err := c.IsBanned(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("401 err=%v", err)
	}
	for i := 0; i < 20; i++ {
		if _, _, err := c.IsBanned(context.Background(), int64(i+2)); err == nil || !strings.Contains(err.Error(), "temporarily unavailable") {
			t.Fatalf("open circuit err=%v", err)
		}
	}
	if count.Load() != 1 {
		t.Fatalf("calls during circuit %d", count.Load())
	}
	healthy.Store(true)
	c.mu.Lock()
	c.openUntil = time.Now().Add(-time.Second)
	c.mu.Unlock()
	banned, _, err := c.IsBanned(context.Background(), 44)
	if err != nil || banned {
		t.Fatalf("recovery banned=%v err=%v", banned, err)
	}
}
func TestCASInvalidSuccessIsNotClean(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true}`) }))
	defer server.Close()
	c := New(newTestHTTPClient(t, server.URL), nil, "test-key")
	if _, _, err := c.IsBanned(context.Background(), 12); err == nil {
		t.Fatal("missing result treated as clean")
	}
}
func TestCASFailureCircuitIsBounded(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	c := New(newTestHTTPClient(t, server.URL), nil, "test-key")
	for i := 0; i < 20; i++ {
		if _, _, err := c.IsBanned(context.Background(), int64(i+1)); err == nil {
			t.Fatal("upstream error treated as clean")
		}
	}
	if count.Load() != 3 {
		t.Fatalf("upstream calls %d want 3", count.Load())
	}
}

func TestCASCircuitPreservesValidCachedResult(t *testing.T) {
	server := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer r.Close()
	ctx := context.Background()
	if err := r.Set(ctx, "cas:123", `{"banned":true,"result":{"offenses":3}}`, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer upstream.Close()
	c := New(newTestHTTPClient(t, upstream.URL), r, "test-key")
	_, _, _ = c.IsBanned(ctx, 456)
	banned, result, err := c.IsBanned(ctx, 123)
	if err != nil || !banned || result == nil || result.Offenses != 3 {
		t.Fatalf("cached banned=%v result=%+v err=%v", banned, result, err)
	}
}
func TestCASConcurrentUnauthorizedHasBoundedRequests(t *testing.T) {
	var calls atomic.Int32
	start := make(chan struct{}, maxConcurrentRequests)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		start <- struct{}{}
		<-release
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	c := New(newTestHTTPClient(t, server.URL), nil, "test-key")
	const n = 50
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(id int) { _, _, err := c.IsBanned(context.Background(), int64(id+1)); done <- err }(i)
	}
	for i := 0; i < maxConcurrentRequests; i++ {
		select {
		case <-start:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("initial requests did not start")
		}
	}
	close(release)
	for i := 0; i < n; i++ {
		if err := <-done; err == nil {
			t.Fatal("error treated as clean")
		}
	}
	if calls.Load() != maxConcurrentRequests {
		t.Fatalf("calls=%d", calls.Load())
	}
}
