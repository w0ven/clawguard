//go:build integration

package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestAdKillerRoutingPostgresIntegration(t *testing.T) {
	pool, q, svc, _ := settlementFixture(t)
	ctx := context.Background()
	const chatID int64 = -90001
	const userID int64 = 556677
	_, err := q.UpsertUserTrust(ctx, store.UpsertUserTrustParams{ChatID: chatID, UserID: userID, JoinedAt: time.Now(), Status: "new", Score: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	ad, calls := newAdKillerTestService(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"score": 10, "level": "normal", "primary_category": "normal_chat", "dimensions": validAdKillerDimensions()})
	})
	svc.adkiller = ad.adkiller
	ref := ai.NewModelRef("p", "m")
	models := aiErrorFallbackModelRegistry{items: map[ai.ModelRef]ai.Model{ref: {ID: 1, ProviderID: 1, ProviderKey: "p", ModelKey: "m", Enabled: true, CapabilityTags: []string{"moderation"}}}}
	model := &routingClient{fail: true}
	svc.aiModerator = ai.NewModerator(ctx, zap.NewNop(), nil, q, aiErrorFallbackProviderRegistry{clients: map[string]ai.LLMClient{"p": model}}, models, ai.NewResolver(models), nil)
	p := config.DefaultPolicy
	p.AI.Enabled = true
	p.AI.PrimaryModelRef = ref.String()
	p.AI.FallbackModelRefs = nil
	p.AI.FallbackChain = nil
	p.AI.MaxRetries = 0
	p.AI.BatchWindowMs = 1
	p.AI.PerUserDailyLimit = 0
	p.AI.CheckProfileOnMessage = false
	p.AI.AdKiller.Enabled = true
	p.AI.AdKiller.EnabledChatIDs = []int64{chatID}
	msg := &tele.Message{ID: 101, Text: "synthetic", Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: userID, FirstName: "Test"}}
	if err = svc.applyAIModeration(ctx, msg, p, false, reviewableContent{Text: msg.Text, Kind: "text"}, false); err != nil {
		t.Fatal(err)
	}
	var source, version, verdict string
	err = pool.QueryRow(ctx, `SELECT model, prompt_version, verdict FROM ai_decisions WHERE chat_id=$1 AND message_id=$2`, chatID, msg.ID).Scan(&source, &version, &verdict)
	if err != nil {
		t.Fatal(err)
	}
	if source != "adkiller-text-fallback" || version != "" || verdict != "normal" || atomic.LoadInt32(calls) != 1 {
		t.Fatalf("decision=%s %q %s calls=%d", source, version, verdict, atomic.LoadInt32(calls))
	}
	if _, err = q.UpsertGroupExemptUser(ctx, chatID, userID, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	msg.ID++
	if err = svc.applyAIModeration(ctx, msg, p, false, reviewableContent{Text: msg.Text, Kind: "text"}, false); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatal("exempt user called adkiller")
	}
}
