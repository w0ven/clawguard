package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAssistantVerificationToolHTTPRoundTrip(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer assistant-test-fake" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Model       string           `json:"model"`
			Messages    []Message        `json:"messages"`
			Tools       []ToolDefinition `json:"tools"`
			Temperature float64          `json:"temperature"`
			MaxTokens   int              `json:"max_tokens"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		if body.Model != "mock-model" || body.MaxTokens != 700 || body.Temperature != .3 || len(body.Tools) != 1 || body.Tools[0].Function.Name != "knowledge_query" {
			t.Errorf("lost request fields %+v", body)
		}
		if len(body.Messages) == 0 || body.Messages[0].Role != "system" || body.Messages[0].Content != "trusted instructions" {
			t.Errorf("bad system %+v", body.Messages)
		}
		if n == 1 {
			if len(body.Messages) != 2 || body.Messages[1].Role != "user" {
				t.Errorf("bad user context %+v", body.Messages)
			}
			fmt.Fprint(w, `{"model":"mock-model","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"knowledge_query","arguments":"{\"query\":\"hours\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":21,"completion_tokens":7}}`)
		} else {
			if len(body.Messages) != 4 || len(body.Messages[2].ToolCalls) != 1 || body.Messages[3].Role != "tool" || body.Messages[3].ToolCallID != "call-1" {
				t.Errorf("broken tool transcript %+v", body.Messages)
			}
			fmt.Fprint(w, `{"model":"mock-model","choices":[{"message":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":2}}`)
		}
	}))
	defer server.Close()
	c := NewOpenAICompatibleClient(server.URL+"/v1", "assistant-test-fake", time.Second, nil)
	req := ToolChatRequest{Model: "mock-model", SystemPrompt: "trusted instructions", Messages: []Message{{Role: "user", Content: "untrusted source"}}, Tools: []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "knowledge_query", Parameters: map[string]any{"type": "object"}}}}, MaxTokens: 700, Temperature: .3}
	got, e := c.ChatWithTools(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	if got.FinishReason != "tool_calls" || len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call-1" || got.PromptTokens != 21 || got.CompletionTokens != 7 {
		t.Fatalf("bad response %+v", got)
	}
	req.Messages = append(req.Messages, Message{Role: "assistant", Content: got.Content, ToolCalls: got.ToolCalls}, Message{Role: "tool", ToolCallID: "call-1", Content: `{"items":[]}`})
	got, e = c.ChatWithTools(context.Background(), req)
	if e != nil || got.Content != "answer" || calls.Load() != 2 {
		t.Fatalf("second round %+v %v count=%d", got, e, calls.Load())
	}
}

func TestAssistantVerificationProviderHTTPErrors(t *testing.T) {
	for _, status := range []int{429, 500, 503} {
		for _, method := range []string{"tools", "plain"} {
			t.Run(fmt.Sprintf("%d/%s", status, method), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", "2")
					w.WriteHeader(status)
					fmt.Fprint(w, "private-response-body assistant-test-fake")
				}))
				defer srv.Close()
				c := NewOpenAICompatibleClient(srv.URL, "", time.Second, nil)
				var e error
				if method == "tools" {
					_, e = c.ChatWithTools(context.Background(), ToolChatRequest{Model: "fake"})
				} else {
					_, e = c.Chat(context.Background(), CheckRequest{Model: "fake"})
				}
				var pe *ProviderError
				if !errors.As(e, &pe) || pe.StatusCode != status || pe.RetryAfter != 2*time.Second {
					t.Fatalf("missing structured HTTP error: %T %v", e, e)
				}
				if strings.Contains(e.Error(), "private-response-body") || strings.Contains(e.Error(), "assistant-test-fake") {
					t.Fatal("response body leaked")
				}
			})
		}
	}
	t.Run("RetryAfterHTTPDate", func(t *testing.T) {
		at := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
		e := providerHTTPError(&http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{at}}}, errors.New("busy"))
		var pe *ProviderError
		if !errors.As(e, &pe) || pe.RetryAfter <= 3*time.Second || pe.RetryAfter > 5*time.Second {
			t.Fatalf("Retry-After date %+v", pe)
		}
	})
	t.Run("CanceledRequestNeverReachesProvider", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1) }))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := NewOpenAICompatibleClient(srv.URL, "", time.Second, nil)
		if _, e := c.ChatWithTools(ctx, ToolChatRequest{}); e == nil || n.Load() != 0 {
			t.Fatalf("cancellation err=%v requests=%d", e, n.Load())
		}
	})
	t.Run("RequestTimeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(100 * time.Millisecond):
			}
		}))
		defer srv.Close()
		c := NewOpenAICompatibleClient(srv.URL, "", time.Second, nil)
		start := time.Now()
		_, e := c.ChatWithTools(context.Background(), ToolChatRequest{Timeout: 15 * time.Millisecond})
		if e == nil || time.Since(start) > 500*time.Millisecond {
			t.Fatalf("timeout %v elapsed=%s", e, time.Since(start))
		}
	})
}
func TestAssistantVerificationToolArgumentShape(t *testing.T) {
	for _, raw := range []string{"", `null`, `[]`, `1`, `{} {}`, `{"x":}`, strings.Repeat("x", 100)} {
		if _, e := ValidateToolArguments(raw, 80); e == nil {
			t.Errorf("accepted malformed arguments %q", raw)
		}
	}
	if _, e := ValidateToolArguments(`{"query":"hours"}`, 80); e != nil {
		t.Fatal(e)
	}
}
