package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestTmePreviewRedirectCannotReachOtherOrigin(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		fmt.Fprint(w, `<meta property="og:title" content="secret">`)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	previous := tmePreviewBaseURL
	tmePreviewBaseURL = source.URL
	t.Cleanup(func() { tmePreviewBaseURL = previous })
	if _, err := fetchTmeLinkPreview(context.Background(), "https://t.me/fixture/1", nil); err == nil {
		t.Fatal("cross origin redirect accepted")
	}
	if targetCalls.Load() != 0 {
		t.Fatalf("redirect target was fetched %d times", targetCalls.Load())
	}
}
func TestTmePreviewRedirectSameOriginWorks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/1" {
			http.Redirect(w, r, "/preview", http.StatusFound)
			return
		}
		fmt.Fprint(w, `<meta property="og:title" content="ok">`)
	}))
	defer server.Close()
	previous := tmePreviewBaseURL
	tmePreviewBaseURL = server.URL
	t.Cleanup(func() { tmePreviewBaseURL = previous })
	preview, err := fetchTmeLinkPreview(context.Background(), "https://t.me/fixture/1", nil)
	if err != nil || preview.Title != "ok" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
}
