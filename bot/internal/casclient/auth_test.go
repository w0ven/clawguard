package casclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestCASBearerAndMissingConfiguration(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Errorf("missing authorization")
		}
		if r.URL.Query().Get("user_id") != "123" || r.URL.Query().Get("api_key") != "" || strings.Contains(r.URL.String(), "fixture-secret") {
			t.Errorf("unexpected URL")
		}
		fmt.Fprint(w, `{"ok":true,"result":{"offenses":2}}`)
	}))
	defer server.Close()
	c := New(newTestHTTPClient(t, server.URL), nil, "fixture-secret")
	b, r, err := c.IsBanned(context.Background(), 123)
	if err != nil || !b || r == nil || r.Offenses != 2 {
		t.Fatalf("result %v %+v %v", b, r, err)
	}
	c = New(newTestHTTPClient(t, server.URL), nil, "")
	b, r, err = c.IsBanned(context.Background(), 123)
	if b || r != nil || !errors.Is(err, ErrNotConfigured) || calls.Load() != 1 {
		t.Fatalf("missing key result=%v %+v %v calls=%d", b, r, err, calls.Load())
	}
}
func TestCASMissingKeyPreservesCacheAndErrorDoesNotExposeKey(t *testing.T) {
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer r.Close()
	if err := r.Set(context.Background(), "cas:123", `{"banned":true,"result":{"offenses":1}}`, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"description":"fixture-secret"}`)
	}))
	defer server.Close()
	c := New(newTestHTTPClient(t, server.URL), r, "")
	b, _, err := c.IsBanned(context.Background(), 123)
	if !b || err != nil {
		t.Fatalf("cached %v %v", b, err)
	}
	c = New(newTestHTTPClient(t, server.URL), nil, "fixture-secret")
	_, _, err = c.IsBanned(context.Background(), 124)
	if err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("secret leaked or error lost: %v", err)
	}
}
func TestCASErrorCodeNeverTreatedAsNotFound(t *testing.T) {
	for _, code := range []int{401, 403, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"Record not found."}`, code)
			}))
			defer server.Close()
			c := New(newTestHTTPClient(t, server.URL), nil, "fixture-secret")
			b, _, err := c.IsBanned(context.Background(), 123)
			if err == nil || b {
				t.Fatalf("code %d treated as clean: %v", code, err)
			}
			if code == 401 || code == 403 {
				_, _, err = c.IsBanned(context.Background(), 124)
				if err == nil || calls.Load() != 1 {
					t.Fatalf("authorization circuit: %v calls=%d", err, calls.Load())
				}
			}
		})
	}
}
func TestCASNoRedirectWithAuthorization(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer origin.Close()
	c := New(newTestHTTPClient(t, origin.URL), nil, "fixture-secret")
	_, _, err := c.IsBanned(context.Background(), 123)
	if err == nil || targetCalls.Load() != 0 {
		t.Fatalf("redirect error=%v calls=%d", err, targetCalls.Load())
	}
}
