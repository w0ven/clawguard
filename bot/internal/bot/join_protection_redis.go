package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
)

const (
	joinProtectionActiveKey       = "clawguard:join-protection:active"
	joinProtectionCleanupQueueKey = "clawguard:join-protection:cleanup-queue"
	joinProtectionCleanupDeadKey  = "clawguard:join-protection:cleanup-dead"
	joinProtectionStateTTL        = 7 * 24 * time.Hour
	membershipSessionTTL          = 30 * 24 * time.Hour
)

var joinProtectionEventSequence atomic.Uint64

var observeJoinProtectionScript = redis.NewScript(`
local now_ms = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local threshold = tonumber(ARGV[3])
local protection_ms = tonumber(ARGV[4])
local notify_ms = tonumber(ARGV[5])
local max_pending = tonumber(ARGV[6])
local database_pending = tonumber(ARGV[7])
local event_member = ARGV[8]
local chat_id = ARGV[9]
local state_ttl_ms = tonumber(ARGV[10])
local join_ttl_ms = tonumber(ARGV[11])

local recovered_intercepted = 0
local recovered_until = 0
local protection_until = tonumber(redis.call('HGET', KEYS[1], 'protection_until_ms') or '0')

if protection_until > now_ms then
    local intercepted = redis.call('HINCRBY', KEYS[1], 'intercepted', 1)
    local active_trigger = redis.call('HGET', KEYS[1], 'current_trigger') or 'active'
    local last_notification = tonumber(redis.call('HGET', KEYS[1], 'last_notification_ms') or '0')
    local notify = 0
    if last_notification == 0 or now_ms - last_notification >= notify_ms then
        notify = 1
        redis.call('HSET', KEYS[1], 'last_notification_ms', now_ms)
    end
    redis.call('HSET', KEYS[1], 'last_join_at_ms', now_ms)
    redis.call('PEXPIRE', KEYS[1], state_ttl_ms)
    redis.call('ZADD', KEYS[3], protection_until, chat_id)
    return {1, 0, notify, active_trigger, protection_until, intercepted, 0, 0, 0, 0}
end

if protection_until > 0 then
    recovered_intercepted = tonumber(redis.call('HGET', KEYS[1], 'intercepted') or '0')
    recovered_until = protection_until
    redis.call('HSET', KEYS[1],
        'protection_until_ms', 0,
        'intercepted', 0,
        'last_recovered_at_ms', now_ms,
        'last_intercepted', recovered_intercepted,
        'recent_joins', 0)
    redis.call('HDEL', KEYS[1], 'current_trigger')
    redis.call('ZREM', KEYS[3], chat_id)
end

if database_pending < 0 then
    redis.call('PEXPIRE', KEYS[1], state_ttl_ms)
    return {0, 0, 0, 'needs_pending', 0, 0, 1, 0, recovered_intercepted, recovered_until}
end

local window_start = now_ms - window_ms
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', window_start - 1)
redis.call('ZADD', KEYS[2], now_ms, event_member)
local recent_joins = redis.call('ZCARD', KEYS[2])
redis.call('PEXPIRE', KEYS[2], join_ttl_ms)

local trigger = ''
if recent_joins >= threshold then
    trigger = 'join_threshold'
elseif database_pending + 1 >= max_pending then
    trigger = 'pending_limit'
end

if trigger ~= '' then
    protection_until = now_ms + protection_ms
    redis.call('HSET', KEYS[1],
        'protection_until_ms', protection_until,
        'intercepted', 1,
        'last_notification_ms', now_ms,
        'last_entered_at_ms', now_ms,
        'last_join_at_ms', now_ms,
        'recent_joins', recent_joins,
        'current_trigger', trigger,
        'last_trigger', trigger)
    redis.call('PEXPIRE', KEYS[1], state_ttl_ms)
    redis.call('ZADD', KEYS[3], protection_until, chat_id)
    redis.call('DEL', KEYS[2])
    return {1, 1, 1, trigger, protection_until, 1, 0, recent_joins, recovered_intercepted, recovered_until}
end

redis.call('HSET', KEYS[1], 'recent_joins', recent_joins, 'last_join_at_ms', now_ms)
redis.call('PEXPIRE', KEYS[1], state_ttl_ms)
return {0, 0, 0, '', 0, 0, 0, recent_joins, recovered_intercepted, recovered_until}
`)

var finishJoinProtectionScript = redis.NewScript(`
local expected_until = tonumber(ARGV[1])
local now_ms = tonumber(ARGV[2])
local chat_id = ARGV[3]
local state_ttl_ms = tonumber(ARGV[4])
local protection_until = tonumber(redis.call('HGET', KEYS[1], 'protection_until_ms') or '0')

if protection_until == 0 or protection_until ~= expected_until or now_ms < protection_until then
    return {0, 0, protection_until}
end

local intercepted = tonumber(redis.call('HGET', KEYS[1], 'intercepted') or '0')
redis.call('HSET', KEYS[1],
    'protection_until_ms', 0,
    'intercepted', 0,
    'last_recovered_at_ms', now_ms,
    'last_intercepted', intercepted,
    'recent_joins', 0)
redis.call('HDEL', KEYS[1], 'current_trigger')
redis.call('PEXPIRE', KEYS[1], state_ttl_ms)
redis.call('ZREM', KEYS[2], chat_id)
return {1, intercepted, protection_until}
`)

var restoreJoinProtectionScript = redis.NewScript(`
local now_ms = tonumber(ARGV[1])
local protection_until = tonumber(ARGV[2])
local intercepted = tonumber(ARGV[3])
local last_notification = tonumber(ARGV[4])
local trigger = ARGV[5]
local chat_id = ARGV[6]
local state_ttl_ms = tonumber(ARGV[7])
local current_until = tonumber(redis.call('HGET', KEYS[1], 'protection_until_ms') or '0')
if protection_until <= now_ms or current_until > now_ms then
    return 0
end
redis.call('HSET', KEYS[1],
    'protection_until_ms', protection_until,
    'intercepted', intercepted,
    'last_notification_ms', last_notification,
    'current_trigger', trigger,
    'last_trigger', trigger)
redis.call('PEXPIRE', KEYS[1], state_ttl_ms)
redis.call('ZADD', KEYS[2], protection_until, chat_id)
redis.call('DEL', KEYS[3])
return 1
`)

var restoreJoinProtectionWindowScript = redis.NewScript(`
local window_start = tonumber(ARGV[1])
local ttl_ms = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', window_start - 1)
if redis.call('ZCARD', KEYS[1]) > 0 then
    return 0
end
for i = 3, #ARGV, 2 do
    local score = tonumber(ARGV[i])
    if score >= window_start then
        redis.call('ZADD', KEYS[1], score, ARGV[i + 1])
    end
end
redis.call('PEXPIRE', KEYS[1], ttl_ms)
return 1
`)

var failTelegramCleanupScript = redis.NewScript(`
local now_ms = tonumber(ARGV[1])
local base_ms = tonumber(ARGV[2])
local retry_min_ms = tonumber(ARGV[3])
local max_ms = tonumber(ARGV[4])
local error_text = ARGV[5]
local state_ttl_ms = tonumber(ARGV[6])
local cooldown_until = tonumber(redis.call('HGET', KEYS[1], 'cooldown_until_ms') or '0')

if cooldown_until > now_ms then
    return {cooldown_until, 0, tonumber(redis.call('HGET', KEYS[1], 'failures') or '0')}
end

local failures = tonumber(redis.call('HGET', KEYS[1], 'failures') or '0') + 1
local delay = base_ms * (2 ^ (failures - 1))
if delay > max_ms then delay = max_ms end
if retry_min_ms > delay then delay = retry_min_ms end
if delay > max_ms then delay = max_ms end
cooldown_until = now_ms + delay

redis.call('HSET', KEYS[1],
    'cooldown_until_ms', cooldown_until,
    'failures', failures,
    'last_error', error_text,
    'last_error_at_ms', now_ms)
redis.call('PEXPIRE', KEYS[1], state_ttl_ms)
return {cooldown_until, 1, failures}
`)

var enqueueJoinProtectionCleanupScript = redis.NewScript(`
redis.call('HSET', KEYS[1],
    'chat_id', ARGV[1],
    'user_id', ARGV[2],
    'username', ARGV[3],
    'first_name', ARGV[4],
    'temporary_ban_seconds', ARGV[5],
    'reason', ARGV[6],
    'action', ARGV[7],
    'rule', ARGV[8],
    'created_at_ms', ARGV[9],
    'action_until_ms', ARGV[10],
    'membership_generation', ARGV[11])
redis.call('HINCRBY', KEYS[1], 'enqueue_count', 1)
redis.call('PERSIST', KEYS[1])
redis.call('ZADD', KEYS[2], ARGV[9], ARGV[12])
redis.call('SADD', KEYS[3], ARGV[12])
redis.call('PERSIST', KEYS[3])
if ARGV[11] ~= '' and not redis.call('GET', KEYS[4]) then
    redis.call('PSETEX', KEYS[4], ARGV[13], ARGV[11])
end
return 1
`)

var claimJoinProtectionCleanupScript = redis.NewScript(`
local members = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, 1)
if #members == 0 then
    return ''
end
redis.call('ZADD', KEYS[1], ARGV[2], members[1])
return members[1]
`)

var cancelJoinProtectionCleanupScript = redis.NewScript(`
local members = redis.call('SMEMBERS', KEYS[1])
local removed = 0
for _, member in ipairs(members) do
    if member == ARGV[1] or string.sub(member, 1, string.len(ARGV[2])) == ARGV[2] then
        redis.call('DEL', ARGV[3] .. member)
        redis.call('ZREM', KEYS[2], member)
        redis.call('SREM', KEYS[1], member)
        removed = removed + 1
    end
end
redis.call('DEL', KEYS[3])
return removed
`)

type joinProtectionRedisObservation struct {
	Decision  joinProtectionDecision
	NeedsData bool
	Recovered *joinProtectionSummary
	JoinCount int
}

type JoinProtectionRuntimeStatus struct {
	State                    string     `json:"state"`
	Source                   string     `json:"source"`
	Degraded                 bool       `json:"degraded"`
	ProtectionUntil          *time.Time `json:"protection_until,omitempty"`
	Trigger                  string     `json:"trigger,omitempty"`
	Intercepted              int        `json:"intercepted"`
	RecentJoins              int        `json:"recent_joins"`
	PendingVerifications     int64      `json:"pending_verifications"`
	LastNotificationAt       *time.Time `json:"last_notification_at,omitempty"`
	LastEnteredAt            *time.Time `json:"last_entered_at,omitempty"`
	LastRecoveredAt          *time.Time `json:"last_recovered_at,omitempty"`
	LastIntercepted          int        `json:"last_intercepted"`
	CleanupCooldownUntil     *time.Time `json:"cleanup_cooldown_until,omitempty"`
	CleanupFailures          int        `json:"cleanup_failures"`
	LastCleanupError         string     `json:"last_cleanup_error,omitempty"`
	LastCleanupErrorAt       *time.Time `json:"last_cleanup_error_at,omitempty"`
	DeferredCleanupTaskCount int64      `json:"deferred_cleanup_task_count"`
}

type joinProtectionDeferredCleanup struct {
	Member               string
	ChatID               int64
	UserID               int64
	Username             string
	FirstName            string
	TemporaryBanSeconds  int
	ActionUntil          time.Time
	Reason               string
	Action               string
	Rule                 string
	MembershipGeneration string
	CreatedAt            time.Time
}

func joinProtectionStateKey(chatID int64) string {
	return fmt.Sprintf("clawguard:join-protection:%d:state", chatID)
}

func joinProtectionJoinsKey(chatID int64) string {
	return fmt.Sprintf("clawguard:join-protection:%d:joins", chatID)
}

func telegramCleanupStateKey(chatID int64) string {
	return fmt.Sprintf("clawguard:join-protection:%d:cleanup-breaker", chatID)
}

func joinProtectionCleanupGroupKey(chatID int64) string {
	return fmt.Sprintf("clawguard:join-protection:%d:cleanup-tasks", chatID)
}

func joinProtectionCleanupTaskKey(member string) string {
	return "clawguard:join-protection:cleanup-task:" + member
}

func membershipSessionMapKey(chatID, userID int64) string {
	return fmt.Sprintf("%d:%d", chatID, userID)
}

func membershipSessionKey(chatID, userID int64) string {
	return "clawguard:membership-session:" + membershipSessionMapKey(chatID, userID)
}

func membershipGenerationForUpdate(updateID int, update *tele.ChatMemberUpdate) string {
	if updateID > 0 {
		return "u" + strconv.Itoa(updateID)
	}
	timestamp := int64(0)
	if update != nil {
		timestamp = update.Unixtime
	}
	return fmt.Sprintf("t%d-%d", timestamp, joinProtectionEventSequence.Add(1))
}

func (s *Service) rememberMembershipSession(ctx context.Context, chatID, userID int64, generation string) error {
	if s == nil || chatID == 0 || userID == 0 || strings.TrimSpace(generation) == "" {
		return nil
	}
	s.membershipSessions.Store(membershipSessionMapKey(chatID, userID), generation)
	if s.redis == nil {
		return nil
	}
	return s.redis.Set(ctx, membershipSessionKey(chatID, userID), generation, membershipSessionTTL).Err()
}

func (s *Service) membershipSessionCurrent(ctx context.Context, chatID, userID int64, generation string) (bool, error) {
	if strings.TrimSpace(generation) == "" {
		return true, nil
	}
	if s != nil && s.redis != nil {
		current, err := s.redis.Get(ctx, membershipSessionKey(chatID, userID)).Result()
		if err == nil {
			return current == generation, nil
		}
		if err != redis.Nil {
			return false, err
		}
		return false, nil
	}
	if s == nil {
		return false, nil
	}
	current, ok := s.membershipSessions.Load(membershipSessionMapKey(chatID, userID))
	return ok && current == generation, nil
}

func observeJoinProtectionRedis(
	ctx context.Context,
	rdb redis.Cmdable,
	chatID int64,
	policy config.JoinProtectionPolicy,
	databasePending *int,
	now time.Time,
) (joinProtectionRedisObservation, error) {
	pending := -1
	if databasePending != nil {
		pending = *databasePending
	}
	member := fmt.Sprintf("%d:%d", now.UnixNano(), joinProtectionEventSequence.Add(1))
	joinTTL := time.Duration(policy.JoinWindowSeconds)*time.Second + time.Minute
	values, err := observeJoinProtectionScript.Run(
		ctx,
		rdb,
		[]string{joinProtectionStateKey(chatID), joinProtectionJoinsKey(chatID), joinProtectionActiveKey},
		now.UnixMilli(),
		(time.Duration(policy.JoinWindowSeconds) * time.Second).Milliseconds(),
		policy.JoinThreshold,
		(time.Duration(policy.ProtectionDurationSeconds) * time.Second).Milliseconds(),
		(time.Duration(policy.AdminNotifyIntervalSeconds) * time.Second).Milliseconds(),
		policy.MaxPendingVerifications,
		pending,
		member,
		chatID,
		joinProtectionStateTTL.Milliseconds(),
		joinTTL.Milliseconds(),
	).Slice()
	if err != nil {
		return joinProtectionRedisObservation{}, err
	}
	if len(values) < 10 {
		return joinProtectionRedisObservation{}, fmt.Errorf("unexpected join protection redis result length %d", len(values))
	}

	observation := joinProtectionRedisObservation{
		Decision: joinProtectionDecision{
			Protect:     redisResultInt64(values[0]) == 1,
			Entered:     redisResultInt64(values[1]) == 1,
			Notify:      redisResultInt64(values[2]) == 1,
			Trigger:     redisResultString(values[3]),
			Intercepted: int(redisResultInt64(values[5])),
		},
		NeedsData: redisResultInt64(values[6]) == 1,
		JoinCount: int(redisResultInt64(values[7])),
	}
	if until := redisResultInt64(values[4]); until > 0 {
		observation.Decision.ProtectionUntil = time.UnixMilli(until)
	}
	if recoveredUntil := redisResultInt64(values[9]); recoveredUntil > 0 {
		observation.Recovered = &joinProtectionSummary{
			ProtectionUntil: time.UnixMilli(recoveredUntil),
			Intercepted:     int(redisResultInt64(values[8])),
		}
	}
	return observation, nil
}

func restoreJoinProtectionRedis(ctx context.Context, rdb redis.Cmdable, chatID int64, snapshot joinProtectionMemorySnapshot, now time.Time) error {
	lastNotification := int64(0)
	if !snapshot.LastNotification.IsZero() {
		lastNotification = snapshot.LastNotification.UnixMilli()
	}
	trigger := snapshot.CurrentTrigger
	if trigger == "" {
		trigger = "active"
	}
	return restoreJoinProtectionScript.Run(
		ctx,
		rdb,
		[]string{joinProtectionStateKey(chatID), joinProtectionActiveKey, joinProtectionJoinsKey(chatID)},
		now.UnixMilli(),
		snapshot.ProtectionUntil.UnixMilli(),
		snapshot.Intercepted,
		lastNotification,
		trigger,
		chatID,
		joinProtectionStateTTL.Milliseconds(),
	).Err()
}

func restoreJoinProtectionWindowRedis(ctx context.Context, rdb redis.Cmdable, chatID int64, joins []time.Time, policy config.JoinProtectionPolicy, now time.Time) error {
	if len(joins) == 0 {
		return nil
	}
	joinTTL := time.Duration(policy.JoinWindowSeconds)*time.Second + time.Minute
	args := make([]any, 0, 2+len(joins)*2)
	args = append(args, now.Add(-time.Duration(policy.JoinWindowSeconds)*time.Second).UnixMilli(), joinTTL.Milliseconds())
	for i, joinedAt := range joins {
		args = append(args, joinedAt.UnixMilli(), fmt.Sprintf("shadow:%d:%d", i, joinedAt.UnixNano()))
	}
	return restoreJoinProtectionWindowScript.Run(ctx, rdb, []string{joinProtectionJoinsKey(chatID)}, args...).Err()
}

func finishJoinProtectionRedis(ctx context.Context, rdb redis.Cmdable, chatID int64, expectedUntil, now time.Time) (joinProtectionSummary, bool, error) {
	values, err := finishJoinProtectionScript.Run(
		ctx,
		rdb,
		[]string{joinProtectionStateKey(chatID), joinProtectionActiveKey},
		expectedUntil.UnixMilli(),
		now.UnixMilli(),
		chatID,
		joinProtectionStateTTL.Milliseconds(),
	).Slice()
	if err != nil {
		return joinProtectionSummary{}, false, err
	}
	if len(values) < 3 || redisResultInt64(values[0]) != 1 {
		return joinProtectionSummary{}, false, nil
	}
	return joinProtectionSummary{
		ProtectionUntil: time.UnixMilli(redisResultInt64(values[2])),
		Intercepted:     int(redisResultInt64(values[1])),
	}, true, nil
}

func listExpiredJoinProtectionsRedis(ctx context.Context, rdb redis.Cmdable, now time.Time, limit int64) ([]redis.Z, error) {
	return rdb.ZRangeByScoreWithScores(ctx, joinProtectionActiveKey, &redis.ZRangeBy{
		Min:    "-inf",
		Max:    strconv.FormatInt(now.UnixMilli(), 10),
		Offset: 0,
		Count:  limit,
	}).Result()
}

func clearJoinProtectionRedis(ctx context.Context, rdb redis.Cmdable, chatID int64) error {
	_, err := rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, joinProtectionStateKey(chatID), joinProtectionJoinsKey(chatID))
		pipe.ZRem(ctx, joinProtectionActiveKey, strconv.FormatInt(chatID, 10))
		return nil
	})
	return err
}

func loadJoinProtectionRuntimeStatusRedis(ctx context.Context, rdb redis.Cmdable, chatID int64, now time.Time) (JoinProtectionRuntimeStatus, error) {
	var stateCmd *redis.MapStringStringCmd
	var cleanupCmd *redis.MapStringStringCmd
	var deferredCmd *redis.IntCmd
	_, err := rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		stateCmd = pipe.HGetAll(ctx, joinProtectionStateKey(chatID))
		cleanupCmd = pipe.HGetAll(ctx, telegramCleanupStateKey(chatID))
		deferredCmd = pipe.SCard(ctx, joinProtectionCleanupGroupKey(chatID))
		return nil
	})
	if err != nil {
		return JoinProtectionRuntimeStatus{}, err
	}
	state, err := stateCmd.Result()
	if err != nil {
		return JoinProtectionRuntimeStatus{}, err
	}
	cleanup, err := cleanupCmd.Result()
	if err != nil {
		return JoinProtectionRuntimeStatus{}, err
	}
	deferred, err := deferredCmd.Result()
	if err != nil {
		return JoinProtectionRuntimeStatus{}, err
	}

	status := JoinProtectionRuntimeStatus{
		State:                    "normal",
		Source:                   "redis",
		Trigger:                  state["current_trigger"],
		Intercepted:              parseRedisMapInt(state, "intercepted"),
		RecentJoins:              parseRedisMapInt(state, "recent_joins"),
		LastIntercepted:          parseRedisMapInt(state, "last_intercepted"),
		CleanupFailures:          parseRedisMapInt(cleanup, "failures"),
		LastCleanupError:         cleanup["last_error"],
		DeferredCleanupTaskCount: deferred,
	}
	status.ProtectionUntil = redisMapTime(state, "protection_until_ms")
	status.LastNotificationAt = redisMapTime(state, "last_notification_ms")
	status.LastEnteredAt = redisMapTime(state, "last_entered_at_ms")
	status.LastRecoveredAt = redisMapTime(state, "last_recovered_at_ms")
	status.CleanupCooldownUntil = redisMapTime(cleanup, "cooldown_until_ms")
	status.LastCleanupErrorAt = redisMapTime(cleanup, "last_error_at_ms")
	if status.ProtectionUntil != nil && status.ProtectionUntil.After(now) {
		status.State = "protecting"
	}
	if status.CleanupCooldownUntil != nil && status.CleanupCooldownUntil.After(now) {
		status.State = "cleanup_cooldown"
	}
	return status, nil
}

func allowTelegramCleanupRedis(ctx context.Context, rdb redis.Cmdable, chatID int64, now time.Time) (bool, time.Time, error) {
	value, err := rdb.HGet(ctx, telegramCleanupStateKey(chatID), "cooldown_until_ms").Result()
	if err != nil && err != redis.Nil {
		return false, time.Time{}, err
	}
	if err == redis.Nil || strings.TrimSpace(value) == "" {
		return true, time.Time{}, nil
	}
	millis, parseErr := strconv.ParseInt(value, 10, 64)
	if parseErr != nil {
		return false, time.Time{}, parseErr
	}
	until := time.UnixMilli(millis)
	return !now.Before(until), until, nil
}

func markTelegramCleanupSuccessRedis(ctx context.Context, rdb redis.Cmdable, chatID int64, now time.Time) error {
	key := telegramCleanupStateKey(chatID)
	_, err := rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, key, "cooldown_until_ms", 0, "failures", 0, "last_success_at_ms", now.UnixMilli())
		pipe.Expire(ctx, key, joinProtectionStateTTL)
		return nil
	})
	return err
}

func markTelegramCleanupFailureRedis(
	ctx context.Context,
	rdb redis.Cmdable,
	chatID int64,
	baseCooldown time.Duration,
	retryMinimum time.Duration,
	errorText string,
	now time.Time,
) (time.Time, bool, int, error) {
	values, err := failTelegramCleanupScript.Run(
		ctx,
		rdb,
		[]string{telegramCleanupStateKey(chatID)},
		now.UnixMilli(),
		baseCooldown.Milliseconds(),
		retryMinimum.Milliseconds(),
		time.Hour.Milliseconds(),
		errorText,
		joinProtectionStateTTL.Milliseconds(),
	).Slice()
	if err != nil {
		return time.Time{}, false, 0, err
	}
	if len(values) < 3 {
		return time.Time{}, false, 0, fmt.Errorf("unexpected cleanup breaker redis result length %d", len(values))
	}
	return time.UnixMilli(redisResultInt64(values[0])), redisResultInt64(values[1]) == 1, int(redisResultInt64(values[2])), nil
}

func enqueueJoinProtectionCleanupRedis(ctx context.Context, rdb redis.Cmdable, task joinProtectionDeferredCleanup, now time.Time) error {
	member := fmt.Sprintf("%d:%d", task.ChatID, task.UserID)
	if generation := strings.TrimSpace(task.MembershipGeneration); generation != "" {
		member += ":" + generation
	}
	action := strings.TrimSpace(task.Action)
	if action == "" {
		action = "temporary_ban"
	}
	rule := strings.TrimSpace(task.Rule)
	if rule == "" {
		rule = "join_protection_redis_fallback_cleanup"
	}
	actionUntilMillis := int64(0)
	if !task.ActionUntil.IsZero() {
		actionUntilMillis = task.ActionUntil.UnixMilli()
	}
	_, err := enqueueJoinProtectionCleanupScript.Run(
		ctx,
		rdb,
		[]string{
			joinProtectionCleanupTaskKey(member),
			joinProtectionCleanupQueueKey,
			joinProtectionCleanupGroupKey(task.ChatID),
			membershipSessionKey(task.ChatID, task.UserID),
		},
		task.ChatID,
		task.UserID,
		task.Username,
		task.FirstName,
		task.TemporaryBanSeconds,
		task.Reason,
		action,
		rule,
		now.UnixMilli(),
		actionUntilMillis,
		task.MembershipGeneration,
		member,
		membershipSessionTTL.Milliseconds(),
	).Result()
	return err
}

func claimDueJoinProtectionCleanupRedis(ctx context.Context, rdb redis.Cmdable, now time.Time, lease time.Duration) (joinProtectionDeferredCleanup, bool, error) {
	member, err := claimJoinProtectionCleanupScript.Run(
		ctx,
		rdb,
		[]string{joinProtectionCleanupQueueKey},
		now.UnixMilli(),
		now.Add(lease).UnixMilli(),
	).Text()
	if err != nil {
		return joinProtectionDeferredCleanup{}, false, err
	}
	if member == "" {
		return joinProtectionDeferredCleanup{}, false, nil
	}
	values, loadErr := rdb.HGetAll(ctx, joinProtectionCleanupTaskKey(member)).Result()
	if loadErr != nil {
		return joinProtectionDeferredCleanup{}, false, loadErr
	}
	if len(values) == 0 {
		chatID, _ := cleanupTaskMemberIDs(member)
		_ = rdb.ZRem(ctx, joinProtectionCleanupQueueKey, member).Err()
		if chatID != 0 {
			_ = rdb.SRem(ctx, joinProtectionCleanupGroupKey(chatID), member).Err()
		}
		_ = rdb.ZAdd(ctx, joinProtectionCleanupDeadKey, redis.Z{Score: float64(now.UnixMilli()), Member: member}).Err()
		return joinProtectionDeferredCleanup{}, false, fmt.Errorf("join protection cleanup task %q is missing its payload and was moved to dead letter", member)
	}
	createdAtMillis, _ := strconv.ParseInt(values["created_at_ms"], 10, 64)
	chatID, _ := strconv.ParseInt(values["chat_id"], 10, 64)
	userID, _ := strconv.ParseInt(values["user_id"], 10, 64)
	seconds, _ := strconv.Atoi(values["temporary_ban_seconds"])
	actionUntilMillis, _ := strconv.ParseInt(values["action_until_ms"], 10, 64)
	action := strings.TrimSpace(values["action"])
	if action == "" {
		action = "temporary_ban"
	}
	createdAt := time.UnixMilli(createdAtMillis)
	actionUntil := time.Time{}
	if actionUntilMillis > 0 {
		actionUntil = time.UnixMilli(actionUntilMillis)
	} else if action == joinProtectionActionTemporaryBan || action == joinProtectionActionTemporaryRestrict {
		actionUntil = createdAt.Add(time.Duration(seconds) * time.Second)
	}
	if chatID == 0 || userID == 0 || !validDeferredCleanupAction(action, seconds, actionUntil) {
		_ = rdb.ZRem(ctx, joinProtectionCleanupQueueKey, member).Err()
		if chatID != 0 {
			_ = rdb.SRem(ctx, joinProtectionCleanupGroupKey(chatID), member).Err()
		}
		_ = rdb.ZAdd(ctx, joinProtectionCleanupDeadKey, redis.Z{Score: float64(now.UnixMilli()), Member: member}).Err()
		return joinProtectionDeferredCleanup{}, false, fmt.Errorf("join protection cleanup task %q is invalid and was moved to dead letter", member)
	}
	_, _ = rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Persist(ctx, joinProtectionCleanupTaskKey(member))
		pipe.Persist(ctx, joinProtectionCleanupGroupKey(chatID))
		return nil
	})
	return joinProtectionDeferredCleanup{
		Member:               member,
		ChatID:               chatID,
		UserID:               userID,
		Username:             values["username"],
		FirstName:            values["first_name"],
		TemporaryBanSeconds:  seconds,
		ActionUntil:          actionUntil,
		Reason:               values["reason"],
		Action:               action,
		Rule:                 values["rule"],
		MembershipGeneration: values["membership_generation"],
		CreatedAt:            createdAt,
	}, true, nil
}

func validDeferredCleanupAction(action string, temporaryBanSeconds int, actionUntil time.Time) bool {
	switch action {
	case joinProtectionActionTemporaryBan, joinProtectionActionTemporaryRestrict:
		return temporaryBanSeconds > 0 && !actionUntil.IsZero()
	case "kick", "ban", "mute_permanent":
		return true
	default:
		return false
	}
}

func rescheduleJoinProtectionCleanupRedis(ctx context.Context, rdb redis.Cmdable, member string, retryAt time.Time) error {
	chatID, _ := cleanupTaskMemberIDs(member)
	_, err := rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZAdd(ctx, joinProtectionCleanupQueueKey, redis.Z{Score: float64(retryAt.UnixMilli()), Member: member})
		pipe.Persist(ctx, joinProtectionCleanupTaskKey(member))
		if chatID != 0 {
			pipe.Persist(ctx, joinProtectionCleanupGroupKey(chatID))
		}
		return nil
	})
	return err
}

func removeJoinProtectionCleanupRedis(ctx context.Context, rdb redis.Cmdable, member string, chatID int64) error {
	_, err := rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, joinProtectionCleanupTaskKey(member))
		pipe.ZRem(ctx, joinProtectionCleanupQueueKey, member)
		if chatID != 0 {
			pipe.SRem(ctx, joinProtectionCleanupGroupKey(chatID), member)
		}
		return nil
	})
	return err
}

func cancelJoinProtectionCleanupRedis(ctx context.Context, rdb redis.Cmdable, chatID, userID int64) error {
	base := fmt.Sprintf("%d:%d", chatID, userID)
	return cancelJoinProtectionCleanupScript.Run(
		ctx,
		rdb,
		[]string{joinProtectionCleanupGroupKey(chatID), joinProtectionCleanupQueueKey, membershipSessionKey(chatID, userID)},
		base,
		base+":",
		"clawguard:join-protection:cleanup-task:",
	).Err()
}

func cleanupTaskMemberIDs(member string) (int64, int64) {
	parts := strings.Split(member, ":")
	if len(parts) < 2 {
		return 0, 0
	}
	chatID, _ := strconv.ParseInt(parts[0], 10, 64)
	userID, _ := strconv.ParseInt(parts[1], 10, 64)
	return chatID, userID
}

func redisResultInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case string:
		parsed, _ := strconv.ParseInt(typed, 10, 64)
		return parsed
	case []byte:
		parsed, _ := strconv.ParseInt(string(typed), 10, 64)
		return parsed
	default:
		return 0
	}
}

func redisResultString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func parseRedisMapInt(values map[string]string, key string) int {
	parsed, _ := strconv.Atoi(values[key])
	return parsed
}

func redisMapTime(values map[string]string, key string) *time.Time {
	millis, _ := strconv.ParseInt(values[key], 10, 64)
	if millis <= 0 {
		return nil
	}
	value := time.UnixMilli(millis)
	return &value
}
