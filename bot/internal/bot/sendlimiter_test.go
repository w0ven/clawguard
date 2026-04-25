package bot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
	tele "gopkg.in/telebot.v3"
)

type fakeTelegramSender struct {
	calls int32
	errs  []error
}

func (f *fakeTelegramSender) Send(to tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error) {
	call := atomic.AddInt32(&f.calls, 1)
	if int(call) <= len(f.errs) && f.errs[call-1] != nil {
		return nil, f.errs[call-1]
	}
	return &tele.Message{ID: int(call)}, nil
}

func (f *fakeTelegramSender) Delete(msg tele.Editable) error { return nil }

func TestSendLimiterWaitChatBlocksAfterBurst(t *testing.T) {
	limiter := NewSendLimiter()
	ctx := context.Background()
	for i := 0; i < sendLimiterChatBurst; i++ {
		if err := limiter.WaitChat(ctx, 42); err != nil {
			t.Fatalf("WaitChat burst %d: %v", i, err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := limiter.WaitChat(ctx, 42); err == nil {
		t.Fatalf("WaitChat after burst succeeded, want wait error")
	}
}

func TestSendLimiterWaitHonorsCanceledContext(t *testing.T) {
	limiter := NewSendLimiter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.WaitChat(ctx, 42); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitChat canceled error = %v, want context canceled", err)
	}
	if err := limiter.WaitGlobal(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitGlobal canceled error = %v, want context canceled", err)
	}
}

func TestSendThrottledRetriesFloodError(t *testing.T) {
	oldSleep := sleepContext
	defer func() { sleepContext = oldSleep }()
	var slept time.Duration
	sleepContext = func(ctx context.Context, delay time.Duration) error {
		slept = delay
		return ctx.Err()
	}

	fake := &fakeTelegramSender{errs: []error{&tele.FloodError{RetryAfter: 1}}}
	svc := &Service{
		sender: fake,
		sendLimiter: &SendLimiter{
			global: rate.NewLimiter(rate.Inf, 0),
			now:    time.Now,
		},
	}
	msg, err := svc.sendThrottled(context.Background(), &tele.Chat{ID: 42}, "hello")
	if err != nil {
		t.Fatalf("sendThrottled error = %v", err)
	}
	if msg == nil || msg.ID != 2 {
		t.Fatalf("message = %#v, want second send success", msg)
	}
	if got := atomic.LoadInt32(&fake.calls); got != 2 {
		t.Fatalf("send calls = %d, want 2", got)
	}
	if slept != 2*time.Second {
		t.Fatalf("sleep delay = %v, want 2s", slept)
	}
}
