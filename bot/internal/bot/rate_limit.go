package bot

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	tele "gopkg.in/telebot.v3"
)

var slidingWindowCounterScript = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local member = ARGV[3]

redis.call("ZREMRANGEBYSCORE", key, "-inf", now - window)
redis.call("ZADD", key, "NX", now, member)
local count = redis.call("ZCARD", key)
redis.call("PEXPIRE", key, window + 1000)
return count
`)

func rateLimitMessageMember(msg *tele.Message, now time.Time) string {
	if msg != nil && msg.ID > 0 {
		return strconv.Itoa(msg.ID)
	}
	return fmt.Sprintf("unknown:%d", now.UnixNano())
}

func (s *Service) bumpSlidingWindowCounter(ctx context.Context, key, member string, now time.Time, window time.Duration) (int64, error) {
	if s.redis == nil || key == "" || member == "" || window <= 0 {
		return 0, nil
	}
	windowMilliseconds := window.Milliseconds()
	if windowMilliseconds < 1 {
		windowMilliseconds = 1
	}
	value, err := slidingWindowCounterScript.Run(
		ctx,
		s.redis,
		[]string{key},
		now.UnixMilli(),
		windowMilliseconds,
		member,
	).Int64()
	if err != nil {
		return 0, err
	}
	return value, nil
}
