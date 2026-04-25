package bot

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
	tele "gopkg.in/telebot.v3"
)

func TestBuildReviewableContent_LinkPreviewFetched(t *testing.T) {
	msg := &tele.Message{
		Text: "粉 https://t.me/da91wang/9",
		Entities: tele.Entities{
			{Type: tele.EntityURL, Offset: 2, Length: len("https://t.me/da91wang/9")},
		},
	}

	rv := buildReviewableContentWithOptions(context.Background(), msg, nil, func(_ context.Context, rawURL string, _ *redis.Client, userID int64) (*LinkPreview, error) {
		if userID != 0 {
			t.Fatalf("userID = %d, want 0", userID)
		}
		if rawURL != "https://t.me/da91wang/9" {
			t.Fatalf("rawURL = %q", rawURL)
		}
		return &LinkPreview{
			URL:         rawURL,
			Title:       "澳门招聘",
			Description: "日结八百 联系频道置顶",
			ImageURL:    "https://img.example/1.jpg",
			Source:      "t.me/da91wang/9",
		}, nil
	})

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !strings.Contains(rv.Text, "【链接预览 t.me/da91wang/9】") {
		t.Fatalf("Text 缺少链接预览标记: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "标题: 澳门招聘") {
		t.Fatalf("Text 缺少标题: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "描述: 日结八百 联系频道置顶") {
		t.Fatalf("Text 缺少描述: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "[含预览图]") {
		t.Fatalf("Text 缺少预览图标记: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "【本次消息】粉 https://t.me/da91wang/9") {
		t.Fatalf("Text 缺少原消息: %q", rv.Text)
	}
}

func TestBuildReviewableContent_LinkPreviewTimeoutFallback(t *testing.T) {
	msg := &tele.Message{
		Text: "粉 https://t.me/da91wang/9",
		Entities: tele.Entities{
			{Type: tele.EntityURL, Offset: 2, Length: len("https://t.me/da91wang/9")},
		},
	}

	rv := buildReviewableContentWithOptions(context.Background(), msg, nil, func(ctx context.Context, rawURL string, _ *redis.Client, _ int64) (*LinkPreview, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !strings.Contains(rv.Text, "【无法展开的 Telegram 链接】https://t.me/da91wang/9") {
		t.Fatalf("Text 缺少降级标记: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "【本次消息】粉 https://t.me/da91wang/9") {
		t.Fatalf("Text 缺少原消息: %q", rv.Text)
	}
}

func TestExtractTmeURLs_FiltersNonTme(t *testing.T) {
	msg := &tele.Message{
		Text: "a https://example.com/x b https://t.me/test/1",
		Entities: tele.Entities{
			{Type: tele.EntityURL, Offset: 2, Length: len("https://example.com/x")},
			{Type: tele.EntityURL, Offset: 26, Length: len("https://t.me/test/1")},
		},
	}

	urls := extractTmeURLs(msg)
	if len(urls) != 1 {
		t.Fatalf("len(urls) = %d, want 1 (%v)", len(urls), urls)
	}
	if urls[0] != "https://t.me/test/1" {
		t.Fatalf("urls[0] = %q", urls[0])
	}
}

func TestExtractTmeURLs_NormalizesTelegramHostVariants(t *testing.T) {
	raws := []string{
		"https://t.me/test/1?single",
		"http://t.me/test/1?single",
		"https://www.t.me/test/1?single",
		"https://telegram.me/test/1?single",
		"http://telegram.me/test/1?single",
		"https://www.telegram.me/test/1?single",
	}
	for _, raw := range raws {
		msg := &tele.Message{
			Text: raw,
			Entities: tele.Entities{
				{Type: tele.EntityURL, Offset: 0, Length: len(raw)},
			},
		}
		urls := extractTmeURLs(msg)
		if len(urls) != 1 {
			t.Fatalf("%s: len(urls) = %d, want 1 (%v)", raw, len(urls), urls)
		}
		if urls[0] != "https://t.me/test/1?single" {
			t.Fatalf("%s: url = %q", raw, urls[0])
		}
	}
}

func TestNormalizeTmeURLRejectsNonTelegramHosts(t *testing.T) {
	if _, err := normalizeTmeURL("https://example.com/test/1"); err == nil {
		t.Fatal("expected non Telegram host to be rejected")
	}
}

func TestBuildReviewableContent_MultipleURLsFetchedConcurrently(t *testing.T) {
	msg := &tele.Message{
		Text: "https://t.me/a/1 https://t.me/b/2 https://t.me/c/3",
		Entities: tele.Entities{
			{Type: tele.EntityURL, Offset: 0, Length: len("https://t.me/a/1")},
			{Type: tele.EntityURL, Offset: 17, Length: len("https://t.me/b/2")},
			{Type: tele.EntityURL, Offset: 34, Length: len("https://t.me/c/3")},
		},
	}

	var started int32
	release := make(chan struct{})
	var maxConcurrent int32

	rv := buildReviewableContentWithOptions(context.Background(), msg, nil, func(_ context.Context, rawURL string, _ *redis.Client, userID int64) (*LinkPreview, error) {
		if userID != 0 {
			t.Fatalf("userID = %d, want 0", userID)
		}
		current := atomic.AddInt32(&started, 1)
		for {
			seen := atomic.LoadInt32(&maxConcurrent)
			if current <= seen || atomic.CompareAndSwapInt32(&maxConcurrent, seen, current) {
				break
			}
		}
		if current == 3 {
			close(release)
		}
		<-release
		return &LinkPreview{
			URL:         rawURL,
			Title:       rawURL,
			Description: "preview",
			Source:      strings.TrimPrefix(rawURL, "https://"),
		}, nil
	})

	if maxConcurrent < 2 {
		t.Fatalf("expected concurrent fetch, maxConcurrent=%d", maxConcurrent)
	}
	if strings.Count(rv.Text, "【链接预览 ") != 3 {
		t.Fatalf("expected 3 preview blocks: %q", rv.Text)
	}
}

func TestFetchTmeLinkPreview_UsesRedisCacheOnSecondCall(t *testing.T) {
	redisClient := newFakeRedisClient(t)

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `<html><head>
<meta property="og:title" content="缓存标题">
<meta property="og:description" content="缓存描述">
<meta property="og:image" content="https://img.example/cache.jpg">
</head></html>`)
	}))
	defer server.Close()

	previousBaseURL := tmePreviewBaseURL
	tmePreviewBaseURL = server.URL
	defer func() {
		tmePreviewBaseURL = previousBaseURL
	}()

	ctx := context.Background()
	first, err := fetchTmeLinkPreviewForUser(ctx, "https://t.me/cachetest/1", redisClient, 0)
	if err != nil {
		t.Fatalf("first fetch error: %v", err)
	}
	second, err := fetchTmeLinkPreviewForUser(ctx, "https://t.me/cachetest/1", redisClient, 0)
	if err != nil {
		t.Fatalf("second fetch error: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits = %d, want 1", hits.Load())
	}
	if first.Title != second.Title || second.Description != "缓存描述" {
		t.Fatalf("unexpected cached preview: first=%+v second=%+v", first, second)
	}
}

func TestBuildReviewableContent_OnlyURLAndEmptyCurrentText(t *testing.T) {
	msg := &tele.Message{
		Text: "",
		Entities: tele.Entities{
			{Type: tele.EntityTextLink, URL: "https://t.me/onlyurl/1"},
		},
	}

	rv := buildReviewableContentWithOptions(context.Background(), msg, nil, func(_ context.Context, rawURL string, _ *redis.Client, userID int64) (*LinkPreview, error) {
		if userID != 0 {
			t.Fatalf("userID = %d, want 0", userID)
		}
		return &LinkPreview{
			URL:         rawURL,
			Title:       "只有链接",
			Description: "纯链接消息",
			Source:      "t.me/onlyurl/1",
		}, nil
	})

	if rv.Skip {
		t.Fatalf("Skip 不应该为 true")
	}
	if !strings.Contains(rv.Text, "【链接预览 t.me/onlyurl/1】") {
		t.Fatalf("Text 缺少链接预览: %q", rv.Text)
	}
	if !strings.Contains(rv.Text, "【本次消息】") {
		t.Fatalf("Text 缺少本次消息标记: %q", rv.Text)
	}
}

func TestFetchTmeLinkPreviewForUser_RateLimitedAfterThreeRequests(t *testing.T) {
	redisClient := newFakeRedisClient(t)

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `<html><head><meta property="og:title" content="标题"></head></html>`)
	}))
	defer server.Close()

	previousBaseURL := tmePreviewBaseURL
	tmePreviewBaseURL = server.URL
	defer func() {
		tmePreviewBaseURL = previousBaseURL
	}()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		preview, err := fetchTmeLinkPreviewForUser(ctx, fmt.Sprintf("https://t.me/ratelimit/%d", i), redisClient, 42)
		if err != nil {
			t.Fatalf("request %d error: %v", i+1, err)
		}
		if preview == nil {
			t.Fatalf("request %d preview = nil, want non-nil", i+1)
		}
	}

	preview, err := fetchTmeLinkPreviewForUser(ctx, "https://t.me/ratelimit/4", redisClient, 42)
	if err != nil {
		t.Fatalf("request 4 error: %v", err)
	}
	if preview != nil {
		t.Fatalf("request 4 preview = %+v, want nil", preview)
	}
	if hits.Load() != 3 {
		t.Fatalf("server hits = %d, want 3", hits.Load())
	}
}

func newFakeRedisClient(t *testing.T) *redis.Client {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake redis: %v", err)
	}

	store := &fakeRedisStore{data: map[string]string{}}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleFakeRedisConn(conn, store)
		}
	}()

	t.Cleanup(func() {
		_ = listener.Close()
	})

	client := redis.NewClient(&redis.Options{
		Addr:     listener.Addr().String(),
		Protocol: 2,
	})
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}

type fakeRedisStore struct {
	mu   sync.Mutex
	data map[string]string
}

func handleFakeRedisConn(conn net.Conn, store *fakeRedisStore) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		args, err := readRESPArray(reader)
		if err != nil {
			return
		}
		if len(args) == 0 {
			if _, err := writer.WriteString("-ERR empty command\r\n"); err != nil {
				return
			}
			if err := writer.Flush(); err != nil {
				return
			}
			continue
		}

		switch strings.ToUpper(args[0]) {
		case "GET":
			if len(args) < 2 {
				_, _ = writer.WriteString("-ERR wrong number of arguments\r\n")
				_ = writer.Flush()
				continue
			}
			store.mu.Lock()
			value, ok := store.data[args[1]]
			store.mu.Unlock()
			if !ok {
				_, _ = writer.WriteString("$-1\r\n")
			} else {
				_, _ = writer.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(value), value))
			}
		case "SET":
			if len(args) < 3 {
				_, _ = writer.WriteString("-ERR wrong number of arguments\r\n")
				_ = writer.Flush()
				continue
			}
			store.mu.Lock()
			store.data[args[1]] = args[2]
			store.mu.Unlock()
			_, _ = writer.WriteString("+OK\r\n")
		case "INCR":
			if len(args) < 2 {
				_, _ = writer.WriteString("-ERR wrong number of arguments\r\n")
				_ = writer.Flush()
				continue
			}
			store.mu.Lock()
			current := 0
			if raw, ok := store.data[args[1]]; ok {
				_, _ = fmt.Sscanf(raw, "%d", &current)
			}
			current++
			store.data[args[1]] = fmt.Sprintf("%d", current)
			store.mu.Unlock()
			_, _ = writer.WriteString(fmt.Sprintf(":%d\r\n", current))
		case "EXPIRE":
			if len(args) < 3 {
				_, _ = writer.WriteString("-ERR wrong number of arguments\r\n")
				_ = writer.Flush()
				continue
			}
			_, _ = writer.WriteString(":1\r\n")
		case "PING":
			_, _ = writer.WriteString("+PONG\r\n")
		case "QUIT":
			_, _ = writer.WriteString("+OK\r\n")
			_ = writer.Flush()
			return
		default:
			_, _ = writer.WriteString("-ERR unsupported command\r\n")
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func readRESPArray(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) == 0 || line[0] != '*' {
		return nil, fmt.Errorf("unexpected frame %q", line)
	}

	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(line[1:]), "%d", &count); err != nil {
		return nil, err
	}

	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if len(header) == 0 || header[0] != '$' {
			return nil, fmt.Errorf("unexpected bulk header %q", header)
		}

		var size int
		if _, err := fmt.Sscanf(strings.TrimSpace(header[1:]), "%d", &size); err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}
