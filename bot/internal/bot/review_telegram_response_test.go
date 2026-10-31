package bot

import "testing"

func TestReviewTelegramRequiresConfirmedBoolean(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"ok":true,"result":true}`, true},
		{`{"ok":true,"result":false}`, false},
		{`{"ok":true}`, false},
		{`{"ok":false,"result":true}`, false},
		{`<html>bad gateway</html>`, false},
	} {
		if (confirmTelegramBoolean([]byte(tc.body)) == nil) != tc.ok {
			t.Fatalf("response %q", tc.body)
		}
	}
}
