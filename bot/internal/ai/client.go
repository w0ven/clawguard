package ai

import (
	"context"
	"time"
)

type Message struct {
	// OptionalContext is trusted, local-only provenance for prompt budgeting.
	// Zero value is mandatory; neither user text nor provider JSON can set it.
	OptionalContext string     `json:"-"`
	Role            string     `json:"role"`
	Content         any        `json:"content"`
	Name            string     `json:"name,omitempty"`
	ToolCallID      string     `json:"tool_call_id,omitempty"`
	ToolCalls       []ToolCall `json:"tool_calls,omitempty"`
}

// ToolDefinition is the provider-neutral read-only tool schema used by the
// assistant. It deliberately does not expose a service, database, or bot
// object to the model.
type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type ToolChatRequest struct {
	Model        string
	SystemPrompt string
	Messages     []Message
	Tools        []ToolDefinition
	MaxTokens    int
	Temperature  float64
	Timeout      time.Duration
}

type ToolChatResult struct {
	Model            string
	Content          string
	ToolCalls        []ToolCall
	FinishReason     string
	LatencyMs        int
	PromptTokens     int
	CompletionTokens int
}

type ToolCallingClient interface {
	ChatWithTools(context.Context, ToolChatRequest) (*ToolChatResult, error)
}

// ProviderError preserves only transport metadata needed by the pool router.
// Response bodies are not retained, preventing provider secrets or prompts from
// leaking into health status and logs.
type ProviderError struct {
	StatusCode int
	RetryAfter time.Duration
	Err        error
}

func (e *ProviderError) Error() string {
	if e == nil || e.Err == nil {
		return "provider request failed"
	}
	return e.Err.Error()
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type CheckRequest struct {
	Model        string
	SystemPrompt string
	Messages     []Message
	MaxTokens    int
	Temperature  float64
	Timeout      time.Duration
}

type Verdict struct {
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
	Category   string  `json:"category"`
	Reason     string  `json:"reason"`
}

type CheckResult struct {
	Verdicts         []Verdict
	Model            string
	LatencyMs        int
	CostCents        float64
	PromptTokens     int
	CompletionTokens int
}

type CheckContentPart struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	ImageURL map[string]string `json:"image_url,omitempty"`
}

type LLMClient interface {
	Check(ctx context.Context, req CheckRequest) (*CheckResult, error)
	// Probe issues a minimal ping-style request to the given model and returns
	// the round-trip latency in milliseconds. Implementations should use a
	// short context timeout (10s suggested) and parse success very loosely: any
	// non-error HTTP response with at least one choice is a pass.
	Probe(ctx context.Context, modelKey string) (int, error)
	// Chat returns the raw assistant reply text, bypassing verdict parsing.
	// Used by the admin "test" button and any free-form diagnostic flow.
	Chat(ctx context.Context, req CheckRequest) (*ChatRawResult, error)
}

type EmbeddingClient interface {
	Embed(ctx context.Context, model, input string, timeout time.Duration) ([]float64, error)
}
