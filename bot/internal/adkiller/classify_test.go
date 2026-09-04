package adkiller

import "testing"

func TestConfirmedAd(t *testing.T) {
	if !ConfirmedAd(82, 81, "ad") {
		t.Fatal("expected confirmed ad")
	}
	if ConfirmedAd(80, 81, "likely") {
		t.Fatal("score below threshold should not confirm")
	}
	if ConfirmedAd(50, 81, "ad") {
		t.Fatal("level=ad below min_score should not confirm")
	}
}

func TestMapCategory(t *testing.T) {
	if got := MapCategory("recruitment_tasks"); got != "招聘" {
		t.Fatalf("got %q", got)
	}
	if got := MapCategory("goods_sale"); got != "引流" {
		t.Fatalf("got %q", got)
	}
	if got := MapCategory("crypto_exchange_cashout"); got != "币圈" {
		t.Fatalf("got %q", got)
	}
	if got := MapCategory(""); got != "引流" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveAction(t *testing.T) {
	bands := []ScoreBand{
		{MinScore: 0, MaxScore: 80, Action: "none"},
		{MinScore: 81, MaxScore: 90, Action: "warn"},
		{MinScore: 91, MaxScore: 100, Action: "kick"},
	}
	if got := ResolveAction(12, bands); got != ActionNone {
		t.Fatalf("score 12 got %q", got)
	}
	if got := ResolveAction(81, bands); got != ActionWarn {
		t.Fatalf("score 81 got %q", got)
	}
	if got := ResolveAction(90, bands); got != ActionWarn {
		t.Fatalf("score 90 got %q", got)
	}
	if got := ResolveAction(91, bands); got != ActionKick {
		t.Fatalf("score 91 got %q", got)
	}
	if got := ResolveAction(100, bands); got != ActionKick {
		t.Fatalf("score 100 got %q", got)
	}
}

func TestChatEnabled(t *testing.T) {
	if ChatEnabled(nil, -1001) {
		t.Fatal("empty list should be disabled")
	}
	if !ChatEnabled([]int64{-1001, -1002}, -1001) {
		t.Fatal("listed chat should be enabled")
	}
	if ChatEnabled([]int64{-1001}, -1003) {
		t.Fatal("unlisted chat should be disabled")
	}
}

func TestLowestActionScore(t *testing.T) {
	got := LowestActionScore([]ScoreBand{
		{MinScore: 0, MaxScore: 80, Action: "none"},
		{MinScore: 81, MaxScore: 90, Action: "warn"},
		{MinScore: 91, MaxScore: 100, Action: "kick"},
	})
	if got != 81 {
		t.Fatalf("got %d", got)
	}
}
