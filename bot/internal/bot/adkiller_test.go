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

func TestCheckBioAdKillerHitReturnsMatched(t *testing.T) {
	svc, calls := newAdKillerTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"score":            95,
			"level":            "ad",
			"primary_category": "private_contact_diversion",
			"dimensions":       validAdKillerDimensions(),
		})
	})

	policy := config.DefaultPolicy
	policy.AI.AdKiller.Enabled = true
	policy.AI.AdKiller.EnabledChatIDs = []int64{-1001}
	user := &tele.User{ID: 42, FirstName: "New"}

	matched, output, succeeded, err := svc.checkBioAdKiller(context.Background(), -1001, user, "加我私聊领彩金 vx:abc", policy, "join_adkiller")
	if err != nil {
		t.Fatalf("checkBioAdKiller() error = %v", err)
	}
	if matched == "" {
		t.Fatal("high-score bio should match")
	}
	if output == nil || output.Model != "adkiller-text-fallback" || output.Action != "kick" {
		t.Fatalf("output = %+v, want adkiller kick", output)
	}
	if !succeeded {
		t.Fatal("confirmed bio fallback should succeed")
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("api calls = %d, want 1", atomic.LoadInt32(calls))
	}
}

func TestCheckBioAdKillerLowScoreSucceedsWithoutHit(t *testing.T) {
	svc, _ := newAdKillerTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"score":            12,
			"level":            "normal",
			"primary_category": "normal_chat",
			"dimensions":       validAdKillerDimensions(),
		})
	})

	policy := config.DefaultPolicy
	policy.AI.AdKiller.Enabled = true
	policy.AI.AdKiller.EnabledChatIDs = []int64{-1001}

	matched, output, succeeded, err := svc.checkBioAdKiller(context.Background(), -1001, &tele.User{ID: 42}, "今晚一起吃饭", policy, "join_adkiller")
	if err != nil {
		t.Fatalf("checkBioAdKiller() error = %v", err)
	}
	if matched != "" || output != nil || !succeeded {
		t.Fatalf("low-score bio should succeed without a hit, matched=%q success=%t output=%v", matched, succeeded, output)
	}
}

func validAdKillerDimensions() map[string]int {
	m := map[string]int{}
	for _, k := range []string{"normal_chat", "account_identity_sim", "sexual_service", "counterfeit_smuggling_prohibited", "crypto_exchange_cashout", "gambling_betting", "group_channel_promotion", "followers_traffic_operations", "investment_finance_loan", "other_promotion", "money_mule_payment_channel", "personal_data_tracking", "private_contact_diversion", "goods_sale", "recruitment_tasks", "service_promotion"} {
		m[k] = 0
	}
	return m
}
