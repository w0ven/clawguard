package bot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

func TestDeletedAccountIdentityMatch(t *testing.T) {
	tests := []struct {
		name        string
		user        *tele.User
		wantMatch   bool
		wantDetails string
	}{
		{
			name:        "display name in first name",
			user:        &tele.User{FirstName: "Deleted Account"},
			wantMatch:   true,
			wantDetails: "display_name=Deleted Account",
		},
		{
			name:        "display name split across fields",
			user:        &tele.User{FirstName: "Deleted", LastName: "Account"},
			wantMatch:   true,
			wantDetails: "display_name=Deleted Account",
		},
		{
			name:        "username with separators and suffix",
			user:        &tele.User{FirstName: "Delete Account", LastName: "❄️", Username: "deleted_account_sg"},
			wantMatch:   true,
			wantDetails: "username=@deleted_account_sg",
		},
		{
			name:        "unicode width and zero width normalize",
			user:        &tele.User{FirstName: "Ｄｅｌｅｔｅｄ\u200b_Account Backup"},
			wantMatch:   true,
			wantDetails: "display_name=Ｄｅｌｅｔｅｄ\u200b_Account Backup",
		},
		{
			name:        "both fields match",
			user:        &tele.User{FirstName: "Deleted Account", Username: "deletedaccount_backup"},
			wantMatch:   true,
			wantDetails: "display_name=Deleted Account | username=@deletedaccount_backup",
		},
		{
			name:      "delete is not deleted",
			user:      &tele.User{FirstName: "Delete Account", LastName: "❄️"},
			wantMatch: false,
		},
		{
			name:      "unrelated user",
			user:      &tele.User{FirstName: "Normal", LastName: "User", Username: "normal_user"},
			wantMatch: false,
		},
		{
			name:      "nil user",
			user:      nil,
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDetails, gotMatch := deletedAccountIdentityMatch(tt.user)
			if gotMatch != tt.wantMatch {
				t.Fatalf("match = %t, want %t; details=%q", gotMatch, tt.wantMatch, gotDetails)
			}
			if gotDetails != tt.wantDetails {
				t.Fatalf("details = %q, want %q", gotDetails, tt.wantDetails)
			}
		})
	}
}

func TestDeletedAccountFilterResult(t *testing.T) {
	user := &tele.User{FirstName: "Deleted", LastName: "Account"}

	result, matched := deletedAccountFilterResult(user, config.FilterDeletedAccountPolicy{Enabled: true})
	if !matched || !result.Hit {
		t.Fatalf("enabled filter did not match: matched=%t result=%+v", matched, result)
	}
	if result.Reason != deletedAccountRule || result.Action != "delete_ban" {
		t.Fatalf("unexpected filter result: %+v", result)
	}

	result, matched = deletedAccountFilterResult(user, config.FilterDeletedAccountPolicy{Enabled: false})
	if matched || result.Hit {
		t.Fatalf("disabled filter matched: matched=%t result=%+v", matched, result)
	}
}

func TestApplyDeletedAccountMessageFilterDeletesAndPermanentlyBans(t *testing.T) {
	const (
		chatID = int64(-100123)
		userID = int64(807238339)
	)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "trusted"
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{bot: botClient, logger: zap.NewNop(), queries: store.New(db)}
	cacheDeletedAccountTestSystemState(svc, false)

	policy := config.DefaultPolicy
	msg := &tele.Message{
		ID:     9101,
		Text:   "hello",
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: userID, FirstName: "Delete Account", LastName: "❄️", Username: "deleted_account_sg"},
	}

	handled, err := svc.applyDeletedAccountMessageFilter(context.Background(), msg, policy, false)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v, want handled without error", handled, err)
	}
	methods := transport.Methods()
	if got := countString(methods, "deleteMessage"); got != 1 {
		t.Fatalf("deleteMessage calls = %d, want 1; methods=%v", got, methods)
	}
	if got := countString(methods, "kickChatMember"); got != 1 {
		t.Fatalf("kickChatMember calls = %d, want 1; methods=%v", got, methods)
	}
	assertTelegramRequestParam(t, transport, "kickChatMember", "revoke_messages", "true")
	trust := db.currentTrust()
	if trust.Status != "banned" {
		t.Fatalf("trust status = %q, want banned", trust.Status)
	}
	assertDeletedAccountBanReason(t, trust.BannedReason, "username=@deleted_account_sg")
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.violations) != 1 || db.violations[0].Rule != deletedAccountRule || db.violations[0].Action != "delete_ban" {
		t.Fatalf("violations = %+v, want one deleted-account delete_ban record", db.violations)
	}
}

func TestApplyDeletedAccountMessageFilterWhilePausedRecordsWithoutTelegramAction(t *testing.T) {
	const (
		chatID = int64(-100123)
		userID = int64(7774746654)
	)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "trusted"
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{bot: botClient, logger: zap.NewNop(), queries: store.New(db)}

	msg := &tele.Message{
		ID:     9102,
		Text:   "hello",
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: userID, FirstName: "Deleted", LastName: "Account", Username: "biubiu668866"},
	}
	handled, err := svc.applyDeletedAccountMessageFilter(context.Background(), msg, config.DefaultPolicy, true)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v, want handled paused hit", handled, err)
	}
	methods := transport.Methods()
	if containsString(methods, "deleteMessage") || containsString(methods, "kickChatMember") {
		t.Fatalf("telegram methods = %v, want no moderation action while paused", methods)
	}
	if got := db.currentTrust().Status; got != "trusted" {
		t.Fatalf("trust status = %q, want trusted", got)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.violations) != 1 || db.violations[0].Rule != deletedAccountRule || db.violations[0].Action != "skipped_paused:delete_ban" {
		t.Fatalf("violations = %+v, want one skipped_paused deleted-account record", db.violations)
	}
}

func TestHandleDeletedAccountJoinPermanentlyBansAndDeduplicates(t *testing.T) {
	const (
		chatID = int64(-100123)
		userID = int64(7774746654)
	)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "trusted"
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{bot: botClient, logger: zap.NewNop(), queries: store.New(db)}
	cacheDeletedAccountTestSystemState(svc, false)
	chat := &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}
	user := &tele.User{ID: userID, FirstName: "Deleted", LastName: "Account"}

	for i := 0; i < 2; i++ {
		handled, err := svc.handleDeletedAccountJoin(context.Background(), chat, user, config.DefaultPolicy, false)
		if err != nil || !handled {
			t.Fatalf("call %d handled=%t err=%v, want handled without error", i+1, handled, err)
		}
	}
	methods := transport.Methods()
	if got := countString(methods, "kickChatMember"); got != 1 {
		t.Fatalf("kickChatMember calls = %d, want 1; methods=%v", got, methods)
	}
	assertTelegramRequestParam(t, transport, "kickChatMember", "revoke_messages", "true")
	trust := db.currentTrust()
	if trust.Status != "banned" {
		t.Fatalf("trust status = %q, want banned", trust.Status)
	}
	assertDeletedAccountBanReason(t, trust.BannedReason, "display_name=Deleted Account")
}

func TestHandleDeletedAccountJoinWhilePausedIsHandledWithoutTelegramAction(t *testing.T) {
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{bot: botClient, logger: zap.NewNop()}
	chat := &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup}
	user := &tele.User{ID: 7774746654, FirstName: "Deleted", LastName: "Account"}

	handled, err := svc.handleDeletedAccountJoin(context.Background(), chat, user, config.DefaultPolicy, true)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v, want handled paused hit", handled, err)
	}
	if methods := transport.Methods(); containsString(methods, "kickChatMember") || containsString(methods, "deleteMessage") {
		t.Fatalf("telegram methods = %v, want no moderation action while paused", methods)
	}
}

func assertDeletedAccountBanReason(t *testing.T, raw []byte, wantMatched string) {
	t.Helper()
	var meta map[string]string
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("decode banned_reason %q: %v", raw, err)
	}
	if meta["rule"] != deletedAccountRule || meta["matched"] != wantMatched || meta["source"] != "deleted_account_filter" {
		t.Fatalf("banned_reason = %+v, want rule=%q matched=%q source=deleted_account_filter", meta, deletedAccountRule, wantMatched)
	}
}

func cacheDeletedAccountTestSystemState(svc *Service, actionsPaused bool) {
	svc.systemState.Store(store.SystemState{ActionsPaused: actionsPaused})
	svc.systemStateAt.Store(time.Now().UnixNano())
}
