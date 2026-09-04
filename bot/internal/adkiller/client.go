package adkiller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

const (
	DefaultBaseURL   = "https://api.direct-service.net"
	DefaultTimeout   = 1500 * time.Millisecond
	MaxTextRunes     = 4096
	maxResponseBytes = 1 << 20
)

var (
	ErrNotConfigured = errors.New("adkiller api key is not configured")
	ErrEmptyText     = errors.New("adkiller text is empty")
)

type Scorer interface {
	Score(ctx context.Context, text string) (Result, error)
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     atomic.Value
	logger     *zap.Logger
}

type Result struct {
	Score           int            `json:"score"`
	Level           string         `json:"level"`
	PrimaryCategory string         `json:"primary_category"`
	Dimensions      map[string]int `json:"dimensions"`
	Truncated       bool           `json:"-"`
	Latency         time.Duration  `json:"-"`
	RetryAfter      int            `json:"-"`
	RateRemaining   string         `json:"-"`
}

type APIError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter int
}

func (e *APIError) Error() string {
	if e == nil {
		return "adkiller api error"
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "request failed"
	}
	if strings.TrimSpace(e.Code) != "" {
		return fmt.Sprintf("adkiller api %d %s: %s", e.Status, e.Code, msg)
	}
	return fmt.Sprintf("adkiller api %d: %s", e.Status, msg)
}

type scoreRequest struct {
	Text string `json:"text"`
}

type scoreResponse struct {
	Score           int            `json:"score"`
	Level           string         `json:"level"`
	PrimaryCategory string         `json:"primary_category"`
	Dimensions      map[string]int `json:"dimensions"`
}

type apiErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func NewClient(baseURL string, logger *zap.Logger) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	client := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		logger: logger,
	}
	client.apiKey.Store("")
	return client
}

func (c *Client) SetAPIKey(apiKey string) {
	if c == nil {
		return
	}
	c.apiKey.Store(strings.TrimSpace(apiKey))
}

func (c *Client) HasAPIKey() bool {
	if c == nil {
		return false
	}
	key, _ := c.apiKey.Load().(string)
	return strings.TrimSpace(key) != ""
}

func (c *Client) Score(ctx context.Context, text string) (Result, error) {
	if c == nil {
		return Result{}, ErrNotConfigured
	}
	apiKey, _ := c.apiKey.Load().(string)
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return Result{}, ErrNotConfigured
	}

	trimmed, truncated := TruncateText(text, MaxTextRunes)
	if trimmed == "" {
		return Result{}, ErrEmptyText
	}

	payload, err := json.Marshal(scoreRequest{Text: trimmed})
	if err != nil {
		return Result{}, fmt.Errorf("marshal adkiller request: %w", err)
	}

	endpoint := c.baseURL + "/api/v1/score"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{}, fmt.Errorf("build adkiller request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "clawguard-adkiller/1")

	started := time.Now()
	resp, err := c.httpClient.Do(req)
	latency := time.Since(started)
	if err != nil {
		return Result{Latency: latency, Truncated: truncated}, fmt.Errorf("adkiller request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Result{Latency: latency, Truncated: truncated}, fmt.Errorf("read adkiller response: %w", err)
	}

	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	result := Result{
		Truncated:     truncated,
		Latency:       latency,
		RetryAfter:    retryAfter,
		RateRemaining: strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")),
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := parseAPIError(resp.StatusCode, body, retryAfter)
		c.logger.Warn("adkiller score failed",
			zap.Int("status", resp.StatusCode),
			zap.String("code", apiErr.Code),
			zap.Int("retry_after", retryAfter),
			zap.Int("latency_ms", int(latency.Milliseconds())),
		)
		return result, apiErr
	}

	var decoded scoreResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return result, fmt.Errorf("decode adkiller response: %w", err)
	}
	result.Score = decoded.Score
	result.Level = strings.TrimSpace(decoded.Level)
	result.PrimaryCategory = strings.TrimSpace(decoded.PrimaryCategory)
	result.Dimensions = decoded.Dimensions
	c.logger.Info("adkiller scored message",
		zap.Int("score", result.Score),
		zap.String("level", result.Level),
		zap.String("primary_category", result.PrimaryCategory),
		zap.Bool("truncated", truncated),
		zap.Int("latency_ms", int(latency.Milliseconds())),
		zap.String("rate_remaining", result.RateRemaining),
	)
	return result, nil
}

func TruncateText(text string, maxRunes int) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	if maxRunes <= 0 {
		maxRunes = MaxTextRunes
	}
	if utf8.RuneCountInString(text) <= maxRunes {
		return text, false
	}
	runes := []rune(text)
	return string(runes[:maxRunes]), true
}

func parseAPIError(status int, body []byte, retryAfter int) *APIError {
	apiErr := &APIError{Status: status, RetryAfter: retryAfter, Message: "request failed"}
	var decoded apiErrorBody
	if json.Unmarshal(body, &decoded) == nil {
		apiErr.Code = strings.TrimSpace(decoded.Error.Code)
		if msg := strings.TrimSpace(decoded.Error.Message); msg != "" {
			apiErr.Message = msg
		}
	}
	return apiErr
}

func parseRetryAfter(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return 0
	}
	return seconds
}

func IsNotConfigured(err error) bool {
	return errors.Is(err, ErrNotConfigured)
}

func IsEmptyText(err error) bool {
	return errors.Is(err, ErrEmptyText)
}
