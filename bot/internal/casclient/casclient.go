package casclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	cacheHitTTL           = 6 * time.Hour
	cacheMissTTL          = 30 * time.Minute
	requestTimeout        = 3 * time.Second
	unauthorizedBackoff   = time.Minute
	failureBackoff        = 5 * time.Second
	maxConcurrentRequests = 8
)

type Client struct {
	httpClient *http.Client
	redis      redis.Cmdable
	apiKey     string
	semaphore  chan struct{}
	mu         sync.Mutex
	openUntil  time.Time
	openReason string
	failures   int
}

type CASResult struct {
	Offenses int    `json:"offenses"`
	Reason   string `json:"reason"`
	Time     int64  `json:"time"`
}

type checkResponse struct {
	OK          bool       `json:"ok"`
	Description string     `json:"description"`
	ErrorCode   int        `json:"error_code"`
	Result      *CASResult `json:"result"`
}

type cachedResponse struct {
	Banned bool       `json:"banned"`
	Result *CASResult `json:"result"`
}

var ErrNotConfigured = errors.New("cas not configured: CAS_API_KEY missing")

func New(httpClient *http.Client, redisClient redis.Cmdable, apiKey string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 3 * time.Second}
	}
	// Never forward the Authorization header through a redirect.
	client := *httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		httpClient: &client,
		redis:      redisClient,
		apiKey:     strings.TrimSpace(apiKey),
		semaphore:  make(chan struct{}, maxConcurrentRequests),
	}
}

func (c *Client) IsBanned(ctx context.Context, userID int64) (bool, *CASResult, error) {
	if c == nil {
		return false, nil, nil
	}

	cacheKey := "cas:" + strconv.FormatInt(userID, 10)
	if c.redis != nil {
		cached, err := c.redis.Get(ctx, cacheKey).Result()
		if err == nil {
			var payload cachedResponse
			if json.Unmarshal([]byte(cached), &payload) == nil {
				return payload.Banned, payload.Result, nil
			}
		}
	}

	if c.apiKey == "" {
		return false, nil, ErrNotConfigured
	}

	// Bound both queueing and network time even if the injected client has no timeout.
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		return false, nil, fmt.Errorf("cas concurrency wait: %w", ctx.Err())
	}
	c.mu.Lock()
	if time.Now().Before(c.openUntil) {
		reason := c.openReason
		c.mu.Unlock()
		return false, nil, fmt.Errorf("cas temporarily unavailable (%s); cached results still usable", reason)
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.cas.chat/check?user_id="+strconv.FormatInt(userID, 10), nil)
	if err != nil {
		return false, nil, fmt.Errorf("build cas request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() == nil || ctx.Err() == context.DeadlineExceeded {
			c.recordFailure(false, "network failure")
		}
		return false, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.recordFailure(resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden, fmt.Sprintf("http %d", resp.StatusCode))
		return false, nil, fmt.Errorf("cas status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.recordFailure(false, "response read failure")
		return false, nil, fmt.Errorf("read cas response: %w", err)
	}

	var decoded checkResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		c.recordFailure(false, "invalid response")
		return false, nil, fmt.Errorf("decode cas response: %w", err)
	}
	if !decoded.OK {
		if decoded.ErrorCode != 0 {
			c.recordFailure(decoded.ErrorCode == 401 || decoded.ErrorCode == 403, fmt.Sprintf("api code %d", decoded.ErrorCode))
			return false, nil, fmt.Errorf("cas api code %d", decoded.ErrorCode)
		}
		if isRecordNotFound(decoded.Description) {
			decoded.Result = nil
		} else {
			c.recordFailure(false, "api error")
			return false, nil, errors.New("cas api error")
		}
	} else if decoded.Result == nil {
		c.recordFailure(false, "invalid response")
		return false, nil, fmt.Errorf("cas returned success without a result")
	}
	c.mu.Lock()
	// A successful request already in flight must not erase a newer failure
	// circuit opened by another request.
	if !time.Now().Before(c.openUntil) {
		c.failures = 0
		c.openUntil = time.Time{}
		c.openReason = ""
	}
	c.mu.Unlock()

	banned := decoded.Result != nil
	if c.redis != nil {
		ttl := cacheMissTTL
		if banned {
			ttl = cacheHitTTL
		}
		payload, err := json.Marshal(cachedResponse{Banned: banned, Result: decoded.Result})
		if err == nil {
			_ = c.redis.Set(ctx, cacheKey, payload, ttl).Err()
		}
	}

	return banned, decoded.Result, nil
}

func (c *Client) recordFailure(auth bool, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures++
	if auth {
		c.openUntil = time.Now().Add(unauthorizedBackoff)
		c.openReason = reason
	} else if c.failures >= 3 && !time.Now().Before(c.openUntil) {
		c.openUntil = time.Now().Add(failureBackoff)
		c.openReason = reason
	}
}

func isRecordNotFound(description string) bool {
	normalized := strings.TrimSpace(description)
	normalized = strings.TrimRight(normalized, ".!。！")
	return strings.EqualFold(strings.TrimSpace(normalized), "record not found")
}
