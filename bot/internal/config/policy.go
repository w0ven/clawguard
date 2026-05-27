package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/openclaw/clawguard/internal/store"
)

type GuardPolicy struct {
	Verify   VerifyPolicy         `json:"verify"`
	Filter   FilterConfig         `json:"filter"`
	Messages MessagesPolicy       `json:"messages"`
	AntiSpam AntiSpamPolicy       `json:"anti_spam"`
	Warnings WarningsConfig       `json:"warnings"`
	Logging  LoggingConfig        `json:"logging"`
	AI       AIPolicy             `json:"ai"`
	Feedback ActionFeedbackPolicy `json:"feedback"`
}

// ActionFeedback 单个动作的群内反馈配置
type ActionFeedback struct {
	Enabled           bool   `json:"enabled"`
	Template          string `json:"template"`
	AutoDeleteSeconds int    `json:"auto_delete_seconds"`
	ReplyToMessage    bool   `json:"reply_to_message"`
	ParseMode         string `json:"parse_mode"`
}

// ActionFeedbackPolicy 各种动作的反馈配置
type ActionFeedbackPolicy struct {
	DeleteMsg      ActionFeedback `json:"delete_msg"`
	Mute           ActionFeedback `json:"mute"`
	Kick           ActionFeedback `json:"kick"`
	Ban            ActionFeedback `json:"ban"`
	Warn           ActionFeedback `json:"warn"`
	VerifyPass     ActionFeedback `json:"verify_pass"`
	VerifyFail     ActionFeedback `json:"verify_fail"`
	CASHit         ActionFeedback `json:"cas_hit"`
	TrustGraduated ActionFeedback `json:"trust_graduated"`
	AdminAction    ActionFeedback `json:"admin_action"`
}

type VerifyPolicy struct {
	Enabled           bool                 `json:"enabled"`
	Method            string               `json:"method"`
	TimeoutSeconds    int                  `json:"timeout_seconds"`
	FailAction        string               `json:"fail_action"`
	DeleteJoinMessage bool                 `json:"delete_join_message"`
	WelcomeMessage    WelcomeMessageConfig `json:"welcome_message"`
	CheckProfile      bool                 `json:"check_profile"`
	// ProfileCheckMode: keyword | ai | off
	ProfileCheckMode string   `json:"profile_check_mode"`
	ProfileBlacklist []string `json:"profile_blacklist"`
}

type WelcomeMessageConfig struct {
	Enabled            bool    `json:"enabled"`
	Template           *string `json:"template"`
	RulesLink          string  `json:"rules_link"`
	DeleteAfterSeconds int     `json:"delete_after_seconds"`
	ParseMode          string  `json:"parse_mode"`
}

func (w *WelcomeMessageConfig) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	switch raw {
	case "", "null":
		*w = WelcomeMessageConfig{}
		return nil
	}

	if len(raw) > 0 && raw[0] == '"' {
		var legacy string
		if err := json.Unmarshal(data, &legacy); err != nil {
			return err
		}
		*w = WelcomeMessageConfig{
			Enabled:            true,
			Template:           stringPtr(strings.TrimSpace(legacy)),
			DeleteAfterSeconds: DefaultPolicy.Verify.WelcomeMessage.DeleteAfterSeconds,
		}
		return nil
	}

	type alias WelcomeMessageConfig
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*w = WelcomeMessageConfig(decoded)
	return nil
}

func (w WelcomeMessageConfig) MarshalJSON() ([]byte, error) {
	type alias WelcomeMessageConfig
	return json.Marshal(alias(w))
}

type FilterConfig struct {
	Keywords                 FilterKeywordPolicy  `json:"keywords"`
	Regex                    FilterRegexPolicy    `json:"regex"`
	Links                    FilterLinksPolicy    `json:"links"`
	Usernames                FilterUsernamePolicy `json:"usernames"`
	NewUser                  FilterNewUserPolicy  `json:"new_user"`
	NonTextMessages          string               `json:"non_text_messages"`
	OtherBotsAction          string               `json:"other_bots_action"` // "audit" | "kick" | "ban" | "off"
	BotWhitelist             []string             `json:"bot_whitelist"`     // username (without @)
	BanBotInviterOnViolation bool                 `json:"ban_bot_inviter_on_violation"`
	BanSenderChats           bool                 `json:"ban_sender_chats"`
}

type FilterKeywordPolicy struct {
	Enabled       bool     `json:"enabled"`
	List          []string `json:"list"`
	Action        string   `json:"action"`
	CaseSensitive bool     `json:"case_sensitive"`
}

type FilterRegexPolicy struct {
	Enabled  bool     `json:"enabled"`
	Patterns []string `json:"patterns"`
}

type FilterLinksPolicy struct {
	Enabled      bool     `json:"enabled"`
	Whitelist    []string `json:"whitelist"`
	Action       string   `json:"action"`
	ExemptAdmins bool     `json:"exempt_admins"`
}

type FilterUsernamePolicy struct {
	Enabled   bool     `json:"enabled"`
	Blacklist []string `json:"blacklist"`
}

type FilterNewUserPolicy struct {
	Enabled              bool `json:"enabled"`
	DurationHours        int  `json:"duration_hours"`
	NoLinks              bool `json:"no_links"`
	NoForwards           bool `json:"no_forwards"`
	NoMedia              bool `json:"no_media"`
	MaxMessagesPerMinute int  `json:"max_messages_per_minute"`
}

type MessagesPolicy struct {
	KeywordReplies []KeywordReplyRule `json:"keyword_replies"`
}

type KeywordReplyRule struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Enabled           bool     `json:"enabled"`
	MatchType         string   `json:"match_type"`
	Keywords          []string `json:"keywords"`
	CaseSensitive     bool     `json:"case_sensitive"`
	ReplyText         string   `json:"reply_text"`
	AutoDeleteSeconds int      `json:"auto_delete_seconds"`
	CooldownSeconds   int      `json:"cooldown_seconds"`
	SkipAdmins        bool     `json:"skip_admins"`
	ParseMode         string   `json:"parse_mode,omitempty"`
	TriggerCount      int64    `json:"trigger_count,omitempty"`
	LastTriggeredAt   *string  `json:"last_triggered_at,omitempty"`
}

type AntiSpamPolicy struct {
	CASEnabled bool               `json:"cas_enabled"`
	RateLimit  AntiSpamRatePolicy `json:"rate_limit"`
}

type AntiSpamRatePolicy struct {
	Enabled        bool   `json:"enabled"`
	MessagesPer10s int    `json:"messages_per_10s"`
	Action         string `json:"action"`
}

type WarningsConfig struct {
	Enabled     bool   `json:"enabled"`
	MaxWarns    int    `json:"max_warns"`
	ActionAtMax string `json:"action_at_max"`
	DecayDays   int    `json:"decay_days"`
}

type LoggingConfig struct {
	LogChatID *int64 `json:"log_chat_id"`
}

type AIPolicy struct {
	Enabled                bool     `json:"enabled"`
	ImageModerationEnabled bool     `json:"image_moderation_enabled"`
	PrimaryProvider        string   `json:"primary_provider"`
	PrimaryModel           string   `json:"primary_model"`
	FallbackChain          []string `json:"fallback_chain"`
	PrimaryModelRef        string   `json:"primary_model_ref,omitempty"`
	FallbackModelRefs      []string `json:"fallback_model_refs,omitempty"`
	CapabilityRequirements []string `json:"capability_requirements,omitempty"`
	AutoDegrade            bool     `json:"auto_degrade,omitempty"`
	ProbeEnabled           bool     `json:"probe_enabled,omitempty"`
	ProbeIntervalSeconds   int      `json:"probe_interval_seconds,omitempty"`
	Temperature            float64  `json:"temperature"`
	TimeoutMs              int      `json:"timeout_ms"`
	MaxRetries             int      `json:"max_retries"`
	GraduateAfterMessages  int      `json:"graduate_after_messages"`
	// DEPRECATED: 不再用于毕业判定，仅为向后兼容保留
	GraduateAfterDays       int               `json:"graduate_after_days"`
	PerUserDailyLimit       int               `json:"per_user_daily_limit"`
	SkipMessagesShorterThan int               `json:"skip_messages_shorter_than"`
	BatchWindowMs           int               `json:"batch_window_ms"`
	CacheTTLHours           int               `json:"cache_ttl_hours"`
	CustomRules             string            `json:"custom_rules,omitempty"`
	MessageRules            string            `json:"message_rules"`
	BioRules                string            `json:"bio_rules"`
	Thresholds              AIThresholds      `json:"thresholds"`
	ActionsByCategory       map[string]string `json:"actions_by_category"`
	TriggerKeywords         []string          `json:"trigger_keywords"`
	// 未毕业用户（new/suspicious）发言前，针对其 bio 做一次审核
	CheckProfileOnMessage  bool   `json:"check_profile_on_message"`
	ProfileOnMessageMode   string `json:"profile_on_message_mode"` // "keyword" | "ai"
	BioCacheTTLMinutes     int    `json:"bio_cache_ttl_minutes"`   // 0 表示不缓存
	VideoModerationEnabled bool   `json:"video_moderation_enabled"`
	VideoMaxBytes          int64  `json:"video_max_bytes"`
	VideoMaxDurationSec    int    `json:"video_max_duration_sec"`
	VideoFrameCount        int    `json:"video_frame_count"`
	VideoConcurrency       int    `json:"video_concurrency"`
	IncludeVideoNote       bool   `json:"include_video_note"`
}

type AIThresholds struct {
	Ban  float64 `json:"ban"`
	Mute float64 `json:"mute"`
	Warn float64 `json:"warn"`
	Flag float64 `json:"flag"`
}

var DefaultPolicy = GuardPolicy{
	Verify: VerifyPolicy{
		Enabled:           true,
		Method:            "button",
		TimeoutSeconds:    300,
		FailAction:        "kick",
		DeleteJoinMessage: true,
		WelcomeMessage: WelcomeMessageConfig{
			Enabled:            true,
			Template:           stringPtr("🎉 欢迎 {user_mention} 加入 {group_title}！\n\n👋 看一下群规，交流愉快。\n\n🔗 群规：{rules_link}\n📱 有问题私聊管理员：{admin_list}"),
			DeleteAfterSeconds: 300,
		},
		CheckProfile:     true,
		ProfileCheckMode: "keyword",
		ProfileBlacklist: []string{
			"加我私聊",
			"加微信",
			"加vx",
			"刷单",
			"代刷",
			"币圈",
		},
	},
	Filter: FilterConfig{
		Keywords: FilterKeywordPolicy{
			Enabled: true,
			Action:  "delete_warn",
		},
		Regex: FilterRegexPolicy{
			Enabled: false,
		},
		Links: FilterLinksPolicy{
			Enabled:      true,
			Whitelist:    []string{"t.me", "telegram.me", "telegram.org"},
			Action:       "delete_warn",
			ExemptAdmins: true,
		},
		Usernames: FilterUsernamePolicy{
			Enabled: false,
		},
		NewUser: FilterNewUserPolicy{
			Enabled:              true,
			DurationHours:        24,
			NoLinks:              true,
			NoForwards:           true,
			MaxMessagesPerMinute: 5,
		},
		NonTextMessages:          "ai_review",
		OtherBotsAction:          "audit",
		BotWhitelist:             []string{},
		BanBotInviterOnViolation: false,
		BanSenderChats:           false,
	},
	Messages: MessagesPolicy{
		KeywordReplies: []KeywordReplyRule{},
	},
	AntiSpam: AntiSpamPolicy{
		CASEnabled: true,
		RateLimit: AntiSpamRatePolicy{
			Enabled:        true,
			MessagesPer10s: 8,
			Action:         "mute_5m",
		},
	},
	Warnings: WarningsConfig{
		Enabled:     true,
		MaxWarns:    3,
		ActionAtMax: "kick",
		DecayDays:   30,
	},
	Logging: LoggingConfig{
		LogChatID: nil,
	},
	AI: AIPolicy{
		Enabled:                 false,
		ImageModerationEnabled:  false,
		PrimaryProvider:         "newapi",
		PrimaryModel:            "glm-5",
		FallbackChain:           []string{"newapi/minimax-m2.5", "newapi/claude-sonnet-4-6"},
		CapabilityRequirements:  []string{"moderation"},
		ProbeIntervalSeconds:    60,
		Temperature:             0,
		TimeoutMs:               10000,
		MaxRetries:              2,
		GraduateAfterMessages:   5,
		GraduateAfterDays:       7,
		PerUserDailyLimit:       50,
		SkipMessagesShorterThan: 5,
		BatchWindowMs:           500,
		CacheTTLHours:           24,
		Thresholds: AIThresholds{
			Ban:  0.9,
			Mute: 0.75,
			Warn: 0.5,
			Flag: 0.3,
		},
		CheckProfileOnMessage:  false,
		ProfileOnMessageMode:   "ai",
		BioCacheTTLMinutes:     10,
		VideoModerationEnabled: false,
		VideoMaxBytes:          20 * 1024 * 1024,
		VideoMaxDurationSec:    300,
		VideoFrameCount:        3,
		VideoConcurrency:       2,
		IncludeVideoNote:       false,
		ActionsByCategory: map[string]string{
			"招聘": "warn",
			"交友": "mute",
			"币圈": "ban",
			"刷单": "ban",
			"引流": "mute",
			"政治": "delete",
			"色情": "ban",
			"正常": "none",
		},
	},
	Feedback: ActionFeedbackPolicy{
		DeleteMsg: ActionFeedback{
			Enabled:           false,
			Template:          "🧹 {user} 的消息被清扫员扫进垃圾桶了（{reason}）",
			AutoDeleteSeconds: 15,
			ReplyToMessage:    false,
		},
		Mute: ActionFeedback{
			Enabled:           true,
			Template:          "🤐 {user} 被贴了 {duration} 的封口胶。冷静一下，思考人生（{reason}）",
			AutoDeleteSeconds: 60,
			ReplyToMessage:    false,
		},
		Kick: ActionFeedback{
			Enabled:           true,
			Template:          "👢 {user} 被一脚踹出门外。下次再来请文明发言（{reason}）",
			AutoDeleteSeconds: 60,
			ReplyToMessage:    false,
		},
		Ban: ActionFeedback{
			Enabled:           true,
			Template:          "🚓 {user} 被正义执法，带走调查。理由：{reason}",
			AutoDeleteSeconds: 60,
			ReplyToMessage:    false,
		},
		Warn: ActionFeedback{
			Enabled:           true,
			Template:          "⚠️ 警告一下 {user}，累计 {current}/{limit}。再犯就请你吃手铐了（{reason}）",
			AutoDeleteSeconds: 60,
			ReplyToMessage:    true,
		},
		VerifyPass: ActionFeedback{
			Enabled:           false,
			Template:          "✅ {user} 通过安检，欢迎入群！麻烦看下群规，别让我下次在违规榜上见到你",
			AutoDeleteSeconds: 15,
			ReplyToMessage:    false,
		},
		VerifyFail: ActionFeedback{
			Enabled:           true,
			Template:          "⌛ {user} 验证未通过（{reason}），已被礼送出境。真人欢迎重新申请加群",
			AutoDeleteSeconds: 60,
			ReplyToMessage:    false,
		},
		CASHit: ActionFeedback{
			Enabled:           true,
			Template:          "🚫 {user} 已被 CAS 全网通缉，直接押送离场",
			AutoDeleteSeconds: 60,
			ReplyToMessage:    false,
		},
		TrustGraduated: ActionFeedback{
			Enabled:           false,
			Template:          "🎓 恭喜 {user} 通过见习期，正式成为群里的老油条",
			AutoDeleteSeconds: 30,
			ReplyToMessage:    false,
		},
		AdminAction: ActionFeedback{
			Enabled:           true,
			Template:          "👮 {admin} 对 {user} 执行了「{action}」。理由：{reason}",
			AutoDeleteSeconds: 0,
			ReplyToMessage:    false,
		},
	},
}

func LoadPolicy(ctx context.Context, queries *store.Queries, chatID int64) (GuardPolicy, error) {
	merged := map[string]any{}

	if err := mergeJSONBytesIntoMap(merged, mustJSON(DefaultPolicy)); err != nil {
		return GuardPolicy{}, err
	}

	globalConfig, err := queries.GetGlobalConfig(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return GuardPolicy{}, fmt.Errorf("get global config: %w", err)
	}
	if err == nil {
		if err := mergeJSONBytesIntoMap(merged, globalConfig.Config); err != nil {
			return GuardPolicy{}, fmt.Errorf("merge global config: %w", err)
		}
	}

	group, err := queries.GetGroupByChatID(ctx, chatID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return GuardPolicy{}, fmt.Errorf("get group config: %w", err)
	}
	if err == nil {
		if err := mergeJSONBytesIntoMap(merged, group.Config); err != nil {
			return GuardPolicy{}, fmt.Errorf("merge group config: %w", err)
		}
	}

	raw, err := json.Marshal(merged)
	if err != nil {
		return GuardPolicy{}, fmt.Errorf("marshal merged policy: %w", err)
	}

	policy := DefaultPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return GuardPolicy{}, fmt.Errorf("decode merged policy: %w", err)
	}

	applyVerifyDefaults(&policy.Verify)
	applyFilterDefaults(&policy.Filter)
	applyMessagesDefaults(&policy.Messages)
	applyWarningsDefaults(&policy.Warnings)
	applyAIDefaults(&policy.AI)
	return policy, nil
}

func applyVerifyDefaults(policy *VerifyPolicy) {
	if policy == nil {
		return
	}
	policy.Method = strings.TrimSpace(policy.Method)
	if policy.Method == "" {
		policy.Method = DefaultPolicy.Verify.Method
	}
	if policy.TimeoutSeconds <= 0 {
		policy.TimeoutSeconds = DefaultPolicy.Verify.TimeoutSeconds
	}
	if policy.FailAction == "" {
		policy.FailAction = DefaultPolicy.Verify.FailAction
	}
	applyWelcomeDefaults(&policy.WelcomeMessage)
	// ProfileCheckMode fallback to keyword
	policy.ProfileCheckMode = strings.TrimSpace(policy.ProfileCheckMode)
	if policy.ProfileCheckMode == "" {
		policy.ProfileCheckMode = DefaultPolicy.Verify.ProfileCheckMode
	}
	if len(policy.ProfileBlacklist) == 0 {
		policy.ProfileBlacklist = append([]string(nil), DefaultPolicy.Verify.ProfileBlacklist...)
	}
}

func applyWelcomeDefaults(policy *WelcomeMessageConfig) {
	if policy == nil {
		return
	}
	if policy.Template == nil || strings.TrimSpace(*policy.Template) == "" {
		policy.Template = DefaultPolicy.Verify.WelcomeMessage.Template
	}
	if policy.DeleteAfterSeconds <= 0 {
		policy.DeleteAfterSeconds = DefaultPolicy.Verify.WelcomeMessage.DeleteAfterSeconds
	}
}

func applyFilterDefaults(policy *FilterConfig) {
	if policy == nil {
		return
	}
	if strings.TrimSpace(policy.NonTextMessages) == "" {
		policy.NonTextMessages = DefaultPolicy.Filter.NonTextMessages
	}
	policy.OtherBotsAction = strings.TrimSpace(strings.ToLower(policy.OtherBotsAction))
	switch policy.OtherBotsAction {
	case "off", "audit", "kick", "ban":
	default:
		policy.OtherBotsAction = DefaultPolicy.Filter.OtherBotsAction
	}
	policy.BotWhitelist = normalizeBotWhitelist(policy.BotWhitelist)
	if policy.Keywords.Action == "" {
		policy.Keywords.Action = DefaultPolicy.Filter.Keywords.Action
	}
	if policy.Links.Action == "" {
		policy.Links.Action = DefaultPolicy.Filter.Links.Action
	}
	if len(policy.Links.Whitelist) == 0 {
		policy.Links.Whitelist = append([]string(nil), DefaultPolicy.Filter.Links.Whitelist...)
	}
	if policy.NewUser.DurationHours <= 0 {
		policy.NewUser.DurationHours = DefaultPolicy.Filter.NewUser.DurationHours
	}
	if policy.NewUser.MaxMessagesPerMinute <= 0 {
		policy.NewUser.MaxMessagesPerMinute = DefaultPolicy.Filter.NewUser.MaxMessagesPerMinute
	}
}

func normalizeBotWhitelist(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		username := strings.TrimPrefix(strings.TrimSpace(strings.ToLower(value)), "@")
		if username == "" {
			continue
		}
		if _, ok := seen[username]; ok {
			continue
		}
		seen[username] = struct{}{}
		out = append(out, username)
	}
	return out
}

func applyMessagesDefaults(policy *MessagesPolicy) {
	if policy == nil {
		return
	}
	if policy.KeywordReplies == nil {
		policy.KeywordReplies = []KeywordReplyRule{}
	}
}

func applyWarningsDefaults(policy *WarningsConfig) {
	if policy == nil {
		return
	}
	if policy.MaxWarns <= 0 {
		policy.MaxWarns = DefaultPolicy.Warnings.MaxWarns
	}
	if policy.ActionAtMax == "" {
		policy.ActionAtMax = DefaultPolicy.Warnings.ActionAtMax
	}
	if policy.DecayDays < 0 {
		policy.DecayDays = DefaultPolicy.Warnings.DecayDays
	}
}

func applyAIDefaults(policy *AIPolicy) {
	if policy == nil {
		return
	}
	if strings.TrimSpace(policy.PrimaryProvider) == "" {
		policy.PrimaryProvider = DefaultPolicy.AI.PrimaryProvider
	}
	if strings.TrimSpace(policy.PrimaryModel) == "" {
		policy.PrimaryModel = DefaultPolicy.AI.PrimaryModel
	}
	if len(policy.FallbackChain) == 0 {
		policy.FallbackChain = append([]string(nil), DefaultPolicy.AI.FallbackChain...)
	}
	if len(policy.CapabilityRequirements) == 0 {
		policy.CapabilityRequirements = append([]string(nil), DefaultPolicy.AI.CapabilityRequirements...)
	}
	if policy.ProbeIntervalSeconds <= 0 {
		policy.ProbeIntervalSeconds = DefaultPolicy.AI.ProbeIntervalSeconds
	}
	if policy.TimeoutMs <= 0 {
		policy.TimeoutMs = DefaultPolicy.AI.TimeoutMs
	}
	if policy.MaxRetries < 0 {
		policy.MaxRetries = DefaultPolicy.AI.MaxRetries
	}
	if policy.GraduateAfterMessages <= 0 {
		policy.GraduateAfterMessages = DefaultPolicy.AI.GraduateAfterMessages
	}
	if policy.GraduateAfterDays <= 0 {
		policy.GraduateAfterDays = DefaultPolicy.AI.GraduateAfterDays
	}
	if policy.PerUserDailyLimit <= 0 {
		policy.PerUserDailyLimit = DefaultPolicy.AI.PerUserDailyLimit
	}
	if policy.SkipMessagesShorterThan <= 0 {
		policy.SkipMessagesShorterThan = DefaultPolicy.AI.SkipMessagesShorterThan
	}
	if policy.BatchWindowMs <= 0 {
		policy.BatchWindowMs = DefaultPolicy.AI.BatchWindowMs
	}
	if policy.CacheTTLHours <= 0 {
		policy.CacheTTLHours = DefaultPolicy.AI.CacheTTLHours
	}
	if policy.VideoMaxBytes <= 0 {
		policy.VideoMaxBytes = DefaultPolicy.AI.VideoMaxBytes
	}
	if policy.VideoMaxDurationSec <= 0 {
		policy.VideoMaxDurationSec = DefaultPolicy.AI.VideoMaxDurationSec
	}
	if policy.VideoFrameCount <= 0 {
		policy.VideoFrameCount = DefaultPolicy.AI.VideoFrameCount
	}
	if policy.VideoConcurrency <= 0 {
		policy.VideoConcurrency = DefaultPolicy.AI.VideoConcurrency
	}
	applyAIThresholdDefaults(&policy.Thresholds)
	if len(policy.ActionsByCategory) == 0 {
		policy.ActionsByCategory = cloneStringMap(DefaultPolicy.AI.ActionsByCategory)
	}
	if strings.TrimSpace(policy.MessageRules) == "" && strings.TrimSpace(policy.CustomRules) != "" {
		policy.MessageRules = policy.CustomRules
	}
	if strings.TrimSpace(policy.BioRules) == "" && strings.TrimSpace(policy.CustomRules) != "" {
		policy.BioRules = policy.CustomRules
	}
}

func applyAIThresholdDefaults(policy *AIThresholds) {
	if policy == nil {
		return
	}
	if policy.Ban <= 0 {
		policy.Ban = DefaultPolicy.AI.Thresholds.Ban
	}
	if policy.Mute <= 0 {
		policy.Mute = DefaultPolicy.AI.Thresholds.Mute
	}
	if policy.Warn <= 0 {
		policy.Warn = DefaultPolicy.AI.Thresholds.Warn
	}
	if policy.Flag <= 0 {
		policy.Flag = DefaultPolicy.AI.Thresholds.Flag
	}
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func stringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func mergeJSONBytesIntoMap(dst map[string]any, raw []byte) error {
	if len(raw) == 0 {
		return nil
	}

	var src map[string]any
	if err := json.Unmarshal(raw, &src); err != nil {
		return err
	}

	deepMerge(dst, src)
	return nil
}

func deepMerge(dst, src map[string]any) {
	for key, value := range src {
		if value == nil {
			continue
		}

		srcMap, srcIsMap := value.(map[string]any)
		dstMap, dstIsMap := dst[key].(map[string]any)
		if srcIsMap && dstIsMap {
			deepMerge(dstMap, srcMap)
			continue
		}

		dst[key] = value
	}
}
