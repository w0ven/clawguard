package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

type otherBotMockDB struct {
	mu         sync.Mutex
	now        time.Time
	policy     config.GuardPolicy
	trust      *store.UserTrust
	audits     int
	auditLog   []store.InsertAuditEntryParams
	violations []store.InsertViolationParams
	noTrust    bool
}

func newOtherBotMockDB(policy config.GuardPolicy) *otherBotMockDB {
	return &otherBotMockDB{now: time.Date(2026, 5, 21, 8, 0, 0, 0, time.UTC), policy: policy}
}

func (db *otherBotMockDB) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "INSERT INTO config_audit") {
		db.mu.Lock()
		db.audits++
		db.mu.Unlock()
		return pgconn.NewCommandTag("INSERT 1"), nil
	}
	return pgconn.CommandTag{}, fmt.Errorf("unexpected Exec: %s", query)
}

func (db *otherBotMockDB) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected Query: %s", query)
}

func (db *otherBotMockDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	switch {
	case strings.Contains(query, "FROM global_config"):
		return mockScanRow(int32(1), []byte(`{}`), db.now)
	case strings.Contains(query, "FROM groups"):
		raw, _ := json.Marshal(db.policy)
		return mockScanRow(int64(1), args[0].(int64), "group", "supergroup", int32(0), true, db.now, raw)
	case strings.Contains(query, "FROM authorized_groups"):
		return mockScanRow(args[0].(int64), "group", db.now, (*int64)(nil), true, "")
	case strings.Contains(query, "FROM admins"):
		return mockErrorRow{err: pgx.ErrNoRows}
	case strings.Contains(query, "FROM system_state"):
		return mockScanRow(int32(1), false, false, false, "", db.now, (*int64)(nil))
	case strings.Contains(query, "FROM user_trust"):
		db.mu.Lock()
		defer db.mu.Unlock()
		if db.noTrust || db.trust == nil {
			return mockErrorRow{err: pgx.ErrNoRows}
		}
		return scanTrustRow(*db.trust)
	case strings.Contains(query, "AND status = 'archived'"):
		db.mu.Lock()
		defer db.mu.Unlock()
		if db.trust == nil || db.trust.Status != "archived" {
			return mockErrorRow{err: pgx.ErrNoRows}
		}
		notes := ""
		if db.trust.Notes != nil {
			notes = *db.trust.Notes
		}
		notes += " [reactivated mock]"
		db.trust.Status = "new"
		db.trust.StatusChangedAt = db.now
		db.trust.UpdatedAt = db.now
		db.trust.Notes = &notes
		return scanTrustRow(*db.trust)
	case strings.Contains(query, "INSERT INTO user_trust"):
		trust := store.UserTrust{
			ChatID:          args[0].(int64),
			UserID:          args[1].(int64),
			Username:        args[2].(*string),
			FirstName:       args[3].(*string),
			LastName:        args[4].(*string),
			JoinedAt:        args[5].(time.Time),
			UpdatedAt:       db.now,
			StatusChangedAt: db.now,
			Status:          args[6].(string),
			Score:           args[7].(float64),
			MessagesChecked: args[8].(int32),
			MessagesClean:   args[9].(int32),
			GraduatedAt:     args[10].(*time.Time),
			BannedAt:        args[11].(*time.Time),
			BannedReason:    args[12].([]byte),
			Notes:           args[13].(*string),
			IsBot:           args[14].(bool),
		}
		db.mu.Lock()
		db.trust = &trust
		db.noTrust = false
		db.mu.Unlock()
		return scanTrustRow(trust)
	case strings.Contains(query, "SET status = $3"):
		db.mu.Lock()
		if db.trust == nil {
			db.mu.Unlock()
			return mockErrorRow{err: errors.New("missing trust")}
		}
		db.trust.Status = args[2].(string)
		db.trust.Score = args[3].(float64)
		db.trust.GraduatedAt = args[4].(*time.Time)
		db.trust.BannedAt = args[5].(*time.Time)
		db.trust.BannedReason = args[6].([]byte)
		db.trust.Notes = args[7].(*string)
		trust := *db.trust
		db.mu.Unlock()
		return scanTrustRow(trust)
	case strings.Contains(query, "SET is_bot = $3"):
		db.mu.Lock()
		if db.trust == nil {
			db.mu.Unlock()
			return mockErrorRow{err: errors.New("missing trust")}
		}
		db.trust.IsBot = args[2].(bool)
		trust := *db.trust
		db.mu.Unlock()
		return scanTrustRow(trust)
	case strings.Contains(query, "SET messages_checked = messages_checked"):
		db.mu.Lock()
		if db.trust == nil {
			db.mu.Unlock()
			return mockErrorRow{err: errors.New("missing trust")}
		}
		db.trust.MessagesChecked += args[2].(int32)
		db.trust.MessagesClean += args[3].(int32)
		db.trust.Score = args[4].(float64)
		db.trust.UpdatedAt = db.now
		trust := *db.trust
		db.mu.Unlock()
		return scanTrustRow(trust)
	case strings.Contains(query, "SET messages_clean = 0"):
		db.mu.Lock()
		if db.trust == nil {
			db.mu.Unlock()
			return mockErrorRow{err: errors.New("missing trust")}
		}
		db.trust.MessagesClean = 0
		db.trust.Score = args[2].(float64)
		db.trust.UpdatedAt = db.now
		trust := *db.trust
		db.mu.Unlock()
		return scanTrustRow(trust)
	case strings.Contains(query, "INSERT INTO config_audit"):
		params := store.InsertAuditEntryParams{
			Scope:   args[0].(string),
			ChatID:  args[1].(*int64),
			AdminID: args[2].(int64),
			Action:  args[3].(string),
			Before:  args[4].([]byte),
			After:   args[5].([]byte),
			Diff:    args[6].(*string),
		}
		db.mu.Lock()
		db.auditLog = append(db.auditLog, params)
		db.audits++
		db.mu.Unlock()
		createdAt := db.now.Add(6 * time.Minute)
		return mockScanRow(int64(len(db.auditLog)), params.Scope, params.ChatID, params.AdminID, params.Action, params.Before, params.After, params.Diff, createdAt)
	case strings.Contains(query, "INSERT INTO violations"):
		params := store.InsertViolationParams{
			ChatID:      args[0].(int64),
			UserID:      args[1].(int64),
			Username:    args[2].(*string),
			Rule:        args[3].(string),
			Matched:     args[4].(*string),
			Action:      args[5].(string),
			MessageText: args[6].(*string),
		}
		db.mu.Lock()
		db.violations = append(db.violations, params)
		id := int64(len(db.violations))
		db.mu.Unlock()
		return mockScanRow(id, params.ChatID, params.UserID, params.Username, params.Rule, params.Matched, params.Action, params.MessageText, db.now)
	default:
		return mockErrorRow{err: fmt.Errorf("unexpected query: %s", query)}
	}
}

func scanTrustRow(trust store.UserTrust) pgx.Row {
	return mockScanRow(
		trust.ChatID, trust.UserID, trust.Username, trust.FirstName, trust.LastName,
		trust.JoinedAt, trust.UpdatedAt, trust.StatusChangedAt, trust.Status, trust.Score,
		trust.MessagesChecked, trust.MessagesClean, trust.GraduatedAt, trust.BannedAt,
		trust.BannedReason, trust.Notes, trust.IsBot,
	)
}

func TestHandleOtherBotJoinedActions(t *testing.T) {
	tests := []struct {
		name        string
		action      string
		whitelist   []string
		wantStatus  string
		wantMethods []string
	}{
		{name: "whitelist", action: "audit", whitelist: []string{"helperbot"}, wantStatus: "trusted"},
		{name: "audit", action: "audit", wantStatus: "new"},
		{name: "kick", action: "kick", wantStatus: "banned", wantMethods: []string{"kickChatMember", "unbanChatMember"}},
		{name: "ban", action: "ban", wantStatus: "banned", wantMethods: []string{"kickChatMember"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := config.DefaultPolicy
			policy.Filter.OtherBotsAction = tt.action
			policy.Filter.BotWhitelist = tt.whitelist
			db := newOtherBotMockDB(policy)
			botClient, transport := newMockTelegramBot(t, "")
			svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
			update := &tele.ChatMemberUpdate{
				Chat:          &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
				Sender:        &tele.User{ID: 66, FirstName: "Alice"},
				OldChatMember: &tele.ChatMember{User: &tele.User{ID: 77, IsBot: true, Username: "helperbot"}, Role: tele.Left},
				NewChatMember: &tele.ChatMember{User: &tele.User{ID: 77, IsBot: true, Username: "helperbot"}, Role: tele.Member},
			}
			if err := svc.handleOtherBotJoined(update, update.NewChatMember.User); err != nil {
				t.Fatalf("handleOtherBotJoined returned error: %v", err)
			}
			if db.trust == nil || db.trust.Status != tt.wantStatus || !db.trust.IsBot {
				t.Fatalf("trust = %+v, want status=%s is_bot=true", db.trust, tt.wantStatus)
			}
			if tt.name == "audit" {
				inviter := parseBotInviterFromNotes(db.trust.Notes)
				if inviter == nil || inviter.ID != 66 {
					t.Fatalf("inviter notes = %v, want user 66", db.trust.Notes)
				}
			}
			if tt.name == "whitelist" && parseBotInviterFromNotes(db.trust.Notes) != nil {
				t.Fatalf("whitelisted bot should not record inviter for liability, notes=%v", db.trust.Notes)
			}
			methods := transport.Methods()
			for _, want := range tt.wantMethods {
				if !containsString(methods, want) {
					t.Fatalf("methods = %v, want %s", methods, want)
				}
			}
		})
	}
}

func TestHandleUngraduatedInviteChatMemberKicksBotInvitedByNewUser(t *testing.T) {
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
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	update := &tele.ChatMemberUpdate{
		Chat:          &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
		Sender:        &tele.User{ID: 66, FirstName: "Alice"},
		OldChatMember: &tele.ChatMember{User: &tele.User{ID: 77, IsBot: true, Username: "badbot"}, Role: tele.Left},
		NewChatMember: &tele.ChatMember{User: &tele.User{ID: 77, IsBot: true, Username: "badbot"}, Role: tele.Member},
	}

	if !svc.handleUngraduatedInviteChatMember(context.Background(), update, update.NewChatMember.User, policy) {
		t.Fatal("handleUngraduatedInviteChatMember() = false, want true")
	}
	methods := transport.Methods()
	if got := countString(methods, "kickChatMember"); got != 1 {
		t.Fatalf("kickChatMember calls = %d, want 1; methods=%v", got, methods)
	}
	if got := countString(methods, "unbanChatMember"); got != 1 {
		t.Fatalf("unbanChatMember calls = %d, want 1; methods=%v", got, methods)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.trust == nil || db.trust.UserID != 77 || db.trust.Status != "banned" || !db.trust.IsBot {
		t.Fatalf("bot trust = %+v, want invited bot banned", db.trust)
	}
	if len(db.violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(db.violations))
	}
	got := db.violations[0]
	if got.UserID != 66 || got.Rule != ungraduatedInviteRule || got.Action != "kick" {
		t.Fatalf("violation = %+v, want inviter no_invites kick", got)
	}
}

func TestHandleUngraduatedInviteChatMemberSkipsWhenDisabledOrTrusted(t *testing.T) {
	tests := []struct {
		name          string
		noInvites     bool
		inviterStatus string
	}{
		{name: "disabled", noInvites: false, inviterStatus: "new"},
		{name: "trusted", noInvites: true, inviterStatus: "trusted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := config.DefaultPolicy
			policy.Filter.NewUser.NoInvites = tt.noInvites
			db := newOtherBotMockDB(policy)
			db.trust = &store.UserTrust{
				ChatID:          -1001,
				UserID:          66,
				JoinedAt:        db.now,
				UpdatedAt:       db.now,
				StatusChangedAt: db.now,
				Status:          tt.inviterStatus,
				Score:           0.9,
			}
			botClient, transport := newMockTelegramBot(t, "")
			svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
			update := &tele.ChatMemberUpdate{
				Chat:          &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
				Sender:        &tele.User{ID: 66, FirstName: "Alice"},
				OldChatMember: &tele.ChatMember{User: &tele.User{ID: 77, IsBot: true, Username: "goodbot"}, Role: tele.Left},
				NewChatMember: &tele.ChatMember{User: &tele.User{ID: 77, IsBot: true, Username: "goodbot"}, Role: tele.Member},
			}

			if svc.handleUngraduatedInviteChatMember(context.Background(), update, update.NewChatMember.User, policy) {
				t.Fatal("handleUngraduatedInviteChatMember() = true, want false")
			}
			methods := transport.Methods()
			if containsString(methods, "kickChatMember") || containsString(methods, "deleteMessage") {
				t.Fatalf("methods = %v, want no invite restriction actions", methods)
			}
			if len(db.violations) != 0 {
				t.Fatalf("violations = %+v, want none", db.violations)
			}
		})
	}
}

func TestHandleUngraduatedInviteServiceMessageDeletesAndKicks(t *testing.T) {
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
		Status:          "suspicious",
		Score:           0.2,
	}
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	msg := &tele.Message{
		ID:          123,
		Chat:        &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
		Sender:      &tele.User{ID: 66, FirstName: "Alice"},
		UsersJoined: []tele.User{{ID: 77, IsBot: true, Username: "badbot"}},
	}

	if !svc.handleUngraduatedInviteServiceMessage(context.Background(), msg, policy) {
		t.Fatal("handleUngraduatedInviteServiceMessage() = false, want true")
	}
	methods := transport.Methods()
	if !containsString(methods, "deleteMessage") {
		t.Fatalf("methods = %v, want deleteMessage", methods)
	}
	if got := countString(methods, "kickChatMember"); got != 1 {
		t.Fatalf("kickChatMember calls = %d, want 1; methods=%v", got, methods)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(db.violations))
	}
	got := db.violations[0]
	if got.UserID != 66 || got.Rule != ungraduatedInviteRule || got.Action != "delete_kick" {
		t.Fatalf("violation = %+v, want inviter no_invites delete_kick", got)
	}
}

func TestHandleUngraduatedInviteReactivatesArchivedInviter(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Filter.NewUser.NoInvites = true
	db := newOtherBotMockDB(policy)
	db.trust = &store.UserTrust{
		ChatID:          -1001,
		UserID:          66,
		FirstName:       stringPtr("Alice"),
		JoinedAt:        db.now.Add(-48 * time.Hour),
		UpdatedAt:       db.now,
		StatusChangedAt: db.now,
		Status:          "archived",
		Score:           0.5,
		Notes:           stringPtr("left earlier"),
	}
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	update := &tele.ChatMemberUpdate{
		Chat:          &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
		Sender:        &tele.User{ID: 66, FirstName: "Alice"},
		OldChatMember: &tele.ChatMember{User: &tele.User{ID: 88, FirstName: "Bob"}, Role: tele.Left},
		NewChatMember: &tele.ChatMember{User: &tele.User{ID: 88, FirstName: "Bob"}, Role: tele.Member},
	}

	if !svc.handleUngraduatedInviteChatMember(context.Background(), update, update.NewChatMember.User, policy) {
		t.Fatal("handleUngraduatedInviteChatMember() = false, want true")
	}
	if got := countString(transport.Methods(), "kickChatMember"); got != 1 {
		t.Fatalf("kickChatMember calls = %d, want 1; methods=%v", got, transport.Methods())
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.trust == nil || db.trust.UserID != 66 || db.trust.Status != "new" {
		t.Fatalf("inviter trust = %+v, want archived reactivated to new", db.trust)
	}
	if len(db.violations) != 1 || db.violations[0].UserID != 66 {
		t.Fatalf("violations = %+v, want inviter violation", db.violations)
	}
}

func TestEnsureUserTrustCreatesUnknownBotAsNew(t *testing.T) {
	policy := config.DefaultPolicy
	db := newOtherBotMockDB(policy)
	db.noTrust = true
	botClient, _ := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	msg := &tele.Message{Chat: &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 77, IsBot: true, Username: "unknownbot"}}

	trust, err := svc.ensureUserTrust(context.Background(), msg)
	if err != nil {
		t.Fatalf("ensureUserTrust returned error: %v", err)
	}
	if trust.Status != "new" || !trust.IsBot {
		t.Fatalf("trust = %+v, want new bot", trust)
	}
}

func TestHandleSenderChatMessageRespectsDisabledPolicy(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Filter.BanSenderChats = false
	db := newOtherBotMockDB(policy)
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	msg := &tele.Message{
		ID:         123,
		Text:       "channel message",
		Chat:       &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
		SenderChat: &tele.Chat{ID: -2002, Title: "Spam Channel", Username: "spam_channel", Type: tele.ChatChannel},
	}

	handled, err := svc.handleSenderChatMessage(context.Background(), msg, policy)
	if err != nil {
		t.Fatalf("handleSenderChatMessage() error = %v", err)
	}
	if handled {
		t.Fatal("handleSenderChatMessage() handled = true, want false")
	}
	methods := transport.Methods()
	if containsString(methods, "deleteMessage") || containsString(methods, "banChatSenderChat") {
		t.Fatalf("telegram methods = %v, want no sender_chat action", methods)
	}
	if len(db.violations) != 0 {
		t.Fatalf("violations = %+v, want none", db.violations)
	}
}

func TestHandleSenderChatMessageDeletesBansAndRecords(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Filter.BanSenderChats = true
	db := newOtherBotMockDB(policy)
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	msg := &tele.Message{
		ID:         123,
		Text:       "channel message",
		Chat:       &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
		SenderChat: &tele.Chat{ID: -2002, Title: "Spam Channel", Username: "spam_channel", Type: tele.ChatChannel},
	}

	handled, err := svc.handleSenderChatMessage(context.Background(), msg, policy)
	if err != nil {
		t.Fatalf("handleSenderChatMessage() error = %v", err)
	}
	if !handled {
		t.Fatal("handleSenderChatMessage() handled = false, want true")
	}
	methods := transport.Methods()
	if !containsString(methods, "deleteMessage") {
		t.Fatalf("methods = %v, want deleteMessage", methods)
	}
	if !containsString(methods, "banChatSenderChat") {
		t.Fatalf("methods = %v, want banChatSenderChat", methods)
	}
	bodies := strings.Join(transport.RequestBodies(), "\n")
	if !strings.Contains(bodies, `"sender_chat_id":"-2002"`) {
		t.Fatalf("request bodies missing sender_chat_id: %s", bodies)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(db.violations))
	}
	got := db.violations[0]
	if got.UserID != -2002 || got.Rule != "filter_sender_chat" || got.Action != "delete_ban" {
		t.Fatalf("violation = %+v, want sender_chat delete_ban", got)
	}
	if got.Username == nil || *got.Username != "spam_channel" {
		t.Fatalf("violation username = %v, want spam_channel", got.Username)
	}
	if got.Matched == nil || *got.Matched != "@spam_channel" {
		t.Fatalf("violation matched = %v, want @spam_channel", got.Matched)
	}
}

func TestHandleSenderChatMessageBansEvenWhenFakeSenderPresent(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Filter.BanSenderChats = true
	db := newOtherBotMockDB(policy)
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	msg := &tele.Message{
		ID:         321,
		Text:       "channel message",
		Chat:       &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
		Sender:     &tele.User{ID: 1087968824, IsBot: true, Username: "GroupAnonymousBot"},
		SenderChat: &tele.Chat{ID: -2002, Title: "Spam Channel", Username: "spam_channel", Type: tele.ChatChannel},
	}

	handled, err := svc.handleSenderChatMessage(context.Background(), msg, policy)
	if err != nil {
		t.Fatalf("handleSenderChatMessage() error = %v", err)
	}
	if !handled {
		t.Fatal("handleSenderChatMessage() handled = false, want true")
	}
	methods := transport.Methods()
	if !containsString(methods, "deleteMessage") || !containsString(methods, "banChatSenderChat") {
		t.Fatalf("methods = %v, want delete+banChatSenderChat", methods)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(db.violations))
	}
}

func TestHandleSenderChatMessageSkipsAutomaticForward(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Filter.BanSenderChats = true
	db := newOtherBotMockDB(policy)
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	msg := &tele.Message{
		ID:               999,
		Text:             "linked channel post",
		Chat:             &tele.Chat{ID: -1001, Title: "discussion", Type: tele.ChatSuperGroup},
		SenderChat:       &tele.Chat{ID: -2003, Title: "Linked Channel", Username: "linked", Type: tele.ChatChannel},
		AutomaticForward: true,
	}

	handled, err := svc.handleSenderChatMessage(context.Background(), msg, policy)
	if err != nil {
		t.Fatalf("handleSenderChatMessage() error = %v", err)
	}
	if handled {
		t.Fatal("automatic_forward should not be handled")
	}
	if containsString(transport.Methods(), "banChatSenderChat") {
		t.Fatalf("methods = %v, want no banChatSenderChat for automatic_forward", transport.Methods())
	}
	if len(db.violations) != 0 {
		t.Fatalf("violations = %+v, want none", db.violations)
	}
}

func TestMaybeBanBotInviterAfterViolationBansAndAnnounces(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Filter.BanBotInviterOnViolation = true
	db := newOtherBotMockDB(policy)
	db.trust = &store.UserTrust{
		ChatID:          -1001,
		UserID:          77,
		Username:        stringPtr("badbot"),
		JoinedAt:        db.now,
		UpdatedAt:       db.now,
		StatusChangedAt: db.now,
		Status:          "new",
		Score:           0.5,
		Notes:           stringPtr(botTrustNotes("audit: other bot", &tele.User{ID: 66, FirstName: "Alice"})),
		IsBot:           true,
	}
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	msg := &tele.Message{ID: 123, Chat: &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 77, IsBot: true, Username: "badbot"}}

	svc.maybeBanBotInviterAfterViolation(context.Background(), msg, policy, "ban", "spam")

	methods := transport.Methods()
	if got := countString(methods, "kickChatMember"); got != 1 {
		t.Fatalf("kickChatMember calls = %d, want 1; methods=%v", got, methods)
	}
	if !containsString(methods, "sendMessage") {
		t.Fatalf("methods = %v, want sendMessage announcement", methods)
	}
	bodies := strings.Join(transport.RequestBodies(), "\n")
	if !strings.Contains(bodies, "邀请违规 Bot") {
		t.Fatalf("announcement bodies missing reason: %s", bodies)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	found := false
	for _, entry := range db.auditLog {
		if entry.Action == "ban_bot_inviter" && strings.Contains(string(entry.After), `"outcome":"success"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit log = %+v, want successful ban_bot_inviter", db.auditLog)
	}
}

func TestApplyAIActionBotBanBansInviterBeforeTrustReset(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Filter.BanBotInviterOnViolation = true
	db := newOtherBotMockDB(policy)
	db.trust = &store.UserTrust{
		ChatID:          -1001,
		UserID:          77,
		Username:        stringPtr("badbot"),
		JoinedAt:        db.now,
		UpdatedAt:       db.now,
		StatusChangedAt: db.now,
		Status:          "new",
		Score:           0.5,
		MessagesChecked: 3,
		MessagesClean:   3,
		Notes:           stringPtr(botTrustNotes("audit: other bot", &tele.User{ID: 66, FirstName: "Alice"})),
		IsBot:           true,
	}
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	msg := &tele.Message{
		ID:     123,
		Text:   "spam",
		Chat:   &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: 77, IsBot: true, Username: "badbot"},
	}
	output := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "bot spam"}, Model: "message-model"}

	if err := svc.applyAIAction(context.Background(), msg, policy, *db.trust, output, "ban", false); err != nil {
		t.Fatalf("applyAIAction() error = %v", err)
	}

	methods := transport.Methods()
	if got := countString(methods, "kickChatMember"); got != 2 {
		t.Fatalf("kickChatMember calls = %d, want 2 for bot and inviter; methods=%v bodies=%v", got, methods, transport.RequestBodies())
	}
	if !containsString(methods, "deleteMessage") {
		t.Fatalf("methods = %v, want bot message delete", methods)
	}
	if !containsString(methods, "sendMessage") {
		t.Fatalf("methods = %v, want inviter announcement", methods)
	}
	bodies := strings.Join(transport.RequestBodies(), "\n")
	if !strings.Contains(bodies, "邀请违规 Bot") {
		t.Fatalf("announcement bodies missing inviter reason: %s", bodies)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.trust == nil || db.trust.Status != "banned" || db.trust.Score != 0 {
		t.Fatalf("trust = %+v, want banned score 0", db.trust)
	}
	if db.trust.Notes == nil || *db.trust.Notes != "bot spam" {
		t.Fatalf("trust notes = %v, want reset reason to prove liability ran before reset", db.trust.Notes)
	}
	found := false
	for _, entry := range db.auditLog {
		if entry.Action == "ban_bot_inviter" && strings.Contains(string(entry.After), `"outcome":"success"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit log = %+v, want successful ban_bot_inviter", db.auditLog)
	}
}

func TestApplyAIActionNoneDoesNotBanBotInviter(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Filter.BanBotInviterOnViolation = true
	db := newOtherBotMockDB(policy)
	db.trust = &store.UserTrust{
		ChatID:          -1001,
		UserID:          77,
		Username:        stringPtr("goodbot"),
		JoinedAt:        db.now,
		UpdatedAt:       db.now,
		StatusChangedAt: db.now,
		Status:          "new",
		Score:           0.5,
		MessagesChecked: 3,
		MessagesClean:   3,
		Notes:           stringPtr(botTrustNotes("audit: other bot", &tele.User{ID: 66, FirstName: "Alice"})),
		IsBot:           true,
	}
	botClient, transport := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	msg := &tele.Message{
		ID:     123,
		Text:   "clean",
		Chat:   &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: 77, IsBot: true, Username: "goodbot"},
	}
	output := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "clean", Confidence: 0.99, Category: "clean", Reason: "clean"}, Model: "message-model"}

	if err := svc.applyAIAction(context.Background(), msg, policy, *db.trust, output, "none", false); err != nil {
		t.Fatalf("applyAIAction() error = %v", err)
	}

	methods := transport.Methods()
	if got := countString(methods, "kickChatMember"); got != 0 {
		t.Fatalf("kickChatMember calls = %d, want 0; methods=%v", got, methods)
	}
	if containsString(methods, "sendMessage") {
		t.Fatalf("methods = %v, did not expect inviter announcement", methods)
	}
}

func TestMaybeBanBotInviterAfterViolationSkippedInvalidInviter(t *testing.T) {
	tests := []struct {
		name    string
		inviter *tele.User
	}{
		{name: "missing"},
		{name: "bot", inviter: &tele.User{ID: 66, IsBot: true, Username: "otherbot"}},
		{name: "same as bot", inviter: &tele.User{ID: 77, FirstName: "badbot"}},
		{name: "self bot", inviter: &tele.User{ID: 999, FirstName: "ClawGuard"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := config.DefaultPolicy
			policy.Filter.BanBotInviterOnViolation = true
			db := newOtherBotMockDB(policy)
			notes := "audit: other bot"
			if tt.inviter != nil {
				notes = botTrustNotes("audit: other bot", tt.inviter)
			}
			db.trust = &store.UserTrust{
				ChatID:          -1001,
				UserID:          77,
				JoinedAt:        db.now,
				UpdatedAt:       db.now,
				StatusChangedAt: db.now,
				Status:          "new",
				Score:           0.5,
				Notes:           stringPtr(notes),
				IsBot:           true,
			}
			botClient, transport := newMockTelegramBot(t, "")
			svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
			msg := &tele.Message{ID: 123, Chat: &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 77, IsBot: true, Username: "badbot"}}

			svc.maybeBanBotInviterAfterViolation(context.Background(), msg, policy, "ban", "spam")

			if got := countString(transport.Methods(), "kickChatMember"); got != 0 {
				t.Fatalf("kickChatMember calls = %d, want 0; methods=%v", got, transport.Methods())
			}
			db.mu.Lock()
			defer db.mu.Unlock()
			foundSkipped := false
			for _, entry := range db.auditLog {
				if entry.Action == "ban_bot_inviter" && strings.Contains(string(entry.After), `"outcome":"skipped"`) {
					foundSkipped = true
				}
			}
			if !foundSkipped {
				t.Fatalf("audit log = %+v, want skipped ban_bot_inviter", db.auditLog)
			}
		})
	}
}

func TestMaybeBanBotInviterAfterViolationBanFailureAuditsFailed(t *testing.T) {
	policy := config.DefaultPolicy
	policy.Filter.BanBotInviterOnViolation = true
	db := newOtherBotMockDB(policy)
	db.trust = &store.UserTrust{
		ChatID:          -1001,
		UserID:          77,
		JoinedAt:        db.now,
		UpdatedAt:       db.now,
		StatusChangedAt: db.now,
		Status:          "new",
		Score:           0.5,
		Notes:           stringPtr(botTrustNotes("audit: other bot", &tele.User{ID: 66, FirstName: "Alice"})),
		IsBot:           true,
	}
	botClient, transport := newMockTelegramBot(t, "")
	transport.failBanUserID = 66
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	msg := &tele.Message{ID: 123, Chat: &tele.Chat{ID: -1001, Title: "group", Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 77, IsBot: true, Username: "badbot"}}

	svc.maybeBanBotInviterAfterViolation(context.Background(), msg, policy, "ban", "spam")

	methods := transport.Methods()
	if got := countString(methods, "kickChatMember"); got != 1 {
		t.Fatalf("kickChatMember calls = %d, want 1; methods=%v", got, methods)
	}
	if containsString(methods, "sendMessage") {
		t.Fatalf("methods = %v, did not expect announcement on failed inviter ban", methods)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	foundFailed := false
	for _, entry := range db.auditLog {
		if entry.Action == "ban_bot_inviter" && strings.Contains(string(entry.After), `"outcome":"failed"`) {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Fatalf("audit log = %+v, want failed ban_bot_inviter", db.auditLog)
	}
}

func TestMaybeGraduateUserSkipsBots(t *testing.T) {
	db := newOtherBotMockDB(config.DefaultPolicy)
	botClient, _ := newMockTelegramBot(t, "")
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}
	trust := store.UserTrust{ChatID: -1001, UserID: 77, Status: "new", Score: 0.9, MessagesClean: 99, IsBot: true}

	if err := svc.maybeGraduateUser(context.Background(), trust, config.DefaultPolicy.AI); err != nil {
		t.Fatalf("maybeGraduateUser returned error: %v", err)
	}
	if db.trust != nil {
		t.Fatalf("bot trust should not be updated, got %+v", db.trust)
	}
}

func TestMaybeGraduateUserSuspiciousUsesGraduateAfterMessages(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name                  string
		clean                 int32
		graduateAfterMessages int
		statusAge             time.Duration
		wantStatus            string
		wantGraduated         bool
	}{
		{
			name:                  "below default threshold after thirty days",
			clean:                 4,
			graduateAfterMessages: config.DefaultPolicy.AI.GraduateAfterMessages,
			statusAge:             31 * 24 * time.Hour,
			wantStatus:            "suspicious",
		},
		{
			name:                  "meets default threshold",
			clean:                 5,
			graduateAfterMessages: config.DefaultPolicy.AI.GraduateAfterMessages,
			statusAge:             time.Hour,
			wantStatus:            "trusted",
			wantGraduated:         true,
		},
		{
			name:                  "below custom threshold after thirty days",
			clean:                 6,
			graduateAfterMessages: 7,
			statusAge:             31 * 24 * time.Hour,
			wantStatus:            "suspicious",
		},
		{
			name:                  "meets custom threshold",
			clean:                 7,
			graduateAfterMessages: 7,
			statusAge:             time.Hour,
			wantStatus:            "trusted",
			wantGraduated:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := config.DefaultPolicy
			policy.AI.GraduateAfterMessages = tt.graduateAfterMessages
			db := newOtherBotMockDB(policy)
			trust := store.UserTrust{
				ChatID:          -1001,
				UserID:          77,
				FirstName:       stringPtr("Bob"),
				JoinedAt:        now.Add(-60 * 24 * time.Hour),
				UpdatedAt:       now,
				StatusChangedAt: now.Add(-tt.statusAge),
				Status:          "suspicious",
				Score:           0.3,
				MessagesClean:   tt.clean,
			}
			db.trust = &trust
			botClient, _ := newMockTelegramBot(t, "")
			svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient, sender: botClient, sendLimiter: NewSendLimiter()}

			if err := svc.maybeGraduateUser(context.Background(), trust, policy.AI); err != nil {
				t.Fatalf("maybeGraduateUser returned error: %v", err)
			}

			db.mu.Lock()
			got := *db.trust
			db.mu.Unlock()
			gotGraduated := got.GraduatedAt != nil
			if got.Status != tt.wantStatus || gotGraduated != tt.wantGraduated {
				t.Fatalf("trust = %+v, want status=%s graduated=%v", got, tt.wantStatus, tt.wantGraduated)
			}
			if tt.wantGraduated && got.Score < 0.95 {
				t.Fatalf("score = %v, want at least 0.95 after graduation", got.Score)
			}
		})
	}
}
