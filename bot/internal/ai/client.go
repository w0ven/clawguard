package ai

import "context"

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type CheckRequest struct {
	Model        string
	SystemPrompt string
	Messages     []Message
	MaxTokens    int
	Temperature  float64
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
	// short context timeout (5s suggested) and parse success very loosely: any
	// non-error HTTP response with at least one choice is a pass.
	Probe(ctx context.Context, modelKey string) (int, error)
	// Chat returns the raw assistant reply text, bypassing verdict parsing.
	// Used by the admin "test" button and any free-form diagnostic flow.
	Chat(ctx context.Context, req CheckRequest) (*ChatRawResult, error)
}
