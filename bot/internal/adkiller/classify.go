package adkiller

import "strings"

const (
	OnFailureFallback = "fallback"
	OnFailureSkip     = "skip"

	ActionNone   = "none"
	ActionWarn   = "warn"
	ActionMute   = "mute"
	ActionKick   = "kick"
	ActionBan    = "ban"
	ActionDelete = "delete"
)

func ConfirmedAd(score, minScore int, level string) bool {
	if minScore <= 0 {
		minScore = 81
	}
	_ = level
	return score >= minScore
}

func NormalizeAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case ActionWarn, "delete_warn", "delete_and_warn":
		return ActionWarn
	case ActionMute, "mute_5m", "mute_1h", "delete_mute":
		return ActionMute
	case ActionKick:
		return ActionKick
	case ActionBan, "delete_ban":
		return ActionBan
	case ActionDelete:
		return ActionDelete
	default:
		return ActionNone
	}
}

type ScoreBand struct {
	MinScore int
	MaxScore int
	Action   string
}

func ResolveAction(score int, bands []ScoreBand) string {
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	matched := ActionNone
	matchedMin := -1
	for _, band := range bands {
		minScore := band.MinScore
		maxScore := band.MaxScore
		if maxScore <= 0 {
			maxScore = 100
		}
		if minScore < 0 {
			minScore = 0
		}
		if score < minScore || score > maxScore {
			continue
		}
		if matchedMin < 0 || minScore >= matchedMin {
			matchedMin = minScore
			matched = NormalizeAction(band.Action)
		}
	}
	return matched
}

func LowestActionScore(bands []ScoreBand) int {
	lowest := 0
	found := false
	for _, band := range bands {
		if NormalizeAction(band.Action) == ActionNone {
			continue
		}
		minScore := band.MinScore
		if minScore < 0 {
			minScore = 0
		}
		if !found || minScore < lowest {
			lowest = minScore
			found = true
		}
	}
	if !found {
		return 81
	}
	return lowest
}

func ChatEnabled(chatIDs []int64, chatID int64) bool {
	for _, id := range chatIDs {
		if id == chatID {
			return true
		}
	}
	return false
}

func ActionLabel(action string) string {
	switch NormalizeAction(action) {
	case ActionWarn:
		return "警告"
	case ActionMute:
		return "禁言"
	case ActionKick:
		return "踢出"
	case ActionBan:
		return "封禁"
	case ActionDelete:
		return "删除消息"
	default:
		return "交给后续模型"
	}
}

// MapCategory converts AdKiller's English primary_category into the Chinese
// labels already used by ai.actions_by_category. Unknown values stay as-is so
// the verdict-level "ad" mapping still applies.
func MapCategory(primary string) string {
	switch strings.ToLower(strings.TrimSpace(primary)) {
	case "recruitment_tasks":
		return "招聘"
	case "group_channel_promotion", "followers_traffic_operations", "private_contact_diversion", "other_promotion", "service_promotion":
		return "引流"
	case "crypto_exchange_cashout", "investment_finance_loan":
		return "币圈"
	case "sexual_service":
		return "色情"
	case "gambling_betting", "counterfeit_smuggling_prohibited", "money_mule_payment_channel", "account_identity_sim", "personal_data_tracking":
		return "引流"
	case "goods_sale":
		return "引流"
	case "normal_chat":
		return "正常"
	default:
		if primary == "" {
			return "引流"
		}
		return primary
	}
}

func Confidence(score int) float64 {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 1
	}
	return float64(score) / 100
}
