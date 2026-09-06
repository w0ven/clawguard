package bot

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func keywordReplyPolicy(keyword string, cooldownSeconds int) config.GuardPolicy {
	policy := config.DefaultPolicy
	policy.Messages.KeywordReplies = []config.KeywordReplyRule{{
		ID:              "rule-ad",
		Name:            "ad",
		Enabled:         true,
		MatchType:       "fuzzy",
		Keywords:        []string{keyword},
		ReplyText:       "detected",
		CooldownSeconds: cooldownSeconds,
	}}
	return policy
}

func keywordReplyMessage(text string) *tele.Message {
	return &tele.Message{
		ID:     11,
		Text:   text,
		Chat:   &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: 42, FirstName: "Bob"},
	}
}

func TestTryKeywordReplyRedisFailureIsUnprocessed(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		Protocol:    2,
		DialTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	fake := &fakeTelegramSender{}
	svc := &Service{logger: zap.NewNop(), redis: client, sender: fake}

	matched, err := svc.tryKeywordReply(context.Background(), keywordReplyMessage("buy cheap crypto now"), keywordReplyPolicy("cheap crypto", 30))
	if err != nil {
		t.Fatalf("tryKeywordReply redis failure err = %v, want nil", err)
	}
	if matched {
		t.Fatal("matched = true, want false so moderation continues")
	}
	if got := atomic.LoadInt32(&fake.calls); got != 0 {
		t.Fatalf("send calls = %d, want 0 when redis cooldown query fails", got)
	}
}

func TestTryKeywordReplyCooldownHitIsUnprocessed(t *testing.T) {
	_, client := newJoinProtectionTestRedis(t)
	fake := &fakeTelegramSender{}
	svc := &Service{logger: zap.NewNop(), redis: client, sender: fake}
	msg := keywordReplyMessage("buy cheap crypto now")
	policy := keywordReplyPolicy("cheap crypto", 30)
	cdKey := fmt.Sprintf("kwreply:cd:%d:%s", msg.Chat.ID, "rule-ad")
	if err := client.Set(context.Background(), cdKey, "1", time.Minute).Err(); err != nil {
		t.Fatalf("seed cooldown key: %v", err)
	}

	matched, err := svc.tryKeywordReply(context.Background(), msg, policy)
	if err != nil {
		t.Fatalf("tryKeywordReply cooldown hit err = %v, want nil", err)
	}
	if matched {
		t.Fatal("matched = true, want false so cooldown does not finish moderation")
	}
	if got := atomic.LoadInt32(&fake.calls); got != 0 {
		t.Fatalf("send calls = %d, want 0 on cooldown hit", got)
	}
}

func TestTryKeywordReplyIgnoresPollQuestion(t *testing.T) {
	fake := &fakeTelegramSender{}
	svc := &Service{logger: zap.NewNop(), sender: fake}
	msg := &tele.Message{
		ID:     12,
		Chat:   &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: 42, FirstName: "Bob"},
		Poll: &tele.Poll{
			Question: "buy cheap crypto now",
			Options:  []tele.PollOption{{Text: "yes"}, {Text: "no"}},
		},
	}

	matched, err := svc.tryKeywordReply(context.Background(), msg, keywordReplyPolicy("cheap crypto", 0))
	if err != nil {
		t.Fatalf("tryKeywordReply poll-only err = %v, want nil", err)
	}
	if matched {
		t.Fatal("matched = true, want false; auto-reply must stay Text/Caption only")
	}
	if got := atomic.LoadInt32(&fake.calls); got != 0 {
		t.Fatalf("send calls = %d, want 0 for poll-only content", got)
	}
}

func TestMaybeKeywordReplyDoesNotStopModeration(t *testing.T) {
	fake := &fakeTelegramSender{errs: []error{errors.New("telegram send failed")}}
	svc := &Service{logger: zap.NewNop(), sender: fake}
	// maybeKeywordReply is void: match/send errors cannot return to abort
	// handleIncomingMessageWithOptions before buildReviewableContent.
	svc.maybeKeywordReply(context.Background(), keywordReplyMessage("buy cheap crypto now"), keywordReplyPolicy("cheap crypto", 0))
	if got := atomic.LoadInt32(&fake.calls); got != 1 {
		t.Fatalf("send calls = %d, want 1; send error must be swallowed by maybeKeywordReply", got)
	}
}
