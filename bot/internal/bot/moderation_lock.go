package bot

import (
	"context"
	"errors"
	"sync"
	"time"
)

// All bot-owned mutations for a subject share this process-wide gate. It is
// deliberately not held across more than one subject, and it is not a CAS
// against independent Telegram administrators.
const moderationLockWait = 15 * time.Second

var errModerationLockTimeout = errors.New("moderation operation busy")

type moderationKey struct{ chat, user int64 }
type moderationGate struct {
	semaphore chan struct{}
	refs      int
}

var moderationGates = struct {
	sync.Mutex
	entries map[moderationKey]*moderationGate
}{entries: make(map[moderationKey]*moderationGate)}

// AcquireModerationReview waits at most 15s or until ctx cancellation. The
// returned release must be called once after ownership checking, Telegram
// dispatch, and durable settlement or uncertain-state recording.
func (s *Service) AcquireModerationReview(ctx context.Context, chatID, userID int64) (func(), error) {
	if chatID == 0 || userID <= 0 {
		return nil, errors.New("invalid moderation subject")
	}
	key := moderationKey{chatID, userID}
	moderationGates.Lock()
	g := moderationGates.entries[key]
	if g == nil {
		g = &moderationGate{semaphore: make(chan struct{}, 1)}
		g.semaphore <- struct{}{}
		moderationGates.entries[key] = g
	}
	g.refs++
	moderationGates.Unlock()
	drop := func() {
		moderationGates.Lock()
		g.refs--
		if g.refs == 0 {
			delete(moderationGates.entries, key)
		}
		moderationGates.Unlock()
	}
	timer := time.NewTimer(moderationLockWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-timer.C:
		drop()
		return nil, errModerationLockTimeout
	case <-g.semaphore:
		if err := ctx.Err(); err != nil {
			g.semaphore <- struct{}{}
			drop()
			return nil, err
		}
		var once sync.Once
		return func() { once.Do(func() { g.semaphore <- struct{}{}; drop() }) }, nil
	}
}
