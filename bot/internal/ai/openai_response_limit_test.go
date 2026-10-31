package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIResponseSizeBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"`)
		fmt.Fprint(w, strings.Repeat("x", maxModelResponseBytes))
		fmt.Fprint(w, `"}}]}`)
	}))
	defer server.Close()
	client := NewOpenAICompatibleClient(server.URL, "", time.Second, nil)
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"moderation", func() error { _, err := client.Check(context.Background(), CheckRequest{}); return err }},
		{"probe", func() error { _, err := client.Probe(context.Background(), "model"); return err }},
		{"chat", func() error { _, err := client.Chat(context.Background(), CheckRequest{}); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("got %v", err)
			}
		})
	}
}
