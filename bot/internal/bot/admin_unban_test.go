package bot

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestNoUnbanNeededTelegramErrorIsBenign(t *testing.T) {
	cases := []string{
		"Bad Request: user not found",
		"Bad Request: member not found",
		"Bad Request: user is not banned",
		"Bad Request: user is not kicked",
		"Bad Request: PARTICIPANT_ID_INVALID",
	}
	for _, tc := range cases {
		if !isNoUnbanNeededTelegramError(errors.New(tc)) {
			t.Fatalf("%q should be treated as no-op unban", tc)
		}
	}
}

func TestNoUnbanNeededTelegramErrorDoesNotHideRightsFailures(t *testing.T) {
	if isNoUnbanNeededTelegramError(errors.New("Bad Request: not enough rights to unban chat member")) {
		t.Fatal("rights failure should remain actionable error")
	}
}

func TestHandleUnbanCommandDoesNotUseDangerousPermissionActions(t *testing.T) {
	raw, err := os.ReadFile("bot.go")
	if err != nil {
		t.Fatalf("read bot.go: %v", err)
	}
	source := string(raw)
	start := strings.Index(source, "func (s *Service) handleUnbanCommand")
	if start < 0 {
		t.Fatal("handleUnbanCommand not found")
	}
	end := strings.Index(source[start:], "func (s *Service) handleSpamCommand")
	if end < 0 {
		t.Fatal("handleSpamCommand marker not found")
	}
	body := source[start : start+end]
	for _, forbidden := range []string{".Ban(", ".Restrict(", ".Promote(", "UnmuteChatUser("} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("handleUnbanCommand contains dangerous action %s", forbidden)
		}
	}
	if !strings.Contains(body, "SafeUnbanChatUser") {
		t.Fatal("handleUnbanCommand should use SafeUnbanChatUser")
	}
}

func TestSafeUnbanChatUserUsesOnlyIfBanned(t *testing.T) {
	raw, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatalf("read admin.go: %v", err)
	}
	source := string(raw)
	start := strings.Index(source, "func (s *Service) SafeUnbanChatUser")
	if start < 0 {
		t.Fatal("SafeUnbanChatUser not found")
	}
	end := strings.Index(source[start:], "func isNoUnbanNeededTelegramError")
	if end < 0 {
		t.Fatal("isNoUnbanNeededTelegramError marker not found")
	}
	body := source[start : start+end]
	if !strings.Contains(body, "s.bot.Unban(&tele.Chat{ID: chatID}, &tele.User{ID: userID}, true)") {
		t.Fatal("SafeUnbanChatUser must pass only_if_banned=true to Telegram Unban")
	}
}
