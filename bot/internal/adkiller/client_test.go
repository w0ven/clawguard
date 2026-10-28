package adkiller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScoreSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/api/v1/score" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["text"] != "加我领彩金" {
			t.Fatalf("text = %q", payload["text"])
		}
		w.Header().Set("X-RateLimit-Remaining", "12")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"score":            82,
			"level":            "ad",
			"primary_category": "goods_sale",
			"dimensions":       testDimensions(),
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, nil)
	client.SetAPIKey("test-key")
	result, err := client.Score(context.Background(), "加我领彩金")
	if err != nil {
		t.Fatalf("Score() error = %v", err)
	}
	if result.Score != 82 || result.Level != "ad" || result.PrimaryCategory != "goods_sale" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.RateRemaining != "12" {
		t.Fatalf("rate remaining = %q", result.RateRemaining)
	}
}

func TestScoreUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"valid API key required"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, nil)
	client.SetAPIKey("bad-key")
	_, err := client.Score(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if apiErr.Status != 401 || apiErr.Code != "unauthorized" {
		t.Fatalf("api error = %+v", apiErr)
	}
}

func TestScoreRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "8")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"too many requests"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, nil)
	client.SetAPIKey("test-key")
	result, err := client.Score(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if apiErr.RetryAfter != 8 || result.RetryAfter != 8 {
		t.Fatalf("retry after = %d result=%d", apiErr.RetryAfter, result.RetryAfter)
	}
}

func TestScoreRequiresAPIKey(t *testing.T) {
	client := NewClient("https://example.invalid", nil)
	_, err := client.Score(context.Background(), "hello")
	if !IsNotConfigured(err) {
		t.Fatalf("error = %v, want not configured", err)
	}
}

func TestTruncateText(t *testing.T) {
	text, truncated := TruncateText(strings.Repeat("广", MaxTextRunes+3), MaxTextRunes)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if got := []rune(text); len(got) != MaxTextRunes {
		t.Fatalf("len = %d, want %d", len(got), MaxTextRunes)
	}
}

func TestScoreEmptyText(t *testing.T) {
	client := NewClient("https://example.invalid", nil)
	client.SetAPIKey("test-key")
	_, err := client.Score(context.Background(), "   ")
	if !IsEmptyText(err) {
		t.Fatalf("error = %v, want empty text", err)
	}
}

func TestScoreHonorsContextTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"score":1,"level":"normal","primary_category":"normal_chat","dimensions":{}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, nil)
	client.SetAPIKey("test-key")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.Score(ctx, "hello")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestScoreImageMultipart(t *testing.T) {
	image := []byte("\x89PNG\r\n\x1a\nsynthetic")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseMultipartForm(6 << 20); err != nil {
			t.Errorf("multipart: %v", err)
			return
		}
		f, _, err := r.FormFile("image")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		got, _ := io.ReadAll(f)
		if string(got) != string(image) {
			t.Error("image differs")
		}
		if r.FormValue("caption") != "test caption" {
			t.Error("caption differs")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"score": 0, "level": "normal", "primary_category": "normal_chat", "dimensions": testDimensions(), "ocr_text": "synthetic"})
	}))
	defer server.Close()
	client := NewClient(server.URL, nil)
	client.SetAPIKey("test")
	result, err := client.ScoreImage(context.Background(), image, "test caption")
	if err != nil || result.OCRText != "synthetic" || calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	if _, err = client.ScoreImage(context.Background(), []byte("not image"), ""); err == nil {
		t.Fatal("invalid image accepted")
	}
	if _, err = client.ScoreImage(context.Background(), make([]byte, MaxImageBytes+1), ""); err == nil {
		t.Fatal("oversize image accepted")
	}
	if calls != 1 {
		t.Fatalf("invalid image sent: %d", calls)
	}
}

func TestScoreRejectsInvalidResponse(t *testing.T) {
	for _, body := range []string{`{}`, `{"score":101,"level":"ad","primary_category":"goods_sale","dimensions":{}}`, `{"score":-1,"level":"normal","primary_category":"normal_chat","dimensions":{}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		client := NewClient(server.URL, nil)
		client.SetAPIKey("test")
		if _, err := client.Score(context.Background(), "test"); err == nil {
			t.Errorf("accepted %s", body)
		}
		server.Close()
	}
}

func testDimensions() map[string]int {
	m := map[string]int{}
	for _, k := range scoreDimensions {
		m[k] = 0
	}
	return m
}

func TestScoreImageCaptionAndRedirectSafety(t *testing.T) {
	var redirected atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(6 << 20); err != nil {
			t.Error(err)
			return
		}
		if got := len([]rune(r.FormValue("caption"))); got != MaxTextRunes {
			t.Errorf("caption runes=%d", got)
		}
		http.Redirect(w, r, dst.URL, http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	c := NewClient(src.URL, nil)
	c.SetAPIKey("fixture")
	_, err := c.ScoreImage(context.Background(), []byte("\x89PNG\r\n\x1a\nfixture"), strings.Repeat("测", MaxTextRunes+1))
	if err == nil {
		t.Fatal("redirect accepted")
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect followed")
	}
	if _, err = c.ScoreImage(context.Background(), []byte("\x89PNG\r\n\x1a\nfixture"), string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 caption accepted")
	}
}

func TestScoreRejectsIncompleteDimensionsAndMissingOCR(t *testing.T) {
	for _, body := range []string{`{"score":0,"level":"normal","primary_category":"normal_chat","dimensions":{}}`, `{"score":99,"level":"normal","primary_category":"normal_chat","dimensions":{}}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		c := NewClient(srv.URL, nil)
		c.SetAPIKey("fixture")
		if _, err := c.Score(context.Background(), "fixture"); err == nil {
			t.Error("invalid response accepted")
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"score": 0, "level": "normal", "primary_category": "normal_chat", "dimensions": testDimensions()})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, nil)
	c.SetAPIKey("fixture")
	if _, err := c.ScoreImage(context.Background(), []byte("\x89PNG\r\n\x1a\nfixture"), ""); err == nil {
		t.Error("missing OCR field accepted")
	}
}

func TestAdKillerErrorsAndLogsDoNotEchoUpstreamContent(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":{"code":"private-caption","message":"private-ocr private-caption private-key"}}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, zap.New(core))
	c.SetAPIKey("private-key")
	_, err := c.ScoreImage(context.Background(), []byte("\x89PNG\r\n\x1a\nprivate-image"), "private-caption")
	if err == nil {
		t.Fatal("expected error")
	}
	text := err.Error() + fmt.Sprint(logs.All())
	for _, secret := range []string{"private-key", "private-caption", "private-ocr", "private-image"} {
		if strings.Contains(text, secret) {
			t.Fatalf("error or logs contained %s", secret)
		}
	}
}
