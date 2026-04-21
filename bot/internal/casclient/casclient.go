package casclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	cacheHitTTL  = 6 * time.Hour
	cacheMissTTL = 30 * time.Minute
)

type Client struct {
	httpClient *http.Client
	redis      redis.Cmdable
}

type CASResult struct {
	Offenses int    `json:"offenses"`
	Reason   string `json:"reason"`
	Time     int64  `json:"time"`
}

type checkResponse struct {
	OK          bool       `json:"ok"`
	Description string     `json:"description"`
	Result      *CASResult `json:"result"`
}

type cachedResponse struct {
	Banned bool       `json:"banned"`
	Result *CASResult `json:"result"`
}

func New(httpClient *http.Client, redisClient redis.Cmdable) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 3 * time.Second}
	}
	return &Client{
		httpClient: httpClient,
		redis:      redisClient,
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.cas.chat/check?user_id="+strconv.FormatInt(userID, 10), nil)
	if err != nil {
		return false, nil, fmt.Errorf("build cas request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, nil, fmt.Errorf("cas status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, nil, fmt.Errorf("read cas response: %w", err)
	}

	var decoded checkResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return false, nil, fmt.Errorf("decode cas response: %w", err)
	}
	if !decoded.OK {
		if decoded.Description == "Record not found" {
			decoded.Result = nil
		} else {
			return false, nil, fmt.Errorf("cas api error: %s", decoded.Description)
		}
	}

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
