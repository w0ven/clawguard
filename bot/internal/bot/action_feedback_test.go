package bot

import (
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestRenderFeedbackTemplateUserLabelAcrossParseModes(t *testing.T) {
	users := []struct {
		name string
		user *tele.User
	}{
		{
			name: "with username",
			user: &tele.User{ID: 42, Username: "my_name", FirstName: "可乐"},
		},
		{
			name: "without username",
			user: &tele.User{ID: 42, FirstName: "可乐"},
		},
	}

	cases := []struct {
		name              string
		parseMode         string
		wantUserLabel     string
		wantAdminLabel    string
		wantReasonSnippet string
	}{
		{
			name:              "markdownv2",
			parseMode:         "markdownv2",
			wantUserLabel:     "42",
			wantAdminLabel:    "99",
			wantReasonSnippet: "括号\\(需要转义\\)\\!",
		},
		{
			name:              "html",
			parseMode:         "html",
			wantUserLabel:     "42",
			wantAdminLabel:    "99",
			wantReasonSnippet: "括号(需要转义)!",
		},
		{
			name:              "markdown",
			parseMode:         "markdown",
			wantUserLabel:     "42",
			wantAdminLabel:    "99",
			wantReasonSnippet: "括号(需要转义)!",
		},
	}

	admin := &tele.User{ID: 99, Username: "admin_name", FirstName: "管理员"}

	for _, userCase := range users {
		for _, tc := range cases {
			t.Run(userCase.name+"/"+tc.name, func(t *testing.T) {
				rendered := renderFeedbackTemplate("✅ {user} <- {admin} / {reason}", map[string]string{
					"user":   feedbackUserLabel(userCase.user, tc.parseMode),
					"admin":  feedbackAdminLabel(admin, tc.parseMode),
					"reason": "括号(需要转义)!",
				}, tc.parseMode)

				if !strings.Contains(rendered, tc.wantUserLabel) {
					t.Fatalf("user label lost or escaped: %q", rendered)
				}
				if !strings.Contains(rendered, tc.wantAdminLabel) {
					t.Fatalf("admin label lost or escaped: %q", rendered)
				}
				if !strings.Contains(rendered, tc.wantReasonSnippet) {
					t.Fatalf("plain vars should keep parse-mode escaping, got %q", rendered)
				}
			})
		}
	}
}
