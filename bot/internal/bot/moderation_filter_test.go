package bot

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func telegramRequestInt64(t *testing.T, transport *telegramMockTransport, method, key string) int64 {
	t.Helper()
	methods := transport.Methods()
	bodies := transport.RequestBodies()
	for index, gotMethod := range methods {
		if gotMethod != method || index >= len(bodies) {
			continue
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal([]byte(bodies[index]), &values); err != nil {
			t.Fatalf("decode %s request: %v", method, err)
		}
		var stringValue string
		if err := json.Unmarshal(values[key], &stringValue); err == nil {
			value, parseErr := strconv.ParseInt(stringValue, 10, 64)
			if parseErr != nil {
				t.Fatalf("parse %s.%s: %v", method, key, parseErr)
			}
			return value
		}
		var numberValue int64
		if err := json.Unmarshal(values[key], &numberValue); err != nil {
			t.Fatalf("decode %s.%s: %v", method, key, err)
		}
		return numberValue
	}
	t.Fatalf("telegram method %s not found", method)
	return 0
}

func TestApplyFilterChecksHandledFlag(t *testing.T) {
	svc := &Service{}
	msg := &tele.Message{
		Text:   "clean message",
		Chat:   &tele.Chat{ID: -100},
		Sender: &tele.User{ID: 123},
	}

	policy := config.DefaultPolicy
	policy.Filter.Keywords.Enabled = true
	policy.Filter.Keywords.List = []string{"blocked"}

	handled, err := svc.applyFilterChecks(context.Background(), msg, policy, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error on non-hit filter: %v", err)
	}
	if handled {
		t.Fatalf("handled = true, want false when filter does not hit")
	}
}

func TestApplyFilterChecksWhilePausedRecordsHitWithoutSideEffects(t *testing.T) {
	chatID := int64(-100123)
	userID := int64(777)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "trusted"
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient}

	policy := config.DefaultPolicy
	policy.Filter.Keywords.Enabled = true
	policy.Filter.Keywords.List = []string{"blocked"}
	policy.Filter.Keywords.Action = "delete_ban"
	msg := &tele.Message{
		ID:     9001,
		Text:   "blocked content",
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: userID, FirstName: "Alice"},
	}

	handled, err := svc.applyFilterChecks(context.Background(), msg, policy, false, false, true)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v; want handled paused hit", handled, err)
	}
	if methods := transport.Methods(); containsString(methods, "deleteMessage") || containsString(methods, "kickChatMember") || containsString(methods, "restrictChatMember") {
		t.Fatalf("telegram methods = %v, want no moderation action while paused", methods)
	}
	if got := db.currentTrust().Status; got != "trusted" {
		t.Fatalf("trust status = %q, want trusted", got)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.violations) != 1 || db.violations[0].Action != "skipped_paused:delete_ban" {
		t.Fatalf("violations = %+v, want one skipped_paused record", db.violations)
	}
}

func TestApplyFilterChecksWhilePausedDoesNotRestrictUngraduatedPermissions(t *testing.T) {
	chatID := int64(-100123)
	userID := int64(781)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "new"
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient}

	policy := config.DefaultPolicy
	policy.Filter.NewUser.Enabled = true
	policy.Filter.NewUser.NoMedia = true
	policy.Filter.NonTextMessages = "off"
	msg := &tele.Message{
		ID:     9005,
		Photo:  &tele.Photo{},
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: userID, FirstName: "Alice"},
	}

	handled, err := svc.applyFilterChecks(context.Background(), msg, policy, false, false, true)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v; want handled paused new-user hit", handled, err)
	}
	if methods := transport.Methods(); containsString(methods, "restrictChatMember") || containsString(methods, "deleteMessage") {
		t.Fatalf("telegram methods = %v, want no permission or delete action while paused", methods)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.violations) != 1 || db.violations[0].Action != "skipped_paused:delete" {
		t.Fatalf("violations = %+v, want one skipped_paused delete", db.violations)
	}
}

func TestApplyFilterActionUsesConfiguredMuteDurations(t *testing.T) {
	tests := []struct {
		action  string
		seconds int64
	}{
		{action: "mute_5m", seconds: 300},
		{action: "mute_1h", seconds: 3600},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			chatID := int64(-100123)
			userID := int64(800)
			db := newModerationProfileMatchMockDB(chatID, userID)
			db.userTrust.Status = "trusted"
			botClient, transport := newMockTelegramBot(t, "")
			svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient}
			policy := config.DefaultPolicy
			policy.Feedback.Mute.Enabled = false
			msg := &tele.Message{ID: 9100, Text: "too fast", Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: userID}}

			startedAt := time.Now().Unix()
			if err := svc.applyFilterAction(context.Background(), msg, policy, FilterResult{Hit: true, Reason: "filter_rate_limit", Action: tt.action}); err != nil {
				t.Fatal(err)
			}
			finishedAt := time.Now().Unix()
			deadline := telegramRequestInt64(t, transport, "restrictChatMember", "until_date")
			if deadline < startedAt+tt.seconds-2 || deadline > finishedAt+tt.seconds+2 {
				t.Fatalf("restricted until %d, want approximately now+%ds", deadline, tt.seconds)
			}
		})
	}
}

func TestApplyFilterActionIsIdempotentAcrossWebhookRetry(t *testing.T) {
	chatID := int64(-100123)
	userID := int64(778)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "trusted"
	botClient, transport := newMockTelegramBot(t, "")
	_, client := newJoinProtectionTestRedis(t)
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), redis: client, bot: botClient}

	policy := config.DefaultPolicy
	policy.Feedback.DeleteMsg.Enabled = false
	policy.Feedback.Warn.Enabled = false
	msg := &tele.Message{
		ID:     9002,
		Text:   "blocked content",
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: userID},
	}
	result := FilterResult{Hit: true, Reason: "filter_keyword", MatchedRule: "blocked", Action: "delete_warn"}

	if err := svc.applyFilterAction(context.Background(), msg, policy, result); err != nil {
		t.Fatal(err)
	}
	if err := svc.applyFilterAction(context.Background(), msg, policy, result); err != nil {
		t.Fatal(err)
	}

	if got := countString(transport.Methods(), "deleteMessage"); got != 1 {
		t.Fatalf("deleteMessage calls = %d, want 1", got)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.warnings) != 1 || len(db.violations) != 1 {
		t.Fatalf("warnings=%d violations=%d, want one of each", len(db.warnings), len(db.violations))
	}
}

func TestApplyFilterActionRetryDoesNotRepeatExecutedAction(t *testing.T) {
	chatID := int64(-100123)
	userID := int64(779)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "trusted"
	db.failViolationInserts = 1
	botClient, transport := newMockTelegramBot(t, "")
	_, client := newJoinProtectionTestRedis(t)
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), redis: client, bot: botClient}

	policy := config.DefaultPolicy
	policy.Feedback.DeleteMsg.Enabled = false
	policy.Feedback.Warn.Enabled = false
	msg := &tele.Message{ID: 9003, Text: "blocked", Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: userID}}
	result := FilterResult{Hit: true, Reason: "filter_keyword", MatchedRule: "blocked", Action: "delete_warn"}

	if err := svc.applyFilterAction(context.Background(), msg, policy, result); err == nil {
		t.Fatal("first action succeeded, want injected violation insert failure")
	}
	if err := svc.applyFilterAction(context.Background(), msg, policy, result); err != nil {
		t.Fatalf("retry failed: %v", err)
	}

	if got := countString(transport.Methods(), "deleteMessage"); got != 1 {
		t.Fatalf("deleteMessage calls = %d, want 1 across retry", got)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.warnings) != 1 || len(db.violations) != 1 {
		t.Fatalf("warnings=%d violations=%d, want one of each across retry", len(db.warnings), len(db.violations))
	}
}

func TestApplyFilterActionRetryResumesAfterWarningInsert(t *testing.T) {
	chatID := int64(-100123)
	userID := int64(780)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "trusted"
	db.failWarningQueries = 1
	botClient, transport := newMockTelegramBot(t, "")
	_, client := newJoinProtectionTestRedis(t)
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), redis: client, bot: botClient}

	policy := config.DefaultPolicy
	policy.Feedback.DeleteMsg.Enabled = false
	policy.Feedback.Warn.Enabled = false
	msg := &tele.Message{ID: 9004, Text: "blocked", Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: userID}}
	result := FilterResult{Hit: true, Reason: "filter_keyword", MatchedRule: "blocked", Action: "delete_warn"}

	if err := svc.applyFilterAction(context.Background(), msg, policy, result); err == nil {
		t.Fatal("first action succeeded, want injected warning query failure")
	}
	if err := svc.applyFilterAction(context.Background(), msg, policy, result); err != nil {
		t.Fatalf("retry failed: %v", err)
	}

	if got := countString(transport.Methods(), "deleteMessage"); got != 1 {
		t.Fatalf("deleteMessage calls = %d, want 1 across partial warning retry", got)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.warnings) != 1 || len(db.violations) != 1 {
		t.Fatalf("warnings=%d violations=%d, want one of each across partial warning retry", len(db.warnings), len(db.violations))
	}
}

func TestIsRestrictedNewUserUsesTrustStatusNotJoinDuration(t *testing.T) {
	joinedLongAgo := time.Now().Add(-72 * time.Hour)

	tests := []struct {
		name  string
		trust store.UserTrust
		want  bool
	}{
		{
			name:  "new user stays restricted after legacy duration",
			trust: store.UserTrust{Status: "new", JoinedAt: joinedLongAgo},
			want:  true,
		},
		{
			name:  "suspicious user is restricted",
			trust: store.UserTrust{Status: "suspicious", JoinedAt: joinedLongAgo},
			want:  true,
		},
		{
			name:  "trusted user is not restricted",
			trust: store.UserTrust{Status: "trusted", JoinedAt: time.Now()},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRestrictedNewUser(tt.trust, 24); got != tt.want {
				t.Fatalf("isRestrictedNewUser() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCheckNewUserFilterFallbacksUseDeleteAction(t *testing.T) {
	svc := &Service{}
	trust := store.UserTrust{Status: "new"}
	policy := config.FilterNewUserPolicy{
		Enabled:    true,
		NoLinks:    true,
		NoForwards: true,
		NoMedia:    true,
	}

	tests := []struct {
		name string
		msg  *tele.Message
		want string
	}{
		{
			name: "link",
			msg: &tele.Message{
				Text:   "see https://example.com",
				Chat:   &tele.Chat{ID: -100},
				Sender: &tele.User{ID: 42},
			},
			want: "filter_newuser_no_links",
		},
		{
			name: "forward",
			msg: &tele.Message{
				Chat:               &tele.Chat{ID: -100},
				Sender:             &tele.User{ID: 42},
				OriginalSenderName: "source",
			},
			want: "filter_newuser_no_forwards",
		},
		{
			name: "media",
			msg: &tele.Message{
				Chat:   &tele.Chat{ID: -100},
				Sender: &tele.User{ID: 42},
				Photo:  &tele.Photo{},
			},
			want: "filter_newuser_no_media",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := svc.checkNewUserFilter(context.Background(), tt.msg, trust, policy, true)
			if err != nil {
				t.Fatalf("checkNewUserFilter returned error: %v", err)
			}
			if !result.Hit {
				t.Fatalf("Hit = false, want true")
			}
			if result.Reason != tt.want {
				t.Fatalf("Reason = %q, want %q", result.Reason, tt.want)
			}
			if result.Action != "delete" {
				t.Fatalf("Action = %q, want delete", result.Action)
			}
			if result.ContinueReview {
				t.Fatalf("ContinueReview = true, want false for %s", tt.name)
			}
		})
	}
}

func TestCheckNewUserFilterContactContinuesReview(t *testing.T) {
	svc := &Service{}
	trust := store.UserTrust{Status: "new"}
	policy := config.FilterNewUserPolicy{
		Enabled: true,
		NoMedia: true,
	}
	msg := &tele.Message{
		Chat:   &tele.Chat{ID: -100},
		Sender: &tele.User{ID: 42},
		Contact: &tele.Contact{
			FirstName:   "Crypto_R",
			PhoneNumber: "+8615736504420",
		},
	}

	result, err := svc.checkNewUserFilter(context.Background(), msg, trust, policy, true)
	if err != nil {
		t.Fatalf("checkNewUserFilter returned error: %v", err)
	}
	if !result.Hit {
		t.Fatal("Hit = false, want true")
	}
	if result.Reason != "filter_newuser_no_media" {
		t.Fatalf("Reason = %q, want filter_newuser_no_media", result.Reason)
	}
	if result.MatchedRule != "contact" {
		t.Fatalf("MatchedRule = %q, want contact", result.MatchedRule)
	}
	if result.Action != "delete" {
		t.Fatalf("Action = %q, want delete", result.Action)
	}
	if !result.ContinueReview {
		t.Fatal("ContinueReview = false, want true for contact")
	}
}

func TestApplyFilterChecksContinuesReviewAfterUngraduatedContactDelete(t *testing.T) {
	chatID := int64(-100123)
	userID := int64(8873667625)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "new"
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient}

	policy := config.DefaultPolicy
	policy.Filter.NewUser.Enabled = true
	policy.Filter.NewUser.NoMedia = true
	policy.Filter.NewUser.NoInvites = true

	msg := &tele.Message{
		ID:     12095,
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup, Title: "test-group"},
		Sender: &tele.User{ID: userID, Username: "chusaiqu", FirstName: "一騎"},
		Contact: &tele.Contact{
			FirstName:   "Crypto_R總",
			PhoneNumber: "+8615736504420",
		},
	}

	handled, err := svc.applyFilterChecks(context.Background(), msg, policy, false, false, false)
	if err != nil {
		t.Fatalf("applyFilterChecks returned error: %v", err)
	}
	if handled {
		t.Fatal("handled = true, want false so contact can continue to AI review")
	}

	methods := transport.Methods()
	if got := countString(methods, "restrictChatMember"); got != 1 {
		t.Fatalf("restrictChatMember calls = %d, want 1; methods=%v", got, methods)
	}
	if got := countString(methods, "deleteMessage"); got != 1 {
		t.Fatalf("deleteMessage calls = %d, want 1; methods=%v", got, methods)
	}
}

func TestApplyFilterChecksSyncsUngraduatedPermissionsOnNewUserMediaHit(t *testing.T) {
	chatID := int64(-100123)
	userID := int64(7945990865)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "suspicious"
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient}

	policy := config.DefaultPolicy
	policy.Filter.NewUser.Enabled = true
	policy.Filter.NewUser.NoMedia = true
	policy.Filter.NewUser.NoInvites = true

	msg := &tele.Message{
		ID:     1001,
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup, Title: "test-group"},
		Sender: &tele.User{ID: userID, Username: "legacy_new"},
		Photo:  &tele.Photo{},
	}

	handled, err := svc.applyFilterChecks(context.Background(), msg, policy, false, false, false)
	if err != nil {
		t.Fatalf("applyFilterChecks returned error: %v", err)
	}
	if !handled {
		t.Fatal("handled = false, want true")
	}

	methods := transport.Methods()
	if got := countString(methods, "restrictChatMember"); got != 1 {
		t.Fatalf("restrictChatMember calls = %d, want 1; methods=%v", got, methods)
	}
	if got := countString(methods, "deleteMessage"); got != 1 {
		t.Fatalf("deleteMessage calls = %d, want 1; methods=%v", got, methods)
	}
}

func TestApplyFilterChecksSkipsUngraduatedFiltersForAdmin(t *testing.T) {
	chatID := int64(-100123)
	userID := int64(5105038894)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "suspicious"
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient}

	policy := config.DefaultPolicy
	policy.Filter.NewUser.Enabled = true
	policy.Filter.NewUser.NoMedia = true
	policy.Filter.NewUser.NoInvites = true

	msg := &tele.Message{
		ID:     1001,
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup, Title: "test-group"},
		Sender: &tele.User{ID: userID, Username: "admin_user"},
		Photo:  &tele.Photo{},
	}

	handled, err := svc.applyFilterChecks(context.Background(), msg, policy, true, false, false)
	if err != nil {
		t.Fatalf("applyFilterChecks returned error: %v", err)
	}
	if handled {
		t.Fatal("handled = true, want false for admin ungraduated media")
	}

	methods := transport.Methods()
	if got := countString(methods, "restrictChatMember"); got != 0 {
		t.Fatalf("restrictChatMember calls = %d, want 0; methods=%v", got, methods)
	}
	if got := countString(methods, "deleteMessage"); got != 0 {
		t.Fatalf("deleteMessage calls = %d, want 0; methods=%v", got, methods)
	}
}

func TestCheckMessageRegexHit(t *testing.T) {
	msg := &tele.Message{
		Text:   "buy cheap crypto now",
		Sender: &tele.User{ID: 1},
	}
	policy := config.DefaultPolicy.Filter
	policy.Regex.Enabled = true
	policy.Regex.Patterns = []string{`cheap\s+crypto`}

	result := checkMessage(context.Background(), msg, policy, false)
	if !result.Hit {
		t.Fatalf("expected regex hit")
	}
	if result.Reason != "filter_regex" {
		t.Fatalf("reason = %q, want filter_regex", result.Reason)
	}
	if result.MatchedRule != `cheap\s+crypto` {
		t.Fatalf("matched rule = %q", result.MatchedRule)
	}
}

func TestCheckMessageUsernameBlacklistHit(t *testing.T) {
	msg := &tele.Message{
		Text:   "hello",
		Sender: &tele.User{ID: 1, Username: "SpamAccount"},
	}
	policy := config.DefaultPolicy.Filter
	policy.Usernames.Enabled = true
	policy.Usernames.Blacklist = []string{"@spamaccount"}

	result := checkMessage(context.Background(), msg, policy, false)
	if !result.Hit {
		t.Fatalf("expected username blacklist hit")
	}
	if result.Reason != "filter_username" {
		t.Fatalf("reason = %q, want filter_username", result.Reason)
	}
}

func keywordFilterPolicy(keywords ...string) config.FilterConfig {
	policy := config.DefaultPolicy.Filter
	policy.Keywords.Enabled = true
	policy.Keywords.List = keywords
	policy.Links.Enabled = false
	return policy
}

func TestCheckMessagePollQuestionAndOptionKeywordHit(t *testing.T) {
	t.Run("question", func(t *testing.T) {
		msg := &tele.Message{
			Sender: &tele.User{ID: 1},
			Poll: &tele.Poll{
				Question: "如何免费领取空投？",
				Options:  []tele.PollOption{{Text: "点这里"}, {Text: "算了"}},
			},
		}
		result := checkMessage(context.Background(), msg, keywordFilterPolicy("空投"), false)
		if !result.Hit || result.Reason != "filter_keyword" || result.MatchedRule != "空投" {
			t.Fatalf("result = %+v, want poll question keyword hit", result)
		}
	})
	t.Run("option", func(t *testing.T) {
		msg := &tele.Message{
			Sender: &tele.User{ID: 1},
			Poll: &tele.Poll{
				Question: "晚饭吃什么",
				Options:  []tele.PollOption{{Text: "米饭"}, {Text: "加微信领红包"}},
			},
		}
		result := checkMessage(context.Background(), msg, keywordFilterPolicy("加微信"), false)
		if !result.Hit || result.Reason != "filter_keyword" || result.MatchedRule != "加微信" {
			t.Fatalf("result = %+v, want poll option keyword hit", result)
		}
	})
}

func TestCheckMessageContactNameOrPhoneKeywordHit(t *testing.T) {
	t.Run("name", func(t *testing.T) {
		msg := &tele.Message{
			Sender: &tele.User{ID: 1},
			Contact: &tele.Contact{
				FirstName:   "Crypto",
				LastName:    "Seller",
				PhoneNumber: "+8610000000000",
				UserID:      4242,
			},
		}
		result := checkMessage(context.Background(), msg, keywordFilterPolicy("Seller"), false)
		if !result.Hit || result.Reason != "filter_keyword" || result.MatchedRule != "Seller" {
			t.Fatalf("result = %+v, want contact name keyword hit", result)
		}
	})
	t.Run("phone", func(t *testing.T) {
		msg := &tele.Message{
			Sender: &tele.User{ID: 1},
			Contact: &tele.Contact{
				FirstName:   "Alice",
				PhoneNumber: "+8615736504420",
				UserID:      4242,
			},
		}
		result := checkMessage(context.Background(), msg, keywordFilterPolicy("15736504420"), false)
		if !result.Hit || result.Reason != "filter_keyword" || result.MatchedRule != "15736504420" {
			t.Fatalf("result = %+v, want contact phone keyword hit", result)
		}
	})
}

func TestCheckMessageVenueTitleKeywordHit(t *testing.T) {
	msg := &tele.Message{
		Sender: &tele.User{ID: 1},
		Venue: &tele.Venue{
			Title:    "博彩体验馆",
			Address:  "Example Street 1",
			Location: tele.Location{Lat: 31.23, Lng: 121.47},
		},
	}
	result := checkMessage(context.Background(), msg, keywordFilterPolicy("博彩"), false)
	if !result.Hit || result.Reason != "filter_keyword" || result.MatchedRule != "博彩" {
		t.Fatalf("result = %+v, want venue title keyword hit", result)
	}
}

func TestCheckMessageInvoiceOrDocumentKeywordHit(t *testing.T) {
	t.Run("invoice description", func(t *testing.T) {
		msg := &tele.Message{
			Sender: &tele.User{ID: 1},
			Invoice: &tele.Invoice{
				Title:       "会员开通",
				Description: "代刷单服务费",
			},
		}
		result := checkMessage(context.Background(), msg, keywordFilterPolicy("代刷单"), false)
		if !result.Hit || result.Reason != "filter_keyword" || result.MatchedRule != "代刷单" {
			t.Fatalf("result = %+v, want invoice description keyword hit", result)
		}
	})
	t.Run("document filename", func(t *testing.T) {
		msg := &tele.Message{
			Sender:   &tele.User{ID: 1},
			Document: &tele.Document{FileName: "casino-invite.pdf"},
		}
		result := checkMessage(context.Background(), msg, keywordFilterPolicy("casino"), false)
		if !result.Hit || result.Reason != "filter_keyword" || result.MatchedRule != "casino" {
			t.Fatalf("result = %+v, want document filename keyword hit", result)
		}
	})
}

func TestCheckMessageContactDoesNotHitAIReviewTags(t *testing.T) {
	msg := &tele.Message{
		Sender: &tele.User{ID: 1},
		Contact: &tele.Contact{
			FirstName:   "Alice",
			LastName:    "Lee",
			PhoneNumber: "+8610000000000",
			UserID:      987654321,
		},
	}
	for _, keyword := range []string{"[联系人]", "telegram_user="} {
		result := checkMessage(context.Background(), msg, keywordFilterPolicy(keyword), false)
		if result.Hit {
			t.Fatalf("keyword %q hit %+v; filter must not ingest AI tags or telegram identity", keyword, result)
		}
	}
}

func TestNormalizeBioAICategory(t *testing.T) {
	tests := []struct {
		name     string
		verdict  string
		category string
		want     string
	}{
		{name: "empty category falls back", verdict: "scam", category: "", want: "scam"},
		{name: "normal chinese falls back", verdict: "scam", category: "正常", want: "scam"},
		{name: "normal english falls back case insensitive", verdict: "ad", category: " Normal ", want: "ad"},
		{name: "safe synonym falls back", verdict: "spam", category: "SAFE", want: "spam"},
		{name: "none synonym falls back", verdict: "harass", category: " none ", want: "harass"},
		{name: "real category preserved", verdict: "scam", category: "博彩诈骗", want: "博彩诈骗"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeBioAICategory(tt.verdict, tt.category)
			if got != tt.want {
				t.Fatalf("normalizeBioAICategory(%q, %q) = %q, want %q", tt.verdict, tt.category, got, tt.want)
			}
		})
	}
}
