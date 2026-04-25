package bot

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/time/rate"
	tele "gopkg.in/telebot.v3"
)

const (
	sendLimiterGlobalRate  = rate.Limit(25)
	sendLimiterGlobalBurst = 5
	sendLimiterChatRate    = rate.Limit(18.0 / 60.0)
	sendLimiterChatBurst   = 3
	sendLimiterChatMaxAge  = 30 * time.Minute
)

type chatLimiterEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

type SendLimiter struct {
	global *rate.Limiter
	chats  sync.Map
	now    func() time.Time
}

func NewSendLimiter() *SendLimiter {
	l := &SendLimiter{
		global: rate.NewLimiter(sendLimiterGlobalRate, sendLimiterGlobalBurst),
		now:    time.Now,
	}
	go l.gcLoop()
	return l
}

func (l *SendLimiter) WaitGlobal(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.global.Wait(ctx)
}

func (l *SendLimiter) WaitChat(ctx context.Context, chatID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := l.chatLimiter(chatID)
	return entry.limiter.Wait(ctx)
}

func (l *SendLimiter) chatLimiter(chatID int64) *chatLimiterEntry {
	now := l.now()
	created := &chatLimiterEntry{
		limiter:  rate.NewLimiter(sendLimiterChatRate, sendLimiterChatBurst),
		lastUsed: now,
	}
	actual, _ := l.chats.LoadOrStore(chatID, created)
	entry := actual.(*chatLimiterEntry)
	entry.lastUsed = now
	return entry
}

func (l *SendLimiter) gcLoop() {
	ticker := time.NewTicker(sendLimiterChatMaxAge)
	defer ticker.Stop()
	for range ticker.C {
		l.gc()
	}
}

func (l *SendLimiter) gc() {
	cutoff := l.now().Add(-sendLimiterChatMaxAge)
	l.chats.Range(func(key, value any) bool {
		entry := value.(*chatLimiterEntry)
		if entry.lastUsed.Before(cutoff) {
			l.chats.Delete(key)
		}
		return true
	})
}

type telegramSender interface {
	Send(to tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error)
	Delete(msg tele.Editable) error
}

func (s *Service) sendThrottled(ctx context.Context, chat tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error) {
	if s.sendLimiter != nil {
		if err := s.sendLimiter.WaitChat(ctx, recipientID(chat)); err != nil {
			return nil, err
		}
		if err := s.sendLimiter.WaitGlobal(ctx); err != nil {
			return nil, err
		}
	}
	return s.sendWithFloodRetry(ctx, chat, what, opts...)
}

func (s *Service) sendWithFloodRetry(ctx context.Context, chat tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error) {
	sender := s.sender
	if sender == nil {
		sender = s.bot
	}
	sent, err := sender.Send(chat, what, opts...)
	if err == nil {
		return sent, nil
	}
	retryAfter, ok := floodRetryAfter(err)
	if !ok {
		return nil, err
	}
	if err := sleepContext(ctx, time.Duration(retryAfter+1)*time.Second); err != nil {
		return nil, err
	}
	return sender.Send(chat, what, opts...)
}

func floodRetryAfter(err error) (int, bool) {
	var flood *tele.FloodError
	if errors.As(err, &flood) && flood != nil {
		return flood.RetryAfter, true
	}
	var teleErr *tele.Error
	if errors.As(err, &teleErr) && teleErr != nil && teleErr.Code == 429 {
		return 0, true
	}
	return 0, false
}

func recipientID(r tele.Recipient) int64 {
	if r == nil {
		return 0
	}
	switch v := r.(type) {
	case *tele.Chat:
		return v.ID
	case *tele.User:
		return v.ID
	default:
		return 0
	}
}

var sleepContext = func(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
