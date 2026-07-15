package bot

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestSlidingWindowCounterExpiresOldEntriesAndDeduplicatesMessages(t *testing.T) {
	_, client := newJoinProtectionTestRedis(t)
	svc := &Service{redis: client}
	ctx := context.Background()
	base := time.Unix(1_800_000_000, 0)
	key := "test:sliding-window"

	count, err := svc.bumpSlidingWindowCounter(ctx, key, "message-1", base, 10*time.Second)
	if err != nil || count != 1 {
		t.Fatalf("first count = %d, err=%v; want 1", count, err)
	}
	count, err = svc.bumpSlidingWindowCounter(ctx, key, "message-1", base.Add(time.Second), 10*time.Second)
	if err != nil || count != 1 {
		t.Fatalf("duplicate count = %d, err=%v; want 1", count, err)
	}
	count, err = svc.bumpSlidingWindowCounter(ctx, key, "message-2", base.Add(9*time.Second), 10*time.Second)
	if err != nil || count != 2 {
		t.Fatalf("in-window count = %d, err=%v; want 2", count, err)
	}
	count, err = svc.bumpSlidingWindowCounter(ctx, key, "message-3", base.Add(11*time.Second), 10*time.Second)
	if err != nil || count != 2 {
		t.Fatalf("rolled count = %d, err=%v; want 2 after oldest entry expires", count, err)
	}
}

func TestEditedMessagesDoNotIncrementRateLimit(t *testing.T) {
	_, client := newJoinProtectionTestRedis(t)
	svc := &Service{redis: client, logger: zap.NewNop()}
	policy := config.DefaultPolicy
	policy.AntiSpam.RateLimit.MessagesPer10s = 1
	policy.Filter.Keywords.Enabled = false
	policy.Filter.Regex.Enabled = false
	policy.Filter.Links.Enabled = false
	policy.Filter.Usernames.Enabled = false

	for _, messageID := range []int{101, 102} {
		msg := &tele.Message{ID: messageID, Text: "edited", Chat: &tele.Chat{ID: -100}, Sender: &tele.User{ID: 42}}
		handled, err := svc.applyFilterChecks(context.Background(), msg, policy, true, true, false)
		if err != nil || handled {
			t.Fatalf("edited message %d: handled=%t err=%v; want no rate hit", messageID, handled, err)
		}
	}

	key := "clawguard:ratelimit:v2:-100:42"
	if count, err := client.ZCard(context.Background(), key).Result(); err != nil || count != 0 {
		t.Fatalf("edited message rate count = %d, err=%v; want 0", count, err)
	}
}
