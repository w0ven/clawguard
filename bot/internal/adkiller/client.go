package adkiller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

const (
	DefaultBaseURL    = "https://api.direct-service.net"
	DefaultTimeout    = 1500 * time.Millisecond
	MaxTextRunes      = 4096
	MaxImageBytes     = 5 << 20
	maxMultipartBytes = MaxImageBytes + (64 << 10)
	maxResponseBytes  = 1 << 20
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
	OCRText         string         `json:"ocr_text,omitempty"`
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
	return fmt.Sprintf("adkiller api status %d", e.Status)
}

var scoreDimensions = []string{
	"normal_chat", "account_identity_sim", "sexual_service", "counterfeit_smuggling_prohibited",
	"crypto_exchange_cashout", "gambling_betting", "group_channel_promotion", "followers_traffic_operations",
	"investment_finance_loan", "other_promotion", "money_mule_payment_channel", "personal_data_tracking",
	"private_contact_diversion", "goods_sale", "recruitment_tasks", "service_promotion",
}

type scoreRequest struct {
	Text string `json:"text"`
}

type scoreResponse struct {
	OCRText         *string        `json:"ocr_text"`
	Score           *int           `json:"score"`
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

	return c.doScore(ctx, payload, "application/json", truncated, false)
}

// ScoreImage uploads image bytes, not a Telegram URL. Callers bound OCR time separately.
func (c *Client) ScoreImage(ctx context.Context, image []byte, caption string) (Result, error) {
	if c == nil || !c.HasAPIKey() {
		return Result{}, ErrNotConfigured
	}
	if len(image) == 0 || len(image) > MaxImageBytes {
		return Result{}, errors.New("invalid adkiller image size")
	}
	switch http.DetectContentType(image) {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return Result{}, errors.New("unsupported adkiller image format")
	}
	if !utf8.ValidString(caption) {
		return Result{}, errors.New("invalid adkiller caption encoding")
	}
	caption, truncated := TruncateText(caption, MaxTextRunes)
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("image", "image")
	if err != nil {
		return Result{}, errors.New("build adkiller image form failed")
	}
	if _, err = part.Write(image); err != nil {
		return Result{}, errors.New("build adkiller image form failed")
	}
	if caption != "" {
		if err = writer.WriteField("caption", caption); err != nil {
			return Result{}, errors.New("build adkiller image form failed")
		}
	}
	if err = writer.Close(); err != nil || payload.Len() > maxMultipartBytes {
		return Result{}, errors.New("invalid adkiller image form size")
	}
	return c.doScore(ctx, payload.Bytes(), writer.FormDataContentType(), truncated, true)
}

func (c *Client) doScore(ctx context.Context, payload []byte, contentType string, truncated bool, image bool) (Result, error) {
	apiKey, _ := c.apiKey.Load().(string)
	endpoint := c.baseURL + "/api/v1/score"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{}, errors.New("build adkiller request failed")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "clawguard-adkiller/1")
	started := time.Now()
	resp, err := c.httpClient.Do(req)
	latency := time.Since(started)
	if err != nil {
		return Result{Latency: latency, Truncated: truncated}, fmt.Errorf("adkiller request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return Result{Latency: latency, Truncated: truncated}, errors.New("invalid adkiller response size")
	}
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	result := Result{Truncated: truncated, Latency: latency, RetryAfter: retryAfter, RateRemaining: strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining"))}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := parseAPIError(resp.StatusCode, body, retryAfter)
		c.logger.Warn("adkiller score failed", zap.Int("status", resp.StatusCode), zap.Int("retry_after", retryAfter), zap.Int("latency_ms", int(latency.Milliseconds())))
		return result, apiErr
	}
	var decoded scoreResponse
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Score == nil || *decoded.Score < 0 || *decoded.Score > 100 || (image && decoded.OCRText == nil) {
		return result, errors.New("invalid adkiller response")
	}
	level := "normal"
	switch {
	case *decoded.Score > 80:
		level = "ad"
	case *decoded.Score > 50:
		level = "suspected"
	case *decoded.Score > 20:
		level = "promotion"
	}
	if decoded.Level != level || len(decoded.Dimensions) != len(scoreDimensions) {
		return result, errors.New("invalid adkiller response")
	}
	primaryValid := false
	for _, name := range scoreDimensions {
		value, ok := decoded.Dimensions[name]
		if !ok || value < 0 || value > 100 {
			return result, errors.New("invalid adkiller dimensions")
		}
		if decoded.PrimaryCategory == name {
			primaryValid = true
		}
	}
	if !primaryValid {
		return result, errors.New("invalid adkiller category")
	}

	result.Score = *decoded.Score
	result.Level = strings.TrimSpace(decoded.Level)
	result.PrimaryCategory = strings.TrimSpace(decoded.PrimaryCategory)
	result.Dimensions = decoded.Dimensions
	if decoded.OCRText != nil {
		result.OCRText = *decoded.OCRText
	}
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
