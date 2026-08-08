package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/openclaw/clawguard/internal/messagefmt"
)

var (
	filterActions = stringSet(
		"delete",
		"delete_warn",
		"delete_and_warn",
		"delete_mute",
		"delete_ban",
		"warn",
		"mute",
		"mute_5m",
		"mute_1h",
	)
	warningActions = stringSet("mute", "mute_5m", "mute_1h", "kick", "ban")
	aiActions      = stringSet(
		"none",
		"flag",
		"delete",
		"warn",
		"mute",
		"ban",
		"delete_warn",
		"delete_and_warn",
		"delete_mute",
		"delete_ban",
		"mute_5m",
		"mute_1h",
	)
	parseModes         = stringSet("", "html", "markdown", "markdownv2", "md", "mdv2")
	legacyPolicyFields = stringSet(
		"ai.daily_budget_cents",
		"ai.exempt_admins",
		"feedback.ai_flagged",
	)
)

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func normalizedPolicyValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// NormalizeFilterAction maps legacy aliases to the action name stored in
// violation records while retaining duration-specific mute actions.
func NormalizeFilterAction(action string) string {
	action = normalizedPolicyValue(action)
	if action == "delete_and_warn" {
		return "delete_warn"
	}
	return action
}

func IsFilterAction(action string) bool {
	_, ok := filterActions[normalizedPolicyValue(action)]
	return ok
}

// ValidatePolicyDocument rejects unknown JSON keys and malformed field types.
// Documents may be partial because global and group configs are overlays.
func ValidatePolicyDocument(raw []byte) error {
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("invalid policy json: %w", err)
	}
	if _, ok := document.(map[string]any); !ok {
		return fmt.Errorf("policy document must be an object")
	}
	if err := validateKnownJSONFields(document, reflect.TypeOf(GuardPolicy{}), ""); err != nil {
		return err
	}

	var decoded GuardPolicy
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("invalid policy field type: %w", err)
	}
	return validatePartialPolicy(decoded)
}

func validateKnownJSONFields(value any, typ reflect.Type, path string) error {
	if value == nil {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	switch typ.Kind() {
	case reflect.Struct:
		if typ == reflect.TypeOf(WelcomeMessageConfig{}) {
			if _, legacy := value.(string); legacy {
				return nil
			}
		}
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", policyPath(path))
		}
		fields := make(map[string]reflect.Type, typ.NumField())
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			fields[name] = field.Type
		}
		for name, child := range object {
			fieldType, known := fields[name]
			childPath := name
			if path != "" {
				childPath = path + "." + name
			}
			if !known {
				if _, legacy := legacyPolicyFields[childPath]; legacy {
					continue
				}
				return fmt.Errorf("unknown policy field %s", childPath)
			}
			if err := validateKnownJSONFields(child, fieldType, childPath); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", policyPath(path))
		}
		for index, item := range items {
			if err := validateKnownJSONFields(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", policyPath(path))
		}
		for name, child := range object {
			if err := validateKnownJSONFields(child, typ.Elem(), path+"."+name); err != nil {
				return err
			}
		}
	}
	return nil
}

func policyPath(path string) string {
	if path == "" {
		return "policy"
	}
	return path
}

func validatePartialPolicy(policy GuardPolicy) error {
	if policy.Verify.TimeoutSeconds < 0 {
		return fmt.Errorf("verify.timeout_seconds must not be negative")
	}
	if policy.Verify.WelcomeMessage.DeleteAfterSeconds < 0 {
		return fmt.Errorf("verify.welcome_message.delete_after_seconds must not be negative")
	}
	if policy.Filter.NewUser.DurationHours < 0 {
		return fmt.Errorf("filter.new_user.duration_hours must not be negative")
	}
	if policy.Filter.NewUser.MaxMessagesPerMinute < 0 {
		return fmt.Errorf("filter.new_user.max_messages_per_minute must not be negative")
	}
	if policy.AntiSpam.RateLimit.MessagesPer10s < 0 {
		return fmt.Errorf("anti_spam.rate_limit.messages_per_10s must not be negative")
	}
	if policy.Warnings.MaxWarns < 0 || policy.Warnings.DecayDays < 0 {
		return fmt.Errorf("warning limits must not be negative")
	}
	thresholds := policy.AI.Thresholds
	if !validProbability(thresholds.Ban) || !validProbability(thresholds.Mute) || !validProbability(thresholds.Warn) || !validProbability(thresholds.Flag) {
		return fmt.Errorf("ai.thresholds values must be between 0 and 1")
	}
	return nil
}

// ValidateGuardPolicy validates the fully merged, effective policy. Invalid
// action names are rejected instead of silently falling back at runtime.
func ValidateGuardPolicy(policy GuardPolicy) error {
	if err := ValidateJoinProtectionPolicy(policy.JoinProtection); err != nil {
		return fmt.Errorf("join_protection: %w", err)
	}
	if !oneOf(policy.Verify.Method, "button", "math", "math_image", "random", "turnstile") {
		return fmt.Errorf("verify.method has unsupported value %q", policy.Verify.Method)
	}
	if !oneOf(policy.Verify.FailAction, "kick", "ban", "mute_permanent") {
		return fmt.Errorf("verify.fail_action has unsupported value %q", policy.Verify.FailAction)
	}
	if !oneOf(policy.Verify.ProfileCheckMode, "keyword", "ai", "off") {
		return fmt.Errorf("verify.profile_check_mode has unsupported value %q", policy.Verify.ProfileCheckMode)
	}
	if policy.Verify.TimeoutSeconds <= 0 {
		return fmt.Errorf("verify.timeout_seconds must be positive")
	}
	if policy.Verify.WelcomeMessage.DeleteAfterSeconds < 0 {
		return fmt.Errorf("verify.welcome_message.delete_after_seconds must not be negative")
	}

	if !IsFilterAction(policy.Filter.Keywords.Action) {
		return fmt.Errorf("filter.keywords.action has unsupported value %q", policy.Filter.Keywords.Action)
	}
	if !IsFilterAction(policy.Filter.Links.Action) {
		return fmt.Errorf("filter.links.action has unsupported value %q", policy.Filter.Links.Action)
	}
	if !oneOf(policy.Filter.NonTextMessages, "off", "delete", "delete_warn", "ai_review") {
		return fmt.Errorf("filter.non_text_messages has unsupported value %q", policy.Filter.NonTextMessages)
	}
	if !oneOf(policy.Filter.OtherBotsAction, "off", "audit", "kick", "ban") {
		return fmt.Errorf("filter.other_bots_action has unsupported value %q", policy.Filter.OtherBotsAction)
	}
	if policy.Filter.NewUser.MaxMessagesPerMinute < 0 {
		return fmt.Errorf("filter.new_user.max_messages_per_minute must not be negative")
	}
	for index, pattern := range policy.Filter.Regex.Patterns {
		if strings.TrimSpace(pattern) == "" {
			continue
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("filter.regex.patterns[%d] is invalid: %w", index, err)
		}
	}

	if policy.AntiSpam.RateLimit.MessagesPer10s < 0 {
		return fmt.Errorf("anti_spam.rate_limit.messages_per_10s must not be negative")
	}
	if !IsFilterAction(policy.AntiSpam.RateLimit.Action) {
		return fmt.Errorf("anti_spam.rate_limit.action has unsupported value %q", policy.AntiSpam.RateLimit.Action)
	}
	if policy.Warnings.MaxWarns <= 0 {
		return fmt.Errorf("warnings.max_warns must be positive")
	}
	if policy.Warnings.DecayDays < 0 {
		return fmt.Errorf("warnings.decay_days must not be negative")
	}
	if _, ok := warningActions[normalizedPolicyValue(policy.Warnings.ActionAtMax)]; !ok {
		return fmt.Errorf("warnings.action_at_max has unsupported value %q", policy.Warnings.ActionAtMax)
	}

	for index, rule := range policy.Messages.KeywordReplies {
		if !oneOfDefault(rule.MatchType, "fuzzy", "exact", "regex") {
			return fmt.Errorf("messages.keyword_replies[%d].match_type has unsupported value %q", index, rule.MatchType)
		}
		if rule.AutoDeleteSeconds < 0 || rule.CooldownSeconds < 0 {
			return fmt.Errorf("messages.keyword_replies[%d] timing values must not be negative", index)
		}
		if normalizedPolicyValue(rule.MatchType) == "regex" {
			for keywordIndex, pattern := range rule.Keywords {
				if _, err := regexp.Compile(pattern); err != nil {
					return fmt.Errorf("messages.keyword_replies[%d].keywords[%d] is invalid: %w", index, keywordIndex, err)
				}
			}
		}
		if err := validateParseMode(fmt.Sprintf("messages.keyword_replies[%d].parse_mode", index), rule.ParseMode); err != nil {
			return err
		}
		// Reject templates Telegram would refuse to parse. Without this a single
		// bad rule keeps failing at send time, and because the send happens after
		// the cooldown lock is taken it silently mutes the rule for a full
		// cooldown window on every trigger.
		if err := messagefmt.ValidateTemplate(rule.ReplyText, rule.ParseMode); err != nil {
			return fmt.Errorf("messages.keyword_replies[%d].reply_text is not valid for parse_mode %q: %w", index, messagefmt.ResolveParseMode(rule.ParseMode), err)
		}
	}

	if !oneOf(policy.AI.ProfileOnMessageMode, "keyword", "ai") {
		return fmt.Errorf("ai.profile_on_message_mode has unsupported value %q", policy.AI.ProfileOnMessageMode)
	}
	if policy.AI.BioCacheTTLMinutes < 0 || policy.AI.MaxRetries < 0 {
		return fmt.Errorf("ai cache and retry values must not be negative")
	}
	thresholds := policy.AI.Thresholds
	if !validProbability(thresholds.Ban) || !validProbability(thresholds.Mute) || !validProbability(thresholds.Warn) || !validProbability(thresholds.Flag) {
		return fmt.Errorf("ai.thresholds values must be between 0 and 1")
	}
	if thresholds.Ban < thresholds.Mute || thresholds.Mute < thresholds.Warn || thresholds.Warn < thresholds.Flag {
		return fmt.Errorf("ai.thresholds must satisfy ban >= mute >= warn >= flag")
	}
	for category, action := range policy.AI.ActionsByCategory {
		if _, ok := aiActions[normalizedPolicyValue(action)]; !ok {
			return fmt.Errorf("ai.actions_by_category.%s has unsupported value %q", category, action)
		}
	}

	if err := validateParseMode("verify.welcome_message.parse_mode", policy.Verify.WelcomeMessage.ParseMode); err != nil {
		return err
	}
	feedback := []struct {
		name  string
		value ActionFeedback
	}{
		{"feedback.delete_msg", policy.Feedback.DeleteMsg},
		{"feedback.mute", policy.Feedback.Mute},
		{"feedback.kick", policy.Feedback.Kick},
		{"feedback.ban", policy.Feedback.Ban},
		{"feedback.warn", policy.Feedback.Warn},
		{"feedback.verify_pass", policy.Feedback.VerifyPass},
		{"feedback.verify_fail", policy.Feedback.VerifyFail},
		{"feedback.cas_hit", policy.Feedback.CASHit},
		{"feedback.trust_graduated", policy.Feedback.TrustGraduated},
		{"feedback.admin_action", policy.Feedback.AdminAction},
	}
	for _, item := range feedback {
		if item.value.AutoDeleteSeconds < 0 {
			return fmt.Errorf("%s.auto_delete_seconds must not be negative", item.name)
		}
		if err := validateParseMode(item.name+".parse_mode", item.value.ParseMode); err != nil {
			return err
		}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	value = normalizedPolicyValue(value)
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func oneOfDefault(value string, allowed ...string) bool {
	return strings.TrimSpace(value) == "" || oneOf(value, allowed...)
}

func validProbability(value float64) bool {
	return value >= 0 && value <= 1
}

func validateParseMode(path, value string) error {
	if _, ok := parseModes[normalizedPolicyValue(value)]; !ok {
		return fmt.Errorf("%s has unsupported value %q", path, value)
	}
	return nil
}
