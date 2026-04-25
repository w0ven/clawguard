package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	tele "gopkg.in/telebot.v3"
)

const (
	tmePreviewCacheTTL  = time.Hour
	tmePreviewTimeout   = 3 * time.Second
	tmePreviewUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
	tmePreviewSeparator = "\n---\n"
	maxTmePreviewFetch  = 3
)

var (
	metaTagPattern       = regexp.MustCompile(`(?is)<meta\b[^>]*?(?:property|name)\s*=\s*["']([^"']+)["'][^>]*?content\s*=\s*["']([^"']*)["'][^>]*>`)
	tmePreviewBaseURL    = "https://t.me"
	tmePreviewHTTPClient = &http.Client{
		Timeout: tmePreviewTimeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			MaxConnsPerHost:       50,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
)

type LinkPreview struct {
	URL         string
	Title       string
	Description string
	ImageURL    string
	Source      string
}

func fetchTmeLinkPreview(ctx context.Context, rawURL string, redisClient *redis.Client) (*LinkPreview, error) {
	return fetchTmeLinkPreviewForUser(ctx, rawURL, redisClient, 0)
}

func fetchTmeLinkPreviewForUser(ctx context.Context, rawURL string, redisClient *redis.Client, userID int64) (*LinkPreview, error) {
	normalized, err := normalizeTmeURL(rawURL)
	if err != nil {
		return nil, err
	}

	if redisClient != nil && userID > 0 {
		rateLimitKey := "tme_fetch_rate:" + strconv.FormatInt(userID, 10)
		count, err := redisClient.Incr(ctx, rateLimitKey).Result()
		if err == nil {
			if count == 1 {
				_ = redisClient.Expire(ctx, rateLimitKey, time.Minute).Err()
			}
			if count > 3 {
				return nil, nil
			}
		}
	}

	cacheKey := "tme_preview:" + sha256Hex(normalized)
	if redisClient != nil {
		cached, err := redisClient.Get(ctx, cacheKey).Result()
		if err == nil {
			return parsePreviewCache(normalized, cached)
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, err
		}
	}

	requestURL, err := tmePreviewRequestURL(normalized)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", tmePreviewUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := tmePreviewHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}

	meta := extractOpenGraphMeta(string(body))
	preview := &LinkPreview{
		URL:         normalized,
		Title:       strings.TrimSpace(meta["og:title"]),
		Description: strings.TrimSpace(meta["og:description"]),
		ImageURL:    strings.TrimSpace(meta["og:image"]),
		Source:      tmePreviewSource(normalized),
	}
	if preview.Title == "" && preview.Description == "" && preview.ImageURL == "" {
		return nil, errors.New("empty og preview")
	}

	if redisClient != nil {
		payload, err := json.Marshal(preview)
		if err == nil {
			_ = redisClient.Set(ctx, cacheKey, payload, tmePreviewCacheTTL).Err()
		}
	}

	return preview, nil
}

func extractTmeURLs(msg *tele.Message) []string {
	if msg == nil {
		return nil
	}

	seen := map[string]struct{}{}
	urls := make([]string, 0, 4)
	add := func(raw string) {
		normalized, err := normalizeTmeURL(raw)
		if err != nil {
			return
		}
		if _, ok := seen[normalized]; ok {
			return
		}
		seen[normalized] = struct{}{}
		urls = append(urls, normalized)
	}

	for _, entity := range msg.Entities {
		switch entity.Type {
		case tele.EntityURL:
			add(msg.EntityText(entity))
		case tele.EntityTextLink:
			add(entity.URL)
		}
	}
	for _, entity := range msg.CaptionEntities {
		switch entity.Type {
		case tele.EntityURL:
			add(msg.EntityText(entity))
		case tele.EntityTextLink:
			add(entity.URL)
		}
	}

	return urls
}

func normalizeTmeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty url")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" {
		parsed, err = url.Parse("https://" + raw)
		if err != nil {
			return "", err
		}
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if host != "t.me" && host != "telegram.me" {
		return "", fmt.Errorf("unsupported host %q", host)
	}
	path := strings.TrimSpace(parsed.EscapedPath())
	if path == "" || path == "/" {
		return "", errors.New("missing path")
	}
	normalized := url.URL{
		Scheme:   "https",
		Host:     "t.me",
		Path:     path,
		RawQuery: parsed.RawQuery,
	}
	return normalized.String(), nil
}

func tmePreviewRequestURL(raw string) (string, error) {
	target, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	base, err := url.Parse(tmePreviewBaseURL)
	if err != nil {
		return "", err
	}
	base.Path = target.Path
	base.RawQuery = target.RawQuery
	return base.String(), nil
}

func extractOpenGraphMeta(html string) map[string]string {
	matches := metaTagPattern.FindAllStringSubmatch(html, -1)
	meta := make(map[string]string, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(match[1]))
		if name == "" {
			continue
		}
		meta[name] = htmlUnescape(strings.TrimSpace(match[2]))
	}
	return meta
}

func htmlUnescape(value string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&#39;", "'",
	)
	return replacer.Replace(value)
}

func parsePreviewCache(rawURL, payload string) (*LinkPreview, error) {
	preview := &LinkPreview{}
	if err := json.Unmarshal([]byte(payload), preview); err != nil {
		return nil, err
	}
	preview.URL = rawURL
	if preview.Source == "" {
		preview.Source = tmePreviewSource(rawURL)
	}
	if preview.Title == "" && preview.Description == "" && preview.ImageURL == "" {
		return nil, errors.New("empty cached preview")
	}
	return preview, nil
}

func tmePreviewSource(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "t.me"
	}
	return strings.TrimPrefix(parsed.Hostname()+parsed.EscapedPath(), "/")
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
