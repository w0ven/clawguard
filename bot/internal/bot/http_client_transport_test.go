package bot

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestImageDownloadHTTPClientResponseHeaderTimeout(t *testing.T) {
	server, release := newBlockedHeaderServer(t)
	defer release()

	previousClient := imageDownloadHTTPClient
	transport := testResponseHeaderTimeoutTransport(20 * time.Millisecond)
	imageDownloadHTTPClient = &http.Client{Transport: transport}
	t.Cleanup(func() {
		imageDownloadHTTPClient = previousClient
		transport.CloseIdleConnections()
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	start := time.Now()
	_, err = imageDownloadHTTPClient.Do(req)
	if err == nil {
		t.Fatal("expected response header timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %s, want under 1s", elapsed)
	}
}

func TestTmePreviewHTTPClientResponseHeaderTimeout(t *testing.T) {
	server, release := newBlockedHeaderServer(t)
	defer release()

	previousBaseURL := tmePreviewBaseURL
	previousClient := tmePreviewHTTPClient
	transport := testResponseHeaderTimeoutTransport(20 * time.Millisecond)
	tmePreviewBaseURL = server.URL
	tmePreviewHTTPClient = &http.Client{
		Timeout:   time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	t.Cleanup(func() {
		tmePreviewBaseURL = previousBaseURL
		tmePreviewHTTPClient = previousClient
		transport.CloseIdleConnections()
	})

	start := time.Now()
	_, err := fetchTmeLinkPreview(context.Background(), "https://t.me/timeout/1", nil)
	if err == nil {
		t.Fatal("expected response header timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %s, want under 1s", elapsed)
	}
}

func newBlockedHeaderServer(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	return server, func() {
		close(release)
		server.Close()
	}
}

func testResponseHeaderTimeoutTransport(timeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		MaxConnsPerHost:       50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
}
