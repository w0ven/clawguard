package bot

import (
	tele "gopkg.in/telebot.v3"
	"testing"
)

func TestAnonymousAdminIdentityRequiresSameGroupSenderChat(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  *tele.Message
		want bool
	}{
		{"anonymous", &tele.Message{Chat: &tele.Chat{ID: -1001, Type: tele.ChatSuperGroup}, SenderChat: &tele.Chat{ID: -1001}, Sender: &tele.User{ID: 1087968824, IsBot: true}}, true},
		{"no_fake_sender", &tele.Message{Chat: &tele.Chat{ID: -1001, Type: tele.ChatGroup}, SenderChat: &tele.Chat{ID: -1001}}, true},
		{"external_channel", &tele.Message{Chat: &tele.Chat{ID: -1001, Type: tele.ChatSuperGroup}, SenderChat: &tele.Chat{ID: -2002, Type: tele.ChatChannel}, Sender: &tele.User{ID: 1087968824, IsBot: true}}, false},
		{"username_not_identity", &tele.Message{Chat: &tele.Chat{ID: -1001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 42, Username: "GroupAnonymousBot"}}, false},
		{"synthetic_id_alone_not_identity", &tele.Message{Chat: &tele.Chat{ID: -1001, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 1087968824, IsBot: true}}, false},
		{"linked_forward", &tele.Message{Chat: &tele.Chat{ID: -1001, Type: tele.ChatSuperGroup}, SenderChat: &tele.Chat{ID: -1001}, AutomaticForward: true}, false},
		{"channel_not_group", &tele.Message{Chat: &tele.Chat{ID: -1001, Type: tele.ChatChannel}, SenderChat: &tele.Chat{ID: -1001}}, false},
		{"nil", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAnonymousGroupAdminMessage(tt.msg); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestAnonymousAdminNewAndEditedMessagesSkipUserModeration(t *testing.T) {
	tb, transport := newMockTelegramBot(t, "")
	initialCalls := len(transport.Methods())
	// Deliberately no DB, AI client or trust state: a regression into ordinary
	// moderation fails before it can penalize the synthetic bot identity.
	svc := &Service{}
	for _, edited := range []bool{false, true} {
		msg := &tele.Message{ID: 57164, Text: "快领", Chat: &tele.Chat{ID: -1003939238239, Type: tele.ChatSuperGroup}, SenderChat: &tele.Chat{ID: -1003939238239, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 1087968824, Username: "GroupAnonymousBot", IsBot: true}}
		if err := svc.handleIncomingMessageWithOptions(tb.NewContext(tele.Update{Message: msg}), edited); err != nil {
			t.Fatal(err)
		}
	}
	if methods := transport.Methods(); len(methods) != initialCalls {
		t.Fatalf("anonymous admin triggered Telegram actions: %v", methods)
	}
}
