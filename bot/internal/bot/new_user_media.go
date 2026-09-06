package bot

func shouldContinueReviewAfterNewUserMediaDelete(kind string) bool {
	switch kind {
	case "contact", "poll", "venue", "invoice", "game", "document":
		return true
	default:
		return false
	}
}
