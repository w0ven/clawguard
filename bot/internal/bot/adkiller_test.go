package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/adkiller"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

func newAdKillerTestService(t *testing.T, handler http.HandlerFunc) (*Service, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client := adkiller.NewClient(server.URL, zap.NewNop())
	client.SetAPIKey("test-key")
	return &Service{logger: zap.NewNop(), adkiller: client}, &calls
}

func TestApplyAdKillerPrefilterLowScoreFallsThrough(t *testing.T) {
	svc, calls := newAdKillerTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"score":            12,
			"level":            "normal",
			"primary_category": "normal_chat",
			"dimensions":       map[string]int{"normal_chat": 90},
		})
	})

	policy := config.DefaultPolicy
	policy.AI.AdKiller.Enabled = true
	msg := &tele.Message{
		ID:     12,
		Text:   "今晚一起吃饭",
		Chat:   &tele.Chat{ID: -1001},
		Sender: &tele.User{ID: 42},
	}

	handled, err := svc.applyAdKillerPrefilter(context.Background(), msg, policy, store.UserTrust{Status: "new"}, reviewableContent{Text: msg.Text}, false)
	if err != nil {
		t.Fatalf("applyAdKillerPrefilter() error = %v", err)
	}
	if handled {
		t.Fatal("low score should fall through to later models")
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("api calls = %d, want 1", atomic.LoadInt32(calls))
	}
}

func TestApplyAdKillerPrefilterAPIFailureFallsThrough(t *testing.T) {
	svc, _ := newAdKillerTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"scoring_unavailable","message":"down"}}`))
	})

	policy := config.DefaultPolicy
	policy.AI.AdKiller.Enabled = true
	policy.AI.AdKiller.OnFailure = "fallback"
	msg := &tele.Message{
		ID:     13,
		Text:   "加我领彩金",
		Chat:   &tele.Chat{ID: -1001},
		Sender: &tele.User{ID: 42},
	}

	handled, err := svc.applyAdKillerPrefilter(context.Background(), msg, policy, store.UserTrust{Status: "new"}, reviewableContent{Text: msg.Text}, false)
	if err != nil {
		t.Fatalf("applyAdKillerPrefilter() error = %v", err)
	}
	if handled {
		t.Fatal("API failure with fallback should continue to later models")
	}
}

func TestApplyAdKillerPrefilterSkipOnFailure(t *testing.T) {
	svc, _ := newAdKillerTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad key"}}`))
	})

	policy := config.DefaultPolicy
	policy.AI.AdKiller.Enabled = true
	policy.AI.AdKiller.OnFailure = "skip"
	msg := &tele.Message{
		ID:     14,
		Text:   "加我领彩金",
		Chat:   &tele.Chat{ID: -1001},
		Sender: &tele.User{ID: 42},
	}

	handled, err := svc.applyAdKillerPrefilter(context.Background(), msg, policy, store.UserTrust{Status: "new"}, reviewableContent{Text: msg.Text}, false)
	if err != nil {
		t.Fatalf("applyAdKillerPrefilter() error = %v", err)
	}
	if !handled {
		t.Fatal("on_failure=skip should stop later models")
	}
}

func TestApplyAdKillerPrefilterDisabledDoesNothing(t *testing.T) {
	svc, calls := newAdKillerTestService(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("disabled adkiller should not call the API")
	})

	policy := config.DefaultPolicy
	policy.AI.AdKiller.Enabled = false
	msg := &tele.Message{
		ID:     15,
		Text:   "加我领彩金",
		Chat:   &tele.Chat{ID: -1001},
		Sender: &tele.User{ID: 42},
	}

	handled, err := svc.applyAdKillerPrefilter(context.Background(), msg, policy, store.UserTrust{Status: "new"}, reviewableContent{Text: msg.Text}, false)
	if err != nil {
		t.Fatalf("applyAdKillerPrefilter() error = %v", err)
	}
	if handled {
		t.Fatal("disabled adkiller should not handle the message")
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Fatalf("api calls = %d, want 0", atomic.LoadInt32(calls))
	}
}

func TestApplyAdKillerPrefilterMissingKeyFallsThrough(t *testing.T) {
	svc := &Service{logger: zap.NewNop(), adkiller: adkiller.NewClient("https://example.invalid", zap.NewNop())}
	policy := config.DefaultPolicy
	policy.AI.AdKiller.Enabled = true
	msg := &tele.Message{
		ID:     16,
		Text:   "加我领彩金",
		Chat:   &tele.Chat{ID: -1001},
		Sender: &tele.User{ID: 42},
	}

	handled, err := svc.applyAdKillerPrefilter(context.Background(), msg, policy, store.UserTrust{Status: "new"}, reviewableContent{Text: msg.Text}, false)
	if err != nil {
		t.Fatalf("applyAdKillerPrefilter() error = %v", err)
	}
	if handled {
		t.Fatal("missing api key should fall through to later models")
	}
}
