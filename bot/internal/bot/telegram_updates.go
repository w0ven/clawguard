package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

const (
	joinServiceMessageHandledContextKey = "clawguard:join-service-message-handled"
	telegramUpdateProcessingTTL         = 2 * time.Minute
	telegramUpdateDoneTTL               = 48 * time.Hour
)

var (
	telegramUpdateClaimSequence atomic.Uint64
	claimTelegramUpdateScript   = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current == 'done' then
    return 0
end
if current then
    return -1
end
redis.call('PSETEX', KEYS[1], ARGV[2], ARGV[1])
return 1
`)
	completeTelegramUpdateScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
    return 0
end
redis.call('PSETEX', KEYS[1], ARGV[2], 'done')
return 1
`)
	releaseTelegramUpdateScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
    return 0
end
return redis.call('DEL', KEYS[1])
`)
)

type telegramUpdateExecution struct {
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func newTelegramUpdateExecution() *telegramUpdateExecution {
	return &telegramUpdateExecution{done: make(chan struct{})}
}

func (e *telegramUpdateExecution) add(err error) {
	if e == nil || err == nil {
		return
	}
	e.mu.Lock()
	e.err = errors.Join(e.err, err)
	e.mu.Unlock()
}

func (e *telegramUpdateExecution) result() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *telegramUpdateExecution) finish(err error) {
	if e == nil {
		return
	}
	if e.result() == nil {
		e.add(err)
	}
	close(e.done)
}

func (e *telegramUpdateExecution) wait() error {
	if e == nil {
		return nil
	}
	<-e.done
	return e.result()
}

func (s *Service) recordTelegramUpdateError(c tele.Context, err error) {
	if s == nil || c == nil || err == nil {
		return
	}
	if execution, ok := s.updateExecutions.Load(c.Update().ID); ok {
		execution.(*telegramUpdateExecution).add(err)
	}
}

type telegramUpdateClaim struct {
	service *Service
	key     string
	token   string
}

func (s *Service) claimTelegramUpdate(ctx context.Context, updateID int) (telegramUpdateClaim, bool, error) {
	claim := telegramUpdateClaim{service: s}
	if s == nil || s.redis == nil || updateID <= 0 {
		return claim, true, nil
	}
	claim.key = "clawguard:telegram:update:" + strconv.Itoa(updateID)
	claim.token = fmt.Sprintf("%d:%d", time.Now().UnixNano(), telegramUpdateClaimSequence.Add(1))
	result, err := claimTelegramUpdateScript.Run(
		ctx,
		s.redis,
		[]string{claim.key},
		claim.token,
		telegramUpdateProcessingTTL.Milliseconds(),
	).Int()
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("claim telegram update via redis failed; continuing locally", zap.Error(err), zap.Int("update_id", updateID))
		}
		return telegramUpdateClaim{service: s}, true, nil
	}
	switch result {
	case 0:
		return claim, false, nil
	case -1:
		return claim, false, fmt.Errorf("telegram update %d is already being processed", updateID)
	default:
		return claim, true, nil
	}
}

func (c telegramUpdateClaim) complete(ctx context.Context) {
	if c.service == nil || c.service.redis == nil || c.key == "" || c.token == "" {
		return
	}
	if err := completeTelegramUpdateScript.Run(ctx, c.service.redis, []string{c.key}, c.token, telegramUpdateDoneTTL.Milliseconds()).Err(); err != nil && c.service.logger != nil {
		c.service.logger.Warn("complete telegram update dedupe failed", zap.Error(err), zap.String("key", c.key))
	}
}

func (c telegramUpdateClaim) release(ctx context.Context) {
	if c.service == nil || c.service.redis == nil || c.key == "" || c.token == "" {
		return
	}
	if err := releaseTelegramUpdateScript.Run(ctx, c.service.redis, []string{c.key}, c.token).Err(); err != nil && c.service.logger != nil {
		c.service.logger.Warn("release failed telegram update claim", zap.Error(err), zap.String("key", c.key))
	}
}

func joinedServiceMessageUser(msg *tele.Message) *tele.User {
	if msg == nil {
		return nil
	}
	if msg.UserJoined == nil && len(msg.UsersJoined) > 0 {
		return &msg.UsersJoined[0]
	}
	return msg.UserJoined
}
