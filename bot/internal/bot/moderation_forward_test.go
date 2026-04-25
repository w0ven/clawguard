package bot

import (
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestExtractForwardSourceLegacyFields(t *testing.T) {
	msg := &tele.Message{OriginalChat: &tele.Chat{Title: "Legacy Channel"}}
	if got := extractForwardSource(msg); got != "Legacy Channel" {
		t.Fatalf("legacy channel source = %q", got)
	}

	msg = &tele.Message{OriginalSender: &tele.User{FirstName: "Legacy", LastName: "User", Username: "legacy_user"}}
	if got := extractForwardSource(msg); got != "legacy_user" {
		t.Fatalf("legacy sender source = %q", got)
	}

	msg = &tele.Message{OriginalSenderName: "Hidden Legacy"}
	if got := extractForwardSource(msg); got != "Hidden Legacy" {
		t.Fatalf("legacy hidden source = %q", got)
	}
}

func TestExtractForwardSourceMessageOrigin(t *testing.T) {
	tests := []struct {
		name string
		msg  *tele.Message
		want string
	}{
		{
			name: "sender chat",
			msg:  &tele.Message{Origin: &tele.MessageOrigin{SenderChat: &tele.Chat{Title: "Origin Channel"}}},
			want: "Origin Channel",
		},
		{
			name: "chat",
			msg:  &tele.Message{Origin: &tele.MessageOrigin{Chat: &tele.Chat{Title: "Origin Chat"}}},
			want: "Origin Chat",
		},
		{
			name: "sender",
			msg:  &tele.Message{Origin: &tele.MessageOrigin{Sender: &tele.User{FirstName: "Origin", LastName: "User", Username: "origin_user"}}},
			want: "origin_user",
		},
		{
			name: "sender username",
			msg:  &tele.Message{Origin: &tele.MessageOrigin{SenderUsername: "Hidden Origin"}},
			want: "Hidden Origin",
		},
		{
			name: "signature",
			msg:  &tele.Message{Origin: &tele.MessageOrigin{Signature: "Anonymous Admin"}},
			want: "Anonymous Admin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractForwardSource(tt.msg); got != tt.want {
				t.Fatalf("source = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMatchesTriggerKeywordsUsesMessageOrigin(t *testing.T) {
	svc := &Service{}
	msg := &tele.Message{Origin: &tele.MessageOrigin{SenderChat: &tele.Chat{Title: "Spam Channel"}}}
	if !svc.matchesTriggerKeywords(msg, []string{"spam"}) {
		t.Fatal("expected trigger keyword to match forward_origin source")
	}
}
