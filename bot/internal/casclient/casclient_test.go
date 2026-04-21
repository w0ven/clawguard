package casclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"description":"Record not found"}`))
	}))
	defer server.Close()

	client := New(newTestHTTPClient(t, server.URL), nil)

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
}

func TestIsBannedOtherAPIErrorsStillFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"description":"upstream exploded"}`))
	}))
	defer server.Close()

	client := New(newTestHTTPClient(t, server.URL), nil)

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
