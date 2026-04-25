package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

const (
	defaultProbeInterval  = 60 * time.Second
	minProbeInterval      = 30 * time.Second
	mainProbeTickInterval = 15 * time.Second
	defaultProbeTimeout   = 10 * time.Second
	probeAlertCooldown    = 5 * time.Minute
	probeOrphanSweepEvery = 10 * time.Minute

	// probeFailStreakThreshold 连续失败多少次才把 healthy 翻成 false。
	// 之前每次失败立刻翻 false，导致 aw/sub2api 偶发 400/超时就触发告警。
	// 连续 3 次（约 3 分钟）才算真挂，避免误报。
	probeFailStreakThreshold = 3
)

// PolicyProvider exposes the global AI policy knobs the prober needs. We keep
// it tiny so tests can fake it trivially.
type PolicyProvider interface {
	AIPolicy() config.AIPolicy
}

// LLMNotifier sends proactive operator alerts when a model flips health.
type LLMNotifier interface {
	NotifyOwner(ctx context.Context, htmlMessage string) error
}

// LLMProber periodically pings every enabled model via the provider registry
// and upserts the results into llm_model_stats. Transitions (ok -> fail or
// fail -> ok) trigger throttled Telegram alerts to the primary admin.
type LLMProber struct {
	logger    *zap.Logger
	queries   *store.Queries
	providers ai.ProviderRegistry
	models    ai.ModelRegistry
	resolver  *ai.Resolver
	policy    PolicyProvider
	notifier  LLMNotifier
	redis     redis.Cmdable
	interval  time.Duration
	timeout   time.Duration

	mu         sync.Mutex
	failStreak map[int64]int
}

func NewLLMProber(
	logger *zap.Logger,
	queries *store.Queries,
	providers ai.ProviderRegistry,
	models ai.ModelRegistry,
	resolver *ai.Resolver,
	policy PolicyProvider,
	notifier LLMNotifier,
	rdb redis.Cmdable,
) *LLMProber {
	return &LLMProber{
		logger:     logger,
		queries:    queries,
		providers:  providers,
		models:     models,
		resolver:   resolver,
		policy:     policy,
		notifier:   notifier,
		redis:      rdb,
		interval:   defaultProbeInterval,
		timeout:    defaultProbeTimeout,
		failStreak: make(map[int64]int),
	}
}

// Run loops until ctx is cancelled. Called as `go worker.Run(ctx)`.
func (p *LLMProber) Run(ctx context.Context) {
	p.logger.Info("starting llm prober",
		zap.Duration("main_tick", mainProbeTickInterval),
		zap.Duration("default_interval", p.interval),
		zap.Duration("timeout", p.timeout))

	// One immediate pass on startup so we populate stats without waiting.
	p.tick(ctx)

	orphan := time.NewTicker(probeOrphanSweepEvery)
	defer orphan.Stop()

	for {
		tc := time.NewTimer(mainProbeTickInterval)
		select {
		case <-ctx.Done():
			tc.Stop()
			p.logger.Info("llm prober stopped")
			return
		case <-tc.C:
			p.tick(ctx)
		case <-orphan.C:
			tc.Stop()
			if err := p.queries.DeleteOrphanLLMStats(ctx); err != nil {
				p.logger.Warn("llm prober orphan sweep failed", zap.Error(err))
			}
		}
	}
}

func (p *LLMProber) currentInterval() time.Duration {
	policy := p.policy.AIPolicy()
	if policy.ProbeIntervalSeconds <= 0 {
		return p.interval
	}
	d := time.Duration(policy.ProbeIntervalSeconds) * time.Second
	if d < minProbeInterval {
		return minProbeInterval
	}
	return d
}

// tick evaluates every enabled model against its own schedule. Errors from a
// single model never stop the loop; each probe is its own unit.
func (p *LLMProber) tick(ctx context.Context) {
	policy := p.policy.AIPolicy()
	if !policy.ProbeEnabled {
		return
	}

	globalInterval := p.currentInterval()
	now := time.Now()
	enabled := true
	models := p.models.List(ai.ModelFilter{Enabled: &enabled})
	for _, m := range models {
		if !m.ProbeEnabled {
			continue
		}

		interval := globalInterval
		if m.ProbeIntervalSeconds > 0 {
			d := time.Duration(m.ProbeIntervalSeconds) * time.Second
			if d < minProbeInterval {
				d = minProbeInterval
			}
			interval = d
		}

		stats, err := p.queries.GetLLMModelStats(ctx, m.ID)
		if err == nil && stats.LastCheckAt != nil && now.Sub(*stats.LastCheckAt) < interval {
			continue
		}

		p.probeOne(ctx, m)
	}

	// Let the resolver pick up the freshly written stats right away.
	if p.resolver != nil {
		p.resolver.Refresh(ctx)
	}
}

func (p *LLMProber) probeOne(ctx context.Context, model ai.Model) {
	ref := ai.NewModelRef(model.ProviderKey, model.ModelKey)
	client, ok := p.providers.Client(model.ProviderKey)
	if !ok {
		p.logger.Debug("probe skipped: no client for provider",
			zap.String("ref", string(ref)))
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	_, probeErr := client.Probe(probeCtx, model.ModelKey)

	checkedAt := time.Now().UTC()
	probeOK := probeErr == nil

	prev, loadErr := p.queries.GetLLMModelStats(ctx, model.ID)
	hadPrev := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, pgx.ErrNoRows) {
		p.logger.Warn("load prior probe state failed", zap.Error(loadErr), zap.String("ref", string(ref)))
	}

	// 连续失败计数：只有连续失败到阈值才把 healthy 标 false
	// 避免 aw/sub2api 偶发 400/抖动立刻触发告警
	p.mu.Lock()
	streak := p.failStreak[model.ID]
	if probeOK {
		streak = 0
	} else {
		if streak == 0 && hadPrev && !prev.Healthy {
			streak = probeFailStreakThreshold - 1
		}
		streak++
	}
	p.failStreak[model.ID] = streak
	p.mu.Unlock()

	healthy := streak < probeFailStreakThreshold
	var okAt *time.Time
	errText := ""
	if probeOK {
		t := checkedAt
		okAt = &t
	} else {
		errText = truncateError(probeErr.Error())
		if !healthy {
			p.logger.Warn("llm probe failed consecutively",
				zap.String("ref", string(ref)),
				zap.Int("streak", streak),
				zap.String("error", errText))
		} else {
			p.logger.Info("llm probe transient failure (not yet unhealthy)",
				zap.String("ref", string(ref)),
				zap.Int("streak", streak),
				zap.Int("threshold", probeFailStreakThreshold),
				zap.String("error", errText))
		}
	}

	if err := p.queries.UpsertLLMProbeResult(ctx, store.UpsertLLMProbeResultParams{
		ModelID:     model.ID,
		Healthy:     healthy,
		LastCheckAt: &checkedAt,
		LastOkAt:    okAt,
		LastError:   errText,
	}); err != nil {
		p.logger.Warn("upsert probe result failed", zap.Error(err), zap.String("ref", string(ref)))
		return
	}

	// Only announce transitions. First-ever observation treats "default
	// healthy=true" as the implicit prior to avoid spamming on startup.
	previouslyHealthy := true
	if hadPrev {
		previouslyHealthy = prev.Healthy
	}
	if previouslyHealthy == healthy {
		return
	}

	p.emitTransition(ctx, ref, healthy, errText)
}

func (p *LLMProber) emitTransition(ctx context.Context, ref ai.ModelRef, healthy bool, lastErr string) {
	if p.notifier == nil {
		return
	}

	if p.redis != nil {
		key := fmt.Sprintf("alert:llm:%s", string(ref))
		ok, err := p.redis.SetNX(ctx, key, "1", probeAlertCooldown).Result()
		if err != nil {
			p.logger.Warn("probe alert cooldown check failed", zap.Error(err))
			return
		}
		if !ok {
			return
		}
	}

	var msg string
	if healthy {
		msg = fmt.Sprintf("✅ 模型 <code>%s</code> 已恢复", escapeHTML(string(ref)))
	} else {
		msg = fmt.Sprintf("⚠️ 模型 <code>%s</code> 探活失败\n<pre>%s</pre>", escapeHTML(string(ref)), escapeHTML(lastErr))
	}

	if err := p.notifier.NotifyOwner(ctx, msg); err != nil {
		p.logger.Warn("notify llm transition failed", zap.Error(err), zap.String("ref", string(ref)))
	}
}

func truncateError(s string) string {
	s = strings.TrimSpace(s)
	const max = 400
	if utf8.RuneCountInString(s) > max {
		return string([]rune(s)[:max]) + "…"
	}
	return s
}

func escapeHTML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return r.Replace(s)
}

// OwnerNotifier adapts the bot Service into an LLMNotifier.
type OwnerNotifier struct {
	Bot     *bot.Service
	Queries *store.Queries
	Logger  *zap.Logger
}

func (o *OwnerNotifier) NotifyOwner(ctx context.Context, htmlMessage string) error {
	if o == nil || o.Bot == nil || o.Queries == nil {
		return nil
	}
	admin, err := o.Queries.GetFirstOwnerAdmin(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if admin.TelegramID == 0 {
		return nil
	}
	return o.Bot.SendHTMLPrivateMessage(admin.TelegramID, htmlMessage)
}

// GlobalPolicyProvider loads the merged global AI policy on every call. It is
// fast enough (single-digit ms): queries are small, Postgres is local.
type GlobalPolicyProvider struct {
	Queries *store.Queries
}

func (g *GlobalPolicyProvider) AIPolicy() config.AIPolicy {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	policy, err := config.LoadPolicy(ctx, g.Queries, 0)
	if err != nil {
		return config.DefaultPolicy.AI
	}
	return policy.AI
}
