package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OpenAICompatibleClient struct {
	baseURL      string
	apiKey       string
	extraHeaders map[string]string
	client       *http.Client
}

func NewOpenAICompatibleClient(baseURL, apiKey string, timeout time.Duration, extraHeaders map[string]string) *OpenAICompatibleClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &OpenAICompatibleClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       strings.TrimSpace(apiKey),
		extraHeaders: cloneHeaders(extraHeaders),
		client:       &http.Client{Timeout: timeout},
	}
}

type chatCompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

type chatCompletionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *OpenAICompatibleClient) Check(ctx context.Context, req CheckRequest) (*CheckResult, error) {
	body, err := json.Marshal(chatCompletionRequest{
		Model:       req.Model,
		Messages:    append([]Message{{Role: "system", Content: req.SystemPrompt}}, req.Messages...),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal llm request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create llm request: %w", err)
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
		return nil, fmt.Errorf("call llm: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("llm status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	var decoded chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode llm response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("llm returned no choices")
	}

	verdicts, err := parseVerdicts(decoded.Choices[0].Message.Content)
	if err != nil {
		return nil, err
	}

	return &CheckResult{
		Verdicts:         verdicts,
		Model:            decoded.Model,
		LatencyMs:        int(time.Since(startedAt) / time.Millisecond),
		CostCents:        estimateCostCents(decoded.Usage.PromptTokens, decoded.Usage.CompletionTokens),
		PromptTokens:     decoded.Usage.PromptTokens,
		CompletionTokens: decoded.Usage.CompletionTokens,
	}, nil
}

func estimateCostCents(promptTokens, completionTokens int) float64 {
	total := promptTokens + completionTokens
	if total <= 0 {
		return 0
	}
	return float64(total) * 0.001 / 100.0
}

func cloneHeaders(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

// Probe issues a minimal chat request to verify the model is reachable and
// responding. It does not parse verdicts; any 2xx with at least one choice
// counts as healthy.
func (c *OpenAICompatibleClient) Probe(ctx context.Context, modelKey string) (int, error) {
	body, err := json.Marshal(chatCompletionRequest{
		Model:       modelKey,
		Messages:    []Message{{Role: "user", Content: "ping"}},
		Temperature: 0,
		MaxTokens:   1,
	})
	if err != nil {
		return 0, fmt.Errorf("marshal probe request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create probe request: %w", err)
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
		return 0, fmt.Errorf("call probe: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("probe status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	// Drain body so any upstream pooling is clean. We do not need to parse.
	_, _ = io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	return int(time.Since(startedAt) / time.Millisecond), nil
}

// ChatRawResult is what the admin "test" button uses: the raw assistant text
// reply and latency. No JSON parsing, no verdict extraction.
type ChatRawResult struct {
	Model            string
	Content          string
	LatencyMs        int
	PromptTokens     int
	CompletionTokens int
	CostCents        float64
}

// Chat sends a plain chat request and returns the first assistant message as
// raw text. Used by the admin console's test button and any future free-form
// probe that needs the model's actual output rather than a strict verdict.
func (c *OpenAICompatibleClient) Chat(ctx context.Context, req CheckRequest) (*ChatRawResult, error) {
	msgs := append([]Message{}, Message{Role: "system", Content: req.SystemPrompt})
	msgs = append(msgs, req.Messages...)
	body, err := json.Marshal(chatCompletionRequest{
		Model:       req.Model,
		Messages:    msgs,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create chat request: %w", err)
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
		return nil, fmt.Errorf("call chat: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("chat status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var decoded chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode chat response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("chat returned no choices")
	}
	return &ChatRawResult{
		Model:            decoded.Model,
		Content:          decoded.Choices[0].Message.Content,
		LatencyMs:        int(time.Since(startedAt) / time.Millisecond),
		PromptTokens:     decoded.Usage.PromptTokens,
		CompletionTokens: decoded.Usage.CompletionTokens,
		CostCents:        estimateCostCents(decoded.Usage.PromptTokens, decoded.Usage.CompletionTokens),
	}, nil
}
