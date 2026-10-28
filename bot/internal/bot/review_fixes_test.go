package bot

import (
	tele "gopkg.in/telebot.v3"
	"testing"
	"time"
)

func TestExemptCommandDurationGrammar(t *testing.T) {
	for _, tc := range []struct {
		text         string
		fail, expiry bool
		note         string
	}{
		{"/exempt 123", false, false, ""},
		{"/exempt 123 +1H", false, true, ""},
		{"/exempt 123 1d temp", false, true, "temp"},
		{"/exempt 123 temp 1d", true, false, ""},
		{"/exempt 123 --note temp 1d", false, false, "temp 1d"},
		{"/exempt 123 2025 plan", false, false, "2025 plan"},
		{"/exempt 123 3x", true, false, ""},
		{"/exempt 123 -1h", true, false, ""},
		{"/exempt 123 0d", true, false, ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			id, at, note, err := parseExemptCommand(&tele.Message{Text: tc.text}, false)
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v", err)
			}
			if err == nil && (id != 123 || (at != nil) != tc.expiry || note != tc.note) {
				t.Fatalf("id=%d at=%v note=%q", id, at, note)
			}
			if at != nil && !at.After(time.Now()) {
				t.Fatal("expiry not future")
			}
		})
	}
}
