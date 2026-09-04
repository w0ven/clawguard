package bot

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestRegisteredCommandRunsDeletedAccountFilter(t *testing.T) {
	const (
		chatID = int64(-100123)
		userID = int64(807238339)
	)
	db := newModerationProfileMatchMockDB(chatID, userID)
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{
		bot:            botClient,
		sender:         botClient,
		sendLimiter:    NewSendLimiter(),
		logger:         zap.NewNop(),
		queries:        store.New(db),
		verifyBtn:      tele.Btn{Unique: "verify_human"},
		verifyMathBtn:  tele.Btn{Unique: "verify_math"},
		verifyRandBtn:  tele.Btn{Unique: "verify_random"},
		verifyAdminBtn: tele.Btn{Unique: "verify_admin"},
		joinProtector:  newJoinProtector(),
		cleanupBreaker: newTelegramCleanupBreaker(),
	}
	svc.cacheAuthorizedGroup(context.Background(), chatID, true)
	cacheDeletedAccountTestSystemState(svc, false)
	policy := config.DefaultPolicy
	policy.Feedback.Ban.Enabled = false
	svc.policySnapshots.Store(chatID, policy)
	svc.policySnapshotAt.Store(chatID, time.Now())
	svc.registerHandlers()

	update := tele.Update{
		ID: 9201,
		Message: &tele.Message{
			ID:     9201,
			Text:   "/help",
			Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
			Sender: &tele.User{ID: userID, FirstName: "Deleted", LastName: "Account"},
		},
	}
	if err := svc.ProcessUpdate(update); err != nil {
		t.Fatalf("ProcessUpdate() error = %v", err)
	}

	methods := transport.Methods()
	if got := countString(methods, "kickChatMember"); got != 1 {
		t.Fatalf("kickChatMember calls = %d, want 1; methods=%v", got, methods)
	}
	if got := countString(methods, "deleteMessage"); got != 1 {
		t.Fatalf("deleteMessage calls = %d, want 1; methods=%v", got, methods)
	}
	if containsString(methods, "sendMessage") {
		t.Fatalf("registered command reached command handler after moderation; methods=%v", methods)
	}
}

func TestChatMemberDeletedAccountFilterPrecedesUngraduatedInviteKick(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Filter.NewUser.NoInvites = true
	db := newOtherBotMockDB(policy)
	db.trust = &store.UserTrust{
		ChatID:          -1001,
		UserID:          66,
		FirstName:       stringPtr("Alice"),
		JoinedAt:        db.now,
		UpdatedAt:       db.now,
		StatusChangedAt: db.now,
		Status:          "new",
		Score:           0.5,
	}
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{
		logger:         zap.NewNop(),
		queries:        store.New(db),
		bot:            botClient,
		sender:         botClient,
		sendLimiter:    NewSendLimiter(),
		joinProtector:  newJoinProtector(),
		cleanupBreaker: newTelegramCleanupBreaker(),
	}
	memberUpdate := &tele.ChatMemberUpdate{
		Chat:          &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
		Sender:        &tele.User{ID: 66, FirstName: "Alice"},
		OldChatMember: &tele.ChatMember{User: &tele.User{ID: 77, FirstName: "Deleted", LastName: "Account"}, Role: tele.Left},
		NewChatMember: &tele.ChatMember{User: &tele.User{ID: 77, FirstName: "Deleted", LastName: "Account"}, Role: tele.Member},
	}
	ctx := botClient.NewContext(tele.Update{ID: 9202, ChatMember: memberUpdate})

	if err := svc.handleChatMemberUpdate(ctx); err != nil {
		t.Fatalf("handleChatMemberUpdate() error = %v", err)
	}
	methods := transport.Methods()
	if got := countString(methods, "kickChatMember"); got != 1 {
		t.Fatalf("kickChatMember calls = %d, want permanent ban once; methods=%v", got, methods)
	}
	if got := countString(methods, "unbanChatMember"); got != 0 {
		t.Fatalf("unbanChatMember calls = %d, want 0; deleted-account ban was downgraded to kick", got)
	}
}

func TestChatMemberDeletedAccountFilterSkipsUnauthorizedGroup(t *testing.T) {
	const chatID = int64(-100404)
	policy := config.DefaultPolicy
	db := newOtherBotMockDB(policy)
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient}
	svc.authorizedGroups.Store(chatID, false)
	svc.authorizedGroupAt.Store(chatID, time.Now())
	memberUpdate := &tele.ChatMemberUpdate{
		Chat:          &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		Sender:        &tele.User{ID: 66, FirstName: "Alice"},
		OldChatMember: &tele.ChatMember{User: &tele.User{ID: 77, FirstName: "Deleted", LastName: "Account"}, Role: tele.Left},
		NewChatMember: &tele.ChatMember{User: &tele.User{ID: 77, FirstName: "Deleted", LastName: "Account"}, Role: tele.Member},
	}

	if err := svc.handleChatMemberUpdate(botClient.NewContext(tele.Update{ID: 9204, ChatMember: memberUpdate})); err != nil {
		t.Fatalf("handleChatMemberUpdate() error = %v", err)
	}
	methods := transport.Methods()
	if containsString(methods, "kickChatMember") || containsString(methods, "unbanChatMember") {
		t.Fatalf("unauthorized group received moderation actions; methods=%v", methods)
	}
}

func TestDeletedAccountMessageBanFailureAllowsImmediateRetry(t *testing.T) {
	const (
		chatID = int64(-100123)
		userID = int64(807238339)
	)
	db := newModerationProfileMatchMockDB(chatID, userID)
	botClient, transport := newMockTelegramBot(t, "")
	transport.mu.Lock()
	transport.failBanUserID = userID
	transport.mu.Unlock()
	svc := &Service{bot: botClient, logger: zap.NewNop(), queries: store.New(db)}
	cacheDeletedAccountTestSystemState(svc, false)
	msg := &tele.Message{
		ID:     9203,
		Text:   "hello",
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: userID, FirstName: "Deleted", LastName: "Account"},
	}

	if handled, err := svc.applyDeletedAccountMessageFilter(context.Background(), msg, config.DefaultPolicy, false); err == nil || !handled {
		t.Fatalf("first call handled=%t err=%v, want handled Telegram ban failure", handled, err)
	}
	transport.mu.Lock()
	transport.failBanUserID = 0
	transport.mu.Unlock()
	if handled, err := svc.applyDeletedAccountMessageFilter(context.Background(), msg, config.DefaultPolicy, false); err != nil || !handled {
		t.Fatalf("retry handled=%t err=%v, want successful immediate retry", handled, err)
	}
	if got := countString(transport.Methods(), "kickChatMember"); got != 2 {
		t.Fatalf("kickChatMember calls = %d, want 2; failed attempt retained dedupe lock", got)
	}
}

func TestDeletedAccountJoinBanFailureAllowsImmediateRetry(t *testing.T) {
	const (
		chatID = int64(-100123)
		userID = int64(7774746654)
	)
	db := newModerationProfileMatchMockDB(chatID, userID)
	botClient, transport := newMockTelegramBot(t, "")
	transport.mu.Lock()
	transport.failBanUserID = userID
	transport.mu.Unlock()
	svc := &Service{bot: botClient, logger: zap.NewNop(), queries: store.New(db)}
	cacheDeletedAccountTestSystemState(svc, false)
	chat := &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}
	user := &tele.User{ID: userID, FirstName: "Deleted", LastName: "Account"}

	if handled, err := svc.handleDeletedAccountJoin(context.Background(), chat, user, config.DefaultPolicy, false); err == nil || !handled {
		t.Fatalf("first call handled=%t err=%v, want handled Telegram ban failure", handled, err)
	}
	transport.mu.Lock()
	transport.failBanUserID = 0
	transport.mu.Unlock()
	if handled, err := svc.handleDeletedAccountJoin(context.Background(), chat, user, config.DefaultPolicy, false); err != nil || !handled {
		t.Fatalf("retry handled=%t err=%v, want successful immediate retry", handled, err)
	}
	if got := countString(transport.Methods(), "kickChatMember"); got != 2 {
		t.Fatalf("kickChatMember calls = %d, want 2; failed attempt retained dedupe lock", got)
	}
}

func TestActionDedupeRollbackReleasesRedisAndLocalOwnership(t *testing.T) {
	server, client := newJoinProtectionTestRedis(t)
	svc := &Service{redis: client, logger: zap.NewNop()}
	const (
		chatID = int64(-100123)
		userID = int64(807238339)
	)
	key := "user_action:-100123:807238339"

	rollback, ok := svc.acquireUserActionLock(chatID, userID)
	if !ok {
		t.Fatal("first acquireUserActionLock() = false")
	}
	rollback()
	if server.Exists(key) {
		t.Fatalf("Redis key %q still exists after rollback", key)
	}
	if _, loaded := svc.userActionLocks.Load(key); loaded {
		t.Fatalf("local key %q still exists after rollback", key)
	}
	if _, ok := svc.acquireUserActionLock(chatID, userID); !ok {
		t.Fatal("lock could not be reacquired immediately after rollback")
	}
}
