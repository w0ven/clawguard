package bot

import (
	"reflect"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestCheckBotAdminPermissions_PrivateChat_NoWarn(t *testing.T) {
	chat := &tele.Chat{ID: 5991295345, Type: tele.ChatPrivate, Username: "simonw_tele"}
	member := &tele.ChatMember{Role: tele.Member}

	if !shouldSkipBotAdminPermissionCheck(chat) {
		t.Fatal("private chat should skip bot admin permission check")
	}
	if missing := botAdminPermissionMissingNames(chat, true, member); len(missing) != 0 {
		t.Fatalf("missing permissions = %v, want none", missing)
	}
}

func TestCheckBotAdminPermissions_Channel_NoWarn(t *testing.T) {
	chat := &tele.Chat{ID: -100123, Type: tele.ChatChannel, Title: "Announcements"}
	member := &tele.ChatMember{Role: tele.Member}

	if !shouldSkipBotAdminPermissionCheck(chat) {
		t.Fatal("channel should skip bot admin permission check")
	}
	if missing := botAdminPermissionMissingNames(chat, true, member); len(missing) != 0 {
		t.Fatalf("missing permissions = %v, want none", missing)
	}
}

func TestCheckBotAdminPermissions_UnauthorizedGroup_NoOwnerWarn(t *testing.T) {
	chat := &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup, Title: "Unlisted"}
	member := &tele.ChatMember{Role: tele.Member}

	if shouldSkipBotAdminPermissionCheck(chat) {
		t.Fatal("group should not skip chat-type permission check")
	}
	if missing := botAdminPermissionMissingNames(chat, false, member); len(missing) != 0 {
		t.Fatalf("missing permissions = %v, want none for unauthorized group", missing)
	}
}

func TestCheckBotAdminPermissions_AuthorizedGroup_MissingPerms_Warns(t *testing.T) {
	chat := &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup, Title: "RFCHOST官方群组"}
	member := &tele.ChatMember{Role: tele.Administrator, Rights: tele.Rights{CanDeleteMessages: true, CanRestrictMembers: false}}

	missing := botAdminPermissionMissingNames(chat, true, member)
	want := []string{"封禁/禁言成员"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing permissions = %v, want %v", missing, want)
	}
}

func TestFormatOwnerWarnChatLabel(t *testing.T) {
	tests := []struct {
		name string
		chat *tele.Chat
		want string
	}{
		{
			name: "title priority",
			chat: &tele.Chat{ID: -1002699516772, Type: tele.ChatSuperGroup, Title: "RFCHOST官方群组", Username: "rfchost"},
			want: "RFCHOST官方群组 [-1002699516772] (supergroup)",
		},
		{
			name: "username fallback",
			chat: &tele.Chat{ID: -100456, Type: tele.ChatChannel, Username: "somechan"},
			want: "@somechan [-100456] (channel)",
		},
		{
			name: "unnamed group",
			chat: &tele.Chat{ID: -100789, Type: tele.ChatGroup},
			want: "未命名 [-100789] (group)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatOwnerWarnChatLabel(tt.chat); got != tt.want {
				t.Fatalf("formatOwnerWarnChatLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}
