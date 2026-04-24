package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

const messageBasePrompt = `你是一个中文群聊反垃圾审核员。判断下面的消息属于哪类：
- normal（正常对话）
- ad（商业广告、引流、招聘、交友、币圈、刷单等）
- scam（诈骗）
- spam（无意义刷屏）
- harass（骚扰辱骂）
- porn（色情）
- violence（暴力、血腥、极端内容）

输出 JSON：
{"items":[{"verdict":"...","confidence":0.0-1.0,"category":"招聘/交友/币圈/刷单/引流/政治/色情/正常","reason":"简短中文解释"}]}

管理员自定义规则（如无则忽略）：
%s

如果有图片，判断图片中的文字和画面内容（广告、二维码、色情、收款码、联系方式等）。

如果消息包含【跨聊天引用】块，表示用户从其他群/频道引用消息到本群。
广告号常用此方式引流色情/诈骗/赌博频道：自己只发空白或单字，让被引用的频道内容代为铺陈。
只要原消息来自陌生频道/bot 且涉及色情、赌博、诈骗、引流，即使本次消息本身无文字，也必须判定为 ad、scam、porn 或 violence。
【引用回复】【引用片段】同理，也要把被引用的内容纳入判断。
如果审核内容里有【链接预览】或【无法展开的 Telegram 链接】标记，说明用户消息里嵌了 Telegram 频道/消息链接。把链接预览的标题/描述视为用户本次发送的实际内容来判定，引流型内容判 ad，色情/赌博/诈骗按对应 verdict 判。

消息：
%s`

const bioBasePrompt = `你是一个中文 Telegram 用户资料简介审核员。判断下面的用户简介属于哪类：
- normal（正常简介）
- ad（商业广告、引流、招聘、交友、币圈、刷单等）
- scam（诈骗）
- spam（堆砌关键词、无意义刷屏式简介）
- harass（骚扰辱骂、攻击性内容）
- porn（色情引流）
- violence（暴力、血腥、极端内容）

输出 JSON：
{"items":[{"verdict":"...","confidence":0.0-1.0,"category":"招聘/交友/币圈/刷单/引流/政治/色情/暴力/正常","reason":"简短中文解释"}]}

管理员自定义规则（如无则忽略）：
%s

判定要点：
- 简介为空、或仅是普通自我介绍（职业、爱好、所在地、兴趣标签等）一律 normal。
- 留 Telegram/WhatsApp 链接、TG 频道/群组邀请、@用户名引流、加 vx/微信、TRC20/USDT/收款方式、境外博彩、刷单兼职，按对应 verdict 判，置信度通常 >= 0.7。
- verdict 与 category 不允许矛盾：normal 必须搭配"正常"，其他 verdict 不允许搭配"正常"。
- 没有"消息列表"概念，每次只判一条简介。

简介：
%s`

type Moderator struct {
	logger    *zap.Logger
	redis     redis.Cmdable
	queries   *store.Queries
	providers ProviderRegistry
	models    ModelRegistry
	resolver  *Resolver
	status    interface {
		MarkAISuccess()
		MarkAIFailure(error)
	}

	mu      sync.Mutex
	batches map[string]*pendingBatch
}

type CheckInput struct {
	ChatID      int64
	UserID      int64
	Text        string
	Scene       string
	SenderName  string
	ForwardFrom string // 转发来源（频道名/用户名）
	ImageBase64 string
	ImageHash   string
	Policy      config.AIPolicy
	SkipCache   bool
}

type CheckOutput struct {
	Verdict       Verdict `json:"verdict"`
	Model         string  `json:"model"`
	ProviderID    int64   `json:"provider_id,omitempty"`
	ModelID       int64   `json:"model_id,omitempty"`
	PromptVersion string  `json:"prompt_version"`
	LatencyMs     int     `json:"latency_ms"`
	CostCents     float64 `json:"cost_cents"`
	Cached        bool    `json:"cached"`
	FlagOnly      bool    `json:"flag_only"`
	Skipped       bool    `json:"skipped"`
}

type pendingBatch struct {
	inputs  []CheckInput
	waiters []chan batchResult
}

type batchResult struct {
	output CheckOutput
	err    error
}

func normalizeScene(scene string) string {
	scene = strings.TrimSpace(strings.ToLower(scene))
	if scene == "bio" {
		return "bio"
	}
	return "message"
}

func NewModerator(logger *zap.Logger, redis redis.Cmdable, queries *store.Queries, providers ProviderRegistry, models ModelRegistry, resolver *Resolver, status interface {
	MarkAISuccess()
	MarkAIFailure(error)
}) *Moderator {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Moderator{
		logger:    logger,
		redis:     redis,
		queries:   queries,
		providers: providers,
		models:    models,
		resolver:  resolver,
		batches:   map[string]*pendingBatch{},
		status:    status,
	}
}

func (m *Moderator) CheckMessage(ctx context.Context, input CheckInput) (CheckOutput, error) {
	if strings.TrimSpace(input.Text) == "" && strings.TrimSpace(input.ImageBase64) == "" {
		return CheckOutput{Skipped: true}, nil
	}
	state, err := m.getSystemState(ctx)
	if err == nil {
		if state.AIPaused {
			return CheckOutput{Skipped: true}, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		m.logger.Warn("load system state failed, allow ai path", zap.Error(err))
	}
	if input.Policy.SkipMessagesShorterThan > 0 && len([]rune(strings.TrimSpace(input.Text))) < input.Policy.SkipMessagesShorterThan {
		return CheckOutput{Skipped: true}, nil
	}
	if !input.SkipCache {
		if output, ok, err := m.fromCache(ctx, input); err == nil && ok {
			output.Cached = true
			return output, nil
		} else if err != nil {
			m.logger.Warn("load ai cache failed", zap.Error(err))
		}
	}
	if over, err := m.overPerUserLimit(ctx, input); err == nil && over {
		return CheckOutput{Skipped: true}, nil
	}
	if over, err := m.overBudget(ctx, input.ChatID, input.Policy); err == nil && over {
		if lockErr := m.lockBudget(ctx); lockErr != nil {
			m.logger.Warn("lock ai budget failed", zap.Error(lockErr))
		}
		return CheckOutput{Skipped: true}, nil
	}

	key := strconv.FormatInt(input.ChatID, 10) + ":" + strconv.FormatInt(input.UserID, 10) + ":" + normalizeScene(input.Scene)
	waiter := make(chan batchResult, 1)
	m.mu.Lock()
	batch := m.batches[key]
	if batch == nil {
		batch = &pendingBatch{}
		m.batches[key] = batch
		window := input.Policy.BatchWindowMs
		if window <= 0 {
			window = 500
		}
		safeGo(context.Background(), m.logger, func() {
			m.flushBatch(key, time.Duration(window)*time.Millisecond)
		})
	}
	batch.inputs = append(batch.inputs, input)
	batch.waiters = append(batch.waiters, waiter)
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		return CheckOutput{}, ctx.Err()
	case result := <-waiter:
		return result.output, result.err
	}
}

func (m *Moderator) flushBatch(key string, window time.Duration) {
	time.Sleep(window)
	m.mu.Lock()
	batch := m.batches[key]
	delete(m.batches, key)
	m.mu.Unlock()
	if batch == nil || len(batch.inputs) == 0 {
		return
	}

	batchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	outputs, err := m.checkBatch(batchCtx, batch.inputs)
	if err != nil {
		for _, waiter := range batch.waiters {
			waiter <- batchResult{err: err}
		}
		return
	}
	for index, waiter := range batch.waiters {
		waiter <- batchResult{output: outputs[index]}
	}
}

func (m *Moderator) checkBatch(ctx context.Context, inputs []CheckInput) ([]CheckOutput, error) {
	for _, input := range inputs {
		if strings.TrimSpace(input.ImageBase64) != "" {
			return m.checkIndividually(ctx, inputs)
		}
	}

	policy := inputs[0].Policy
	promptVersion := "m5-v1"
	scene := normalizeScene(inputs[0].Scene)
	rules := policy.MessageRules
	if scene == "bio" {
		rules = policy.BioRules
	}
	prompt := buildPrompt(scene, rules, inputs)
	flagOnly, _ := m.overBudget(ctx, inputs[0].ChatID, policy)
	modelChain, _ := m.resolver.BuildChain(policy, []string{"moderation"})

	var lastErr error
	failureCount := 0
	for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
		for _, ref := range modelChain {
			model, ok := m.models.Get(ref)
			if !ok {
				lastErr = fmt.Errorf("model %q not found", ref)
				failureCount++
				m.warnAICallFailed(ref, Model{}, attempt, 0, lastErr)
				continue
			}
			client, ok := m.providers.Client(model.ProviderKey)
			if !ok {
				lastErr = fmt.Errorf("provider %q not available", model.ProviderKey)
				failureCount++
				m.warnAICallFailed(ref, model, attempt, 0, lastErr)
				continue
			}
			timeout := effectiveTimeout(policy.TimeoutMs, model.ProviderTimeout)
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			result, err := client.Check(callCtx, CheckRequest{
				Model:        model.ModelKey,
				SystemPrompt: prompt,
				Messages: []Message{
					{Role: "user", Content: "请严格返回 JSON。"},
				},
				MaxTokens:   512,
				Temperature: policy.Temperature,
				Timeout:     timeout,
			})
			cancel()
			if err != nil {
				lastErr = err
				failureCount++
				m.warnAICallFailed(ref, model, attempt, timeout, err)
				if m.status != nil {
					m.status.MarkAIFailure(err)
				}
				continue
			}
			if len(result.Verdicts) != len(inputs) {
				lastErr = fmt.Errorf("llm verdict count mismatch")
				failureCount++
				m.warnAICallFailed(ref, model, attempt, timeout, lastErr)
				if m.status != nil {
					m.status.MarkAIFailure(lastErr)
				}
				continue
			}

			outputs := make([]CheckOutput, len(inputs))
			for index, verdict := range result.Verdicts {
				outputs[index] = CheckOutput{
					Verdict:       m.normalizeVerdict(verdict),
					Model:         ref.String(),
					ProviderID:    model.ProviderID,
					ModelID:       model.ID,
					PromptVersion: promptVersion,
					LatencyMs:     result.LatencyMs,
					CostCents:     result.CostCents / float64(maxInt(1, len(inputs))),
					FlagOnly:      flagOnly,
				}
				_ = m.saveCache(context.Background(), inputs[index], outputs[index])
				_ = m.bumpUserCounter(context.Background(), inputs[index])
				_ = m.BumpBudget(context.Background(), inputs[index].ChatID, outputs[index].CostCents)
			}
			if m.status != nil {
				m.status.MarkAISuccess()
			}
			return outputs, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no ai provider available")
	}
	if failureCount > 1 {
		m.logger.Warn("ai moderation exhausted all models",
			zap.Int("model_count", len(modelChain)),
			zap.Int("max_retries", policy.MaxRetries),
			zap.Error(lastErr))
	}
	return nil, lastErr
}

func (m *Moderator) checkIndividually(ctx context.Context, inputs []CheckInput) ([]CheckOutput, error) {
	outputs := make([]CheckOutput, 0, len(inputs))
	for _, input := range inputs {
		output, err := m.checkSingle(ctx, input)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}

func (m *Moderator) checkSingle(ctx context.Context, input CheckInput) (CheckOutput, error) {
	policy := input.Policy
	promptVersion := "m5-v1"
	scene := normalizeScene(input.Scene)
	rules := policy.MessageRules
	if scene == "bio" {
		rules = policy.BioRules
	}
	prompt := buildPrompt(scene, rules, []CheckInput{input})
	flagOnly, _ := m.overBudget(ctx, input.ChatID, policy)
	// Include vision capability when this message carries an image so the
	// resolver filters out moderation-only text models that cannot read
	// image_url payloads.
	caps := []string{"moderation"}
	if strings.TrimSpace(input.ImageBase64) != "" {
		caps = append(caps, "vision")
	}
	modelChain, _ := m.resolver.BuildChain(policy, caps)

	var lastErr error
	failureCount := 0
	for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
		for _, ref := range modelChain {
			model, ok := m.models.Get(ref)
			if !ok {
				lastErr = fmt.Errorf("model %q not found", ref)
				failureCount++
				m.warnAICallFailed(ref, Model{}, attempt, 0, lastErr)
				continue
			}
			client, ok := m.providers.Client(model.ProviderKey)
			if !ok {
				lastErr = fmt.Errorf("provider %q not available", model.ProviderKey)
				failureCount++
				m.warnAICallFailed(ref, model, attempt, 0, lastErr)
				continue
			}
			timeout := effectiveTimeout(policy.TimeoutMs, model.ProviderTimeout)

			callCtx, cancel := context.WithTimeout(ctx, timeout)
			result, err := client.Check(callCtx, CheckRequest{
				Model:        model.ModelKey,
				SystemPrompt: prompt,
				Messages: []Message{
					{Role: "user", Content: visionRequestContent(input, model.SupportsVision)},
				},
				MaxTokens:   512,
				Temperature: policy.Temperature,
				Timeout:     timeout,
			})
			cancel()
			if err != nil {
				lastErr = err
				failureCount++
				m.warnAICallFailed(ref, model, attempt, timeout, err)
				if m.status != nil {
					m.status.MarkAIFailure(err)
				}
				continue
			}
			if len(result.Verdicts) != 1 {
				lastErr = fmt.Errorf("llm verdict count mismatch")
				failureCount++
				m.warnAICallFailed(ref, model, attempt, timeout, lastErr)
				if m.status != nil {
					m.status.MarkAIFailure(lastErr)
				}
				continue
			}

			output := CheckOutput{
				Verdict:       m.normalizeVerdict(result.Verdicts[0]),
				Model:         ref.String(),
				ProviderID:    model.ProviderID,
				ModelID:       model.ID,
				PromptVersion: promptVersion,
				LatencyMs:     result.LatencyMs,
				CostCents:     result.CostCents,
				FlagOnly:      flagOnly,
			}
			_ = m.saveCache(context.Background(), input, output)
			_ = m.bumpUserCounter(context.Background(), input)
			_ = m.BumpBudget(context.Background(), input.ChatID, output.CostCents)
			if m.status != nil {
				m.status.MarkAISuccess()
			}
			return output, nil
		}
	}

	if lastErr == nil {
		lastErr = errors.New("no ai provider available")
	}
	if failureCount > 1 {
		m.logger.Warn("ai moderation exhausted all models",
			zap.Int("model_count", len(modelChain)),
			zap.Int("max_retries", policy.MaxRetries),
			zap.Error(lastErr))
	}
	return CheckOutput{}, lastErr
}

func effectiveTimeout(policyMs int, providerTimeout time.Duration) time.Duration {
	policyTimeout := time.Duration(policyMs) * time.Millisecond
	if policyTimeout <= 0 {
		policyTimeout = 10 * time.Second
	}
	if providerTimeout > 0 && providerTimeout < policyTimeout {
		return providerTimeout
	}
	return policyTimeout
}

func (m *Moderator) warnAICallFailed(ref ModelRef, model Model, attempt int, timeout time.Duration, err error) {
	if m.logger == nil {
		return
	}
	m.logger.Warn("ai call failed, trying next model",
		zap.String("ref", string(ref)),
		zap.String("provider", model.ProviderKey),
		zap.String("model", model.ModelKey),
		zap.Int("attempt", attempt),
		zap.Int("timeout_ms", int(timeout/time.Millisecond)),
		zap.Error(err),
	)
}

func buildPrompt(scene string, customRules string, inputs []CheckInput) string {
	base := messageBasePrompt
	if normalizeScene(scene) == "bio" {
		base = bioBasePrompt
	}
	lines := make([]string, 0, len(inputs))
	for index, input := range inputs {
		msgLine := strings.TrimSpace(input.Text)
		if sn := strings.TrimSpace(input.SenderName); sn != "" {
			msgLine = fmt.Sprintf("[用户昵称: %s] %s", sn, msgLine)
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, msgLine))
	}
	return fmt.Sprintf(base, strings.TrimSpace(customRules), strings.Join(lines, "\n"))
}

// BuildPromptPreview renders the full prompt for the requested scene.
func BuildPromptPreview(scene, customRules, sampleText string) string {
	if strings.TrimSpace(sampleText) == "" {
		sampleText = "<示例文本>"
	}
	return buildPrompt(scene, customRules, []CheckInput{{Text: sampleText}})
}

func (m *Moderator) normalizeVerdict(verdict Verdict) Verdict {
	raw := strings.TrimSpace(strings.ToLower(verdict.Verdict))
	allowed := map[string]struct{}{
		"normal":   {},
		"ad":       {},
		"scam":     {},
		"spam":     {},
		"harass":   {},
		"porn":     {},
		"violence": {},
	}
	if _, ok := allowed[raw]; !ok {
		if raw != "" && m != nil && m.logger != nil {
			m.logger.Warn("unknown ai verdict downgraded to normal", zap.String("verdict", raw))
		}
		verdict.Verdict = "normal"
		if verdict.Confidence == 0 {
			verdict.Confidence = 0.5
		}
	} else {
		verdict.Verdict = raw
	}
	if verdict.Category == "" {
		verdict.Category = "正常"
	}
	if verdict.Confidence < 0 {
		verdict.Confidence = 0
	}
	if verdict.Confidence > 1 {
		verdict.Confidence = 1
	}
	return verdict
}

func parseVerdicts(content string) ([]Verdict, error) {
	raw := trimJSON(content)
	var payload struct {
		Items []Verdict `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err == nil && len(payload.Items) > 0 {
		return payload.Items, nil
	}
	var one Verdict
	if err := json.Unmarshal([]byte(raw), &one); err == nil && one.Verdict != "" {
		return []Verdict{one}, nil
	}
	var many []Verdict
	if err := json.Unmarshal([]byte(raw), &many); err == nil && len(many) > 0 {
		return many, nil
	}
	return nil, fmt.Errorf("parse llm verdict json failed")
}

func trimJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(raw, "```")
		raw = strings.TrimSpace(raw)
	}
	start := strings.IndexAny(raw, "[{")
	end := strings.LastIndexAny(raw, "]}")
	if start >= 0 && end >= start {
		return raw[start : end+1]
	}
	return raw
}

func (m *Moderator) cacheKey(input CheckInput) string {
	scene := normalizeScene(input.Scene)
	policyHash := m.policyFingerprint(scene, input.Policy)
	prefix := "ai:cache:" + strconv.FormatInt(input.ChatID, 10) + ":" + scene + ":" + policyHash
	if strings.TrimSpace(input.ImageHash) != "" {
		return prefix + ":image:" + strings.TrimSpace(input.ImageHash)
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(input.Text))))
	return prefix + ":text:" + hex.EncodeToString(sum[:])
}

func (m *Moderator) policyFingerprint(scene string, policy config.AIPolicy) string {
	rules := policy.MessageRules
	if normalizeScene(scene) == "bio" {
		rules = policy.BioRules
	}
	payload, err := json.Marshal(struct {
		Scene             string              `json:"scene"`
		Rules             string              `json:"rules"`
		PrimaryProvider   string              `json:"primary_provider"`
		PrimaryModel      string              `json:"primary_model"`
		PrimaryModelRef   string              `json:"primary_model_ref"`
		FallbackChain     []string            `json:"fallback_chain"`
		FallbackModelRefs []string            `json:"fallback_model_refs"`
		ActionsByCategory map[string]string   `json:"actions_by_category"`
		Thresholds        config.AIThresholds `json:"thresholds"`
	}{
		Scene:             normalizeScene(scene),
		Rules:             strings.TrimSpace(rules),
		PrimaryProvider:   strings.TrimSpace(policy.PrimaryProvider),
		PrimaryModel:      strings.TrimSpace(policy.PrimaryModel),
		PrimaryModelRef:   strings.TrimSpace(policy.PrimaryModelRef),
		FallbackChain:     policy.FallbackChain,
		FallbackModelRefs: policy.FallbackModelRefs,
		ActionsByCategory: policy.ActionsByCategory,
		Thresholds:        policy.Thresholds,
	})
	if err != nil {
		sum := sha256.Sum256([]byte(normalizeScene(scene) + "|" + strings.TrimSpace(rules) + "|" + strings.TrimSpace(policy.PrimaryModel)))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:8])
}

func visionRequestContent(input CheckInput, includeImage bool) any {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		text = "[图片]"
	}
	if !includeImage || strings.TrimSpace(input.ImageBase64) == "" {
		return "请严格返回 JSON。\n\n" + text
	}
	return []CheckContentPart{
		{Type: "text", Text: "请严格返回 JSON。\n\n" + text},
		{Type: "image_url", ImageURL: map[string]string{"url": "data:image/jpeg;base64," + input.ImageBase64}},
	}
}

func (m *Moderator) fromCache(ctx context.Context, input CheckInput) (CheckOutput, bool, error) {
	if m.redis == nil {
		return CheckOutput{}, false, nil
	}
	raw, err := m.redis.Get(ctx, m.cacheKey(input)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			_ = m.redis.Incr(ctx, "ai:cache:miss").Err()
			return CheckOutput{}, false, nil
		}
		return CheckOutput{}, false, err
	}
	var output CheckOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		return CheckOutput{}, false, err
	}
	_ = m.redis.Incr(ctx, "ai:cache:hit").Err()
	return output, true, nil
}

func (m *Moderator) saveCache(ctx context.Context, input CheckInput, output CheckOutput) error {
	if m.redis == nil {
		return nil
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return err
	}
	ttl := time.Duration(input.Policy.CacheTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return m.redis.Set(ctx, m.cacheKey(input), raw, ttl).Err()
}

func (m *Moderator) overBudget(ctx context.Context, chatID int64, policy config.AIPolicy) (bool, error) {
	if m.redis == nil || policy.DailyBudgetCents <= 0 {
		return false, nil
	}
	key := "ai:budget:" + time.Now().Format("2006-01-02") + ":" + strconv.FormatInt(chatID, 10)
	value, err := m.redis.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, err
	}
	used, _ := strconv.ParseFloat(value, 64)
	return int(used) >= policy.DailyBudgetCents, nil
}

func (m *Moderator) overPerUserLimit(ctx context.Context, input CheckInput) (bool, error) {
	if m.redis == nil || input.Policy.PerUserDailyLimit <= 0 {
		return false, nil
	}
	key := "ai:usercount:" + time.Now().Format("2006-01-02") + ":" + strconv.FormatInt(input.ChatID, 10) + ":" + strconv.FormatInt(input.UserID, 10)
	value, err := m.redis.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, err
	}
	count, _ := strconv.Atoi(value)
	return count >= input.Policy.PerUserDailyLimit, nil
}

func (m *Moderator) bumpUserCounter(ctx context.Context, input CheckInput) error {
	if m.redis == nil {
		return nil
	}
	key := "ai:usercount:" + time.Now().Format("2006-01-02") + ":" + strconv.FormatInt(input.ChatID, 10) + ":" + strconv.FormatInt(input.UserID, 10)
	pipe := m.redis.TxPipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 48*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

func (m *Moderator) BumpBudget(ctx context.Context, chatID int64, costCents float64) error {
	if m.redis == nil || costCents <= 0 {
		return nil
	}
	key := "ai:budget:" + time.Now().Format("2006-01-02") + ":" + strconv.FormatInt(chatID, 10)
	pipe := m.redis.TxPipeline()
	pipe.IncrByFloat(ctx, key, costCents)
	pipe.Expire(ctx, key, 48*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

func (m *Moderator) getSystemState(ctx context.Context) (store.SystemState, error) {
	if m.queries == nil {
		return store.SystemState{}, pgx.ErrNoRows
	}
	return m.queries.GetSystemState(ctx)
}

func (m *Moderator) lockBudget(ctx context.Context) error {
	if m.queries == nil {
		return nil
	}
	state, err := m.queries.GetSystemState(ctx)
	if err != nil {
		return err
	}
	if state.AIBudgetLocked && state.AIPaused {
		return nil
	}
	today := time.Now().Truncate(24 * time.Hour)
	updated, err := m.queries.UpdateSystemState(ctx, store.UpdateSystemStateParams{
		AIPaused:           true,
		ActionsPaused:      state.ActionsPaused,
		Frozen:             state.Frozen,
		AIPausedReason:     "daily budget exhausted",
		AIBudgetLocked:     true,
		AIBudgetLockedDate: &today,
		UpdatedBy:          nil,
	})
	if err != nil {
		return err
	}
	diff := "changed keys: ai_paused, ai_paused_reason, ai_budget_locked, ai_budget_locked_date"
	beforeRaw, _ := json.Marshal(state)
	afterRaw, _ := json.Marshal(updated)
	_, auditErr := m.queries.InsertAuditEntry(ctx, store.InsertAuditEntryParams{
		Scope:   "global",
		ChatID:  nil,
		AdminID: 0,
		Action:  "ai_budget_lock",
		Before:  beforeRaw,
		After:   afterRaw,
		Diff:    &diff,
	})
	if auditErr != nil {
		m.logger.Warn("write ai budget audit failed", zap.Error(auditErr))
	}
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
