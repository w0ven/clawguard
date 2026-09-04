package adkiller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
			"dimensions":       map[string]int{"goods_sale": 92},
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
