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

func TestRenderFeedbackTemplateUserMentionAcrossParseModes(t *testing.T) {
	cases := []struct {
		name      string
		parseMode string
		user      *tele.User
		want      string
	}{
		{
			name:      "html with username",
			parseMode: tele.ModeHTML,
			user:      &tele.User{ID: 42, Username: "my_name", FirstName: "可乐"},
			want:      "@my_name 违规",
		},
		{
			name:      "markdownv2 without username",
			parseMode: tele.ModeMarkdownV2,
			user:      &tele.User{ID: 42, FirstName: "可乐"},
			want:      "[可乐](tg://user?id=42) 违规",
		},
		{
			name:      "default nil user",
			parseMode: "",
			user:      nil,
			want:      "该用户 违规",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderFeedbackTemplate("{user_mention} 违规", map[string]string{
				"user_mention": feedbackUserMention(tc.user, tc.parseMode),
			}, tc.parseMode)

			if rendered != tc.want {
				t.Fatalf("rendered = %q, want %q", rendered, tc.want)
			}
		})
	}
}
