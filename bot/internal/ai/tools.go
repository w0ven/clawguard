package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func providerHTTPError(resp *http.Response, err error) error {
	if resp == nil {
		return err
	}
	out := &ProviderError{StatusCode: resp.StatusCode, Err: err}
	if retry := strings.TrimSpace(resp.Header.Get("Retry-After")); retry != "" {
		if seconds, parseErr := strconv.Atoi(retry); parseErr == nil && seconds >= 0 {
			out.RetryAfter = time.Duration(seconds) * time.Second
		} else if at, parseErr := http.ParseTime(retry); parseErr == nil {
			out.RetryAfter = time.Until(at)
			if out.RetryAfter < 0 {
				out.RetryAfter = 0
			}
		}
	}
	return out
}

// ChatWithTools is deliberately an optional interface: moderation fakes and
// legacy clients implementing only LLMClient remain source compatible.
func (c *OpenAICompatibleClient) ChatWithTools(ctx context.Context, req ToolChatRequest) (*ToolChatResult, error) {
	ctx, cancel := c.withRequestTimeout(ctx, req.Timeout)
	defer cancel()
	messages := append([]Message{}, req.Messages...)
	if !req.PreserveMessages {
		messages = append([]Message{{Role: "system", Content: req.SystemPrompt}}, messages...)
	}
	body, err := json.Marshal(chatCompletionRequest{
		Model: req.Model, Messages: messages, Temperature: req.Temperature,
		MaxTokens: req.MaxTokens, Tools: req.Tools,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tool chat request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create tool chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for key, value := range c.extraHeaders {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		httpReq.Header.Set(key, value)
	}
	startedAt := time.Now()
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call tool chat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return nil, providerHTTPError(resp, fmt.Errorf("tool chat status %d", resp.StatusCode))
	}
	var decoded chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode tool chat response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("tool chat returned no choices")
	}
	choice := decoded.Choices[0]
	return &ToolChatResult{
		Model: decoded.Model, Content: choice.Message.Content, ToolCalls: append([]ToolCall(nil), choice.Message.ToolCalls...),
		FinishReason: choice.FinishReason, LatencyMs: int(time.Since(startedAt) / time.Millisecond),
		PromptTokens: decoded.Usage.PromptTokens, CompletionTokens: decoded.Usage.CompletionTokens,
	}, nil
}

// ValidateToolArguments performs a small provider-independent check before a
// tool reaches a concrete executor. Individual executors still apply their
// narrower schema and scope checks.
func ValidateToolArguments(raw string, maxBytes int) (map[string]any, error) {
	if maxBytes <= 0 {
		maxBytes = 16 << 10
	}
	if len(raw) == 0 || len(raw) > maxBytes {
		return nil, fmt.Errorf("tool arguments size out of range")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var args map[string]any
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	if args == nil {
		return nil, fmt.Errorf("tool arguments must be an object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("tool arguments must contain one JSON object")
	}
	return args, nil
}
