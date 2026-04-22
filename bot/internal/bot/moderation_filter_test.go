package bot

import (
	"context"
	"testing"

	"github.com/openclaw/clawguard/internal/config"
	tele "gopkg.in/telebot.v3"
)

func TestApplyFilterChecksHandledFlag(t *testing.T) {
	svc := &Service{}
	msg := &tele.Message{
		Text:   "clean message",
		Chat:   &tele.Chat{ID: -100},
		Sender: &tele.User{ID: 123},
	}

	policy := config.DefaultPolicy
	policy.Filter.Keywords.Enabled = true
	policy.Filter.Keywords.List = []string{"blocked"}

	handled, err := svc.applyFilterChecks(context.Background(), msg, policy)
	if err != nil {
		t.Fatalf("unexpected error on non-hit filter: %v", err)
	}
	if handled {
		t.Fatalf("handled = true, want false when filter does not hit")
	}
}

func TestCheckMessageRegexHit(t *testing.T) {
	msg := &tele.Message{
		Text:   "buy cheap crypto now",
		Sender: &tele.User{ID: 1},
	}
	policy := config.DefaultPolicy.Filter
	policy.Regex.Enabled = true
	policy.Regex.Patterns = []string{`cheap\s+crypto`}

	result := checkMessage(context.Background(), msg, policy)
	if !result.Hit {
		t.Fatalf("expected regex hit")
	}
	if result.Reason != "filter_regex" {
		t.Fatalf("reason = %q, want filter_regex", result.Reason)
	}
	if result.MatchedRule != `cheap\s+crypto` {
		t.Fatalf("matched rule = %q", result.MatchedRule)
	}
}

func TestCheckMessageUsernameBlacklistHit(t *testing.T) {
	msg := &tele.Message{
		Text:   "hello",
		Sender: &tele.User{ID: 1, Username: "SpamAccount"},
	}
	policy := config.DefaultPolicy.Filter
	policy.Usernames.Enabled = true
	policy.Usernames.Blacklist = []string{"@spamaccount"}

	result := checkMessage(context.Background(), msg, policy)
	if !result.Hit {
		t.Fatalf("expected username blacklist hit")
	}
	if result.Reason != "filter_username" {
		t.Fatalf("reason = %q, want filter_username", result.Reason)
	}
}

func TestNormalizeBioAICategory(t *testing.T) {
	tests := []struct {
		name     string
		verdict  string
		category string
		want     string
	}{
		{name: "empty category falls back", verdict: "scam", category: "", want: "scam"},
		{name: "normal chinese falls back", verdict: "scam", category: "正常", want: "scam"},
		{name: "normal english falls back case insensitive", verdict: "ad", category: " Normal ", want: "ad"},
		{name: "safe synonym falls back", verdict: "spam", category: "SAFE", want: "spam"},
		{name: "none synonym falls back", verdict: "harass", category: " none ", want: "harass"},
		{name: "real category preserved", verdict: "scam", category: "博彩诈骗", want: "博彩诈骗"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeBioAICategory(tt.verdict, tt.category)
			if got != tt.want {
				t.Fatalf("normalizeBioAICategory(%q, %q) = %q, want %q", tt.verdict, tt.category, got, tt.want)
			}
		})
	}
}
