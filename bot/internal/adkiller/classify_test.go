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
