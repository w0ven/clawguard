package adkiller

import "strings"

const (
	OnFailureFallback = "fallback"
	OnFailureSkip     = "skip"
)

func ConfirmedAd(score, minScore int, level string) bool {
	if minScore <= 0 {
		minScore = 81
	}
	_ = level
	return score >= minScore
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
