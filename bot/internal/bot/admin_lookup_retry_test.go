package bot

import (
	"context"
	"errors"
	tele "gopkg.in/telebot.v3"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type adminRetryTransport struct {
	failures int32
	calls    atomic.Int32
	role     string
}

func (tr *adminRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/getMe") {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"test"}}`))}, nil
	}
	n := tr.calls.Add(1)
	if n <= tr.failures {
		return nil, errors.New("connection reset")
	}
	body := `{"ok":true,"result":{"user":{"id":101,"first_name":"test"},"status":"` + tr.role + `"}}`
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
func TestAdminLookupBoundedRetryFailSafe(t *testing.T) {
	for _, tc := range []struct {
		name, role           string
		failures             int32
		wantAdmin, wantError bool
		calls                int32
	}{
		{"transient member", "member", 1, false, false, 2},
		{"transient admin", "administrator", 2, true, false, 3},
		{"persistent unknown", "member", 9, false, true, 3},
		{"invalid role is not cached as non-admin", "mystery", 0, false, true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &adminRetryTransport{failures: tc.failures, role: tc.role}
			b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Client: &http.Client{Transport: tr}, Synchronous: true})
			if err != nil {
				t.Fatal(err)
			}
			s := &Service{bot: b}
			admin, err := s.isChatAdminWithRetry(context.Background(), -90001, 101)
			if admin != tc.wantAdmin || (err != nil) != tc.wantError || tr.calls.Load() != tc.calls {
				t.Fatalf("admin=%v err=%v calls=%d", admin, err, tr.calls.Load())
			}
		})
	}
}
func TestAdminLookupRetryRespectsCancel(t *testing.T) {
	tr := &adminRetryTransport{failures: 20}
	b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Client: &http.Client{Transport: tr}, Synchronous: true})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{bot: b}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = s.isChatAdminWithRetry(ctx, -90001, 101)
	if !errors.Is(err, context.DeadlineExceeded) || tr.calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, tr.calls.Load())
	}
}
