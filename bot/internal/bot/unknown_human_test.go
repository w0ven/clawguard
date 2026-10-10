package bot

import (
	"context"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestUnknownHumanTrustPolicy(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		policy := config.DefaultPolicy
		policy.AI.UnknownUsersAsNew = enabled
		db := newOtherBotMockDB(policy)
		db.noTrust = true
		svc := &Service{logger: zap.NewNop(), queries: store.New(db)}
		msg := &tele.Message{Chat: &tele.Chat{ID: -1001}, Sender: &tele.User{ID: 77, FirstName: "Alice"}}
		got, err := svc.ensureUserTrust(context.Background(), msg)
		if err != nil {
			t.Fatal(err)
		}
		want := "trusted"
		if enabled {
			want = "new"
		}
		if got.Status != want || got.IsBot || got.MessagesChecked != 0 || got.GraduatedAt != nil {
			t.Fatalf("enabled=%v: %+v", enabled, got)
		}
		if enabled && (got.Notes == nil || *got.Notes != "unknown human requires moderation") {
			t.Fatal("missing provenance")
		}
	}
}

func TestUnknownHumanPolicyPreservesExistingTrust(t *testing.T) {
	for _, status := range []string{"new", "trusted", "suspicious", "banned"} {
		policy := config.DefaultPolicy
		policy.AI.UnknownUsersAsNew = true
		db := newOtherBotMockDB(policy)
		db.trust = &store.UserTrust{ChatID: -1001, UserID: 77, Status: status, JoinedAt: db.now, UpdatedAt: db.now, StatusChangedAt: db.now}
		svc := &Service{logger: zap.NewNop(), queries: store.New(db)}
		got, err := svc.ensureUserTrust(context.Background(), &tele.Message{Chat: &tele.Chat{ID: -1001}, Sender: &tele.User{ID: 77}})
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != status {
			t.Fatalf("%s became %s", status, got.Status)
		}
	}
}
