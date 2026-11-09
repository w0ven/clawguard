package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type staticResolver struct {
	addrs []netip.Addr
	err   error
}

func (r staticResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	return r.addrs, r.err
}

func TestValidateProviderBaseURL(t *testing.T) {
	validCases := []struct {
		raw  string
		want string
	}{
		{"https://parrot.ke1e.de/v1", "https://parrot.ke1e.de/v1"},
		{"https://PARROT.ke1e.de:443/v1/", "https://parrot.ke1e.de/v1"},
		{"http://example.com:8080/api/v1///", "http://example.com:8080/api/v1"},
		{"https://8.8.8.8:8443/v1", "https://8.8.8.8:8443/v1"},
		{"https://[2606:4700:4700::1111]/v1", "https://[2606:4700:4700::1111]/v1"},
	}
	for _, tc := range validCases {
		got, err := ValidateProviderBaseURL(tc.raw)
		if err != nil {
			t.Fatalf("ValidateProviderBaseURL(%q) unexpected error: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ValidateProviderBaseURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	invalidCases := []string{
		"",
		"ftp://example.com/v1",
		"javascript:alert(1)",
		"https://user:pass@example.com/v1",
		"https://example.com/v1?api_key=secret",
		"https://example.com/v1#frag",
		"http://localhost:8080/v1",
		"http://sub.localhost/v1",
		"http://bot.internal:8080/v1",
		"http://redis.local/v1",
		"http://127.0.0.1:8080/v1",
		"http://[::1]:8080/v1",
		"http://[::ffff:127.0.0.1]:8080/v1",
		"http://0.0.0.0:8080/v1",
		"http://10.0.0.5:8080/v1",
		"http://172.18.0.2:8080/v1",
		"http://192.168.1.1/v1",
		"http://169.254.169.254/latest/meta-data",
		"http://100.100.100.200/latest/meta-data",
		"http://[fc00::1]:8080/v1",
		"http://[fe80::1]:8080/v1",
		"http://[64:ff9b::7f00:1]/v1",
	}
	for _, raw := range invalidCases {
		if got, err := ValidateProviderBaseURL(raw); err == nil {
			t.Fatalf("ValidateProviderBaseURL(%q) = %q, expected error", raw, got)
		}
	}
}

func TestSameProviderBaseURL(t *testing.T) {
	if !SameProviderBaseURL("https://parrot.ke1e.de/v1", "https://PARROT.ke1e.de:443/v1/") {
		t.Fatal("expected canonical equivalent URLs to match")
	}
	if SameProviderBaseURL("https://parrot.ke1e.de/v1", "http://parrot.ke1e.de/v1") {
		t.Fatal("scheme downgrade must be treated as a base_url change")
	}
	if SameProviderBaseURL("https://parrot.ke1e.de/v1", "https://attacker.example.com/v1") {
		t.Fatal("different host must not match")
	}
	if SameProviderBaseURL("https://parrot.ke1e.de/v1", "https://parrot.ke1e.de:8443/v1") {
		t.Fatal("different port must not match")
	}
	if SameProviderBaseURL("https://parrot.ke1e.de/v1", "https://parrot.ke1e.de/v2") {
		t.Fatal("different path must not match")
	}
}

func TestDefaultClientBlocksLoopbackAndPrivateDNSResolution(t *testing.T) {
	var hit atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Default production client must block literal loopback (127.0.0.1).
	client := NewOpenAICompatibleClient(srv.URL, "secret-key", time.Second, nil)
	if _, err := client.Probe(context.Background(), "gpt-test"); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected loopback dial to be blocked, got err=%v", err)
	}
	if hit.Load() != 0 {
		t.Fatalf("loopback server was reached %d times", hit.Load())
	}

	// Dialer must also reject domain names that resolve to private/loopback/metadata IPs (DNS rebinding).
	dial := newSafeProviderDialContext(nil, staticResolver{
		addrs: []netip.Addr{
			netip.MustParseAddr("93.184.216.34"),
			netip.MustParseAddr("172.18.0.2"),
		},
	})
	if _, err := dial(context.Background(), "tcp", "rebind.example.com:443"); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected mixed public+private DNS resolution to be blocked, got %v", err)
	}
}

func TestOpenAIClientDoesNotFollowRedirectsWithBearerKey(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	httpClient := origin.Client()
	httpClient.CheckRedirect = newSafeOpenAIHTTPClient().CheckRedirect
	client := newOpenAICompatibleClientWithHTTPClient(origin.URL, "secret-key", time.Second, nil, httpClient)
	if _, err := client.Probe(context.Background(), "m"); err == nil {
		t.Fatal("expected redirect status to fail rather than follow")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target received %d requests; expected 0", targetHits.Load())
	}
}
