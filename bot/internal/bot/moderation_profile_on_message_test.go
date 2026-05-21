package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func TestAsyncProfileCheck_ProfileOnMessageHit_RecordsAIDecisionAndBansTrust(t *testing.T) {
	const (
		chatID = -100123
		userID = 42
	)

	botClient, transport := newMockTelegramBot(t, "contact spam seller")
	db := newModerationProfileMatchMockDB(chatID, userID)

	svc := &Service{
		logger:  zap.NewNop(),
		queries: store.New(db),
		bot:     botClient,
	}

	policy := config.DefaultPolicy
	policy.AI.Enabled = true
	policy.AI.CheckProfileOnMessage = true
	policy.AI.ProfileOnMessageMode = "keyword"
	policy.AI.BioCacheTTLMinutes = 0
	policy.Verify.ProfileBlacklist = []string{"spam"}
	policy.Feedback.Ban = config.ActionFeedback{}

	msg := &tele.Message{
		ID:     1001,
		Text:   "trigger message",
		Chat:   &tele.Chat{ID: chatID, Title: "test-group"},
		Sender: &tele.User{ID: userID, Username: "new_user"},
	}

	svc.asyncProfileCheck(msg, policy)

	deadline := time.Now().Add(2 * time.Second)
	var decisions []store.InsertAIDecisionParams
	var violations []store.InsertViolationParams
	var trust store.UserTrust
	for {
		db.mu.Lock()
		decisions = append([]store.InsertAIDecisionParams(nil), db.aiDecisions...)
		violations = append([]store.InsertViolationParams(nil), db.violations...)
		trust = db.userTrust
		db.mu.Unlock()

		if len(decisions) == 1 && len(violations) == 1 && trust.Status == "banned" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("async profile check did not finish in time: decisions=%d violations=%d trust=%q", len(decisions), len(violations), trust.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(decisions) != 1 {
		t.Fatalf("ai_decisions inserts = %d, want 1", len(decisions))
	}
	if decisions[0].MessageID != int64(msg.ID) {
		t.Fatalf("ai_decisions message_id = %d, want %d", decisions[0].MessageID, msg.ID)
	}
	if decisions[0].MessageText == nil || *decisions[0].MessageText != msg.Text {
		t.Fatalf("ai_decisions message_text = %v, want %q", decisions[0].MessageText, msg.Text)
	}
	if decisions[0].ActionTaken != "ban" {
		t.Fatalf("ai_decisions action_taken = %q, want %q", decisions[0].ActionTaken, "ban")
	}
	if decisions[0].Model != "profile_check:on_message_keyword" {
		t.Fatalf("ai_decisions model = %q, want %q", decisions[0].Model, "profile_check:on_message_keyword")
	}
	if decisions[0].Scene != "bio" {
		t.Fatalf("ai_decisions scene = %q, want %q", decisions[0].Scene, "bio")
	}

	if len(violations) != 1 {
		t.Fatalf("violations inserts = %d, want 1", len(violations))
	}
	if violations[0].MessageText == nil || *violations[0].MessageText != msg.Text {
		t.Fatalf("violations message_text = %v, want %q", violations[0].MessageText, msg.Text)
	}

	if trust.Status != "banned" {
		t.Fatalf("user_trust status = %q, want %q", trust.Status, "banned")
	}

	methods := transport.Methods()
	for _, want := range []string{"getChat", "kickChatMember", "deleteMessage"} {
		if !containsString(methods, want) {
			t.Fatalf("telegram methods = %v, missing %q", methods, want)
		}
	}
}

func TestModerationDedupe_ProfileViolationWinsOverMessageAI(t *testing.T) {
	const (
		chatID = -100123
		userID = 42
	)

	svc, transport, db, msg, policy := newModerationDedupeTestService(t, chatID, userID)
	profileOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "资料违规"}, Model: "profile-model"}
	if err := svc.handleProfileOnMessageViolation(context.Background(), msg, policy, "资料违规", &profileOutput); err != nil {
		t.Fatalf("handleProfileOnMessageViolation() error = %v", err)
	}

	db.failTrustSideEffects = true
	messageOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "消息违规"}, Model: "message-model"}
	if err := svc.applyAIAction(context.Background(), msg, policy, db.currentTrust(), messageOutput, "ban", false); err != nil {
		t.Fatalf("applyAIAction() error = %v", err)
	}

	assertModerationDedupeCounts(t, transport, 1, 1, 1)
	if got := joinedTelegramBodies(transport); !strings.Contains(got, "资料简介违规：资料违规") {
		t.Fatalf("feedback body = %q, want profile reason", got)
	}
	if got := db.trustSideEffectCalls(); got != 0 {
		t.Fatalf("deduped message AI trust side effects = %d, want 0", got)
	}
}

func TestModerationDedupe_ProfileBanThenMessageAIWarnSkipsTrustSideEffects(t *testing.T) {
	const (
		chatID = -100123
		userID = 42
	)

	svc, transport, db, msg, policy := newModerationDedupeTestService(t, chatID, userID)
	profileOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "资料违规"}, Model: "profile-model"}
	if err := svc.handleProfileOnMessageViolation(context.Background(), msg, policy, "资料违规", &profileOutput); err != nil {
		t.Fatalf("handleProfileOnMessageViolation() error = %v", err)
	}

	db.failTrustSideEffects = true
	messageOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "violence", Confidence: 0.99, Category: "violence", Reason: "消息违规"}, Model: "message-model"}
	if err := svc.applyAIAction(context.Background(), msg, policy, db.currentTrust(), messageOutput, "warn", false); err != nil {
		t.Fatalf("applyAIAction() error = %v", err)
	}

	assertModerationDedupeCounts(t, transport, 1, 1, 1)
	if got := joinedTelegramBodies(transport); !strings.Contains(got, "资料简介违规：资料违规") || strings.Contains(got, "消息违规") {
		t.Fatalf("feedback body = %q, want only profile reason", got)
	}
	if got := db.trustSideEffectCalls(); got != 0 {
		t.Fatalf("deduped message AI warn trust side effects = %d, want 0", got)
	}
}

func TestModerationDedupe_MessageAIWinsOverProfileViolation(t *testing.T) {
	const (
		chatID = -100123
		userID = 42
	)

	svc, transport, db, msg, policy := newModerationDedupeTestService(t, chatID, userID)
	messageOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "消息违规"}, Model: "message-model"}
	if err := svc.applyAIAction(context.Background(), msg, policy, db.currentTrust(), messageOutput, "ban", false); err != nil {
		t.Fatalf("applyAIAction() error = %v", err)
	}

	db.failTrustSideEffects = true
	profileOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "资料违规"}, Model: "profile-model"}
	if err := svc.handleProfileOnMessageViolation(context.Background(), msg, policy, "资料违规", &profileOutput); err != nil {
		t.Fatalf("handleProfileOnMessageViolation() error = %v", err)
	}

	assertModerationDedupeCounts(t, transport, 1, 1, 1)
	if got := joinedTelegramBodies(transport); !strings.Contains(got, "消息违规") {
		t.Fatalf("feedback body = %q, want message reason", got)
	}
	if got := db.trustSideEffectCalls(); got != 0 {
		t.Fatalf("deduped profile trust side effects = %d, want 0", got)
	}
	db.mu.Lock()
	decisions := len(db.aiDecisions)
	db.mu.Unlock()
	if decisions != 1 {
		t.Fatalf("profile ai_decisions inserts = %d, want 1", decisions)
	}
}

func TestModerationDedupe_ProfileAndMessageAIConcurrent(t *testing.T) {
	const (
		chatID = -100123
		userID = 42
	)

	svc, transport, db, msg, policy := newModerationDedupeTestService(t, chatID, userID)
	profileOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "资料违规"}, Model: "profile-model"}
	messageOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "消息违规"}, Model: "message-model"}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		errs <- svc.handleProfileOnMessageViolation(context.Background(), msg, policy, "资料违规", &profileOutput)
	}()
	go func() {
		defer wg.Done()
		<-start
		errs <- svc.applyAIAction(context.Background(), msg, policy, db.currentTrust(), messageOutput, "ban", false)
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent moderation action error = %v", err)
		}
	}

	assertModerationDedupeCounts(t, transport, 1, 1, 1)
}

func TestModerationDedupe_ProfileViolationWinsOverMessageAIWarnMuteDelete(t *testing.T) {
	for _, action := range []string{"warn", "mute", "delete"} {
		t.Run(action, func(t *testing.T) {
			const (
				chatID = -100123
				userID = 42
			)

			svc, transport, db, msg, policy := newModerationDedupeTestService(t, chatID, userID)
			profileOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "资料违规"}, Model: "profile-model"}
			if err := svc.handleProfileOnMessageViolation(context.Background(), msg, policy, "资料违规", &profileOutput); err != nil {
				t.Fatalf("handleProfileOnMessageViolation() error = %v", err)
			}

			messageOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "消息违规"}, Model: "message-model"}
			if err := svc.applyAIAction(context.Background(), msg, policy, db.currentTrust(), messageOutput, action, false); err != nil {
				t.Fatalf("applyAIAction(%q) error = %v", action, err)
			}

			assertModerationDedupeCounts(t, transport, 1, 1, 1)
			if got := joinedTelegramBodies(transport); !strings.Contains(got, "资料简介违规：资料违规") {
				t.Fatalf("feedback body = %q, want profile reason", got)
			}
		})
	}
}

func TestModerationDedupe_MessageAIWarnWinsOverProfileViolation(t *testing.T) {
	const (
		chatID = -100123
		userID = 42
	)

	svc, transport, db, msg, policy := newModerationDedupeTestService(t, chatID, userID)
	messageOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "violence", Confidence: 0.99, Category: "violence", Reason: "消息违规"}, Model: "message-model"}
	if err := svc.applyAIAction(context.Background(), msg, policy, db.currentTrust(), messageOutput, "warn", false); err != nil {
		t.Fatalf("applyAIAction() error = %v", err)
	}

	profileOutput := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "资料违规"}, Model: "profile-model"}
	if err := svc.handleProfileOnMessageViolation(context.Background(), msg, policy, "资料违规", &profileOutput); err != nil {
		t.Fatalf("handleProfileOnMessageViolation() error = %v", err)
	}

	assertModerationDedupeCounts(t, transport, 0, 1, 1)
	if got := joinedTelegramBodies(transport); !strings.Contains(got, "violence") || strings.Contains(got, "资料简介违规") {
		t.Fatalf("feedback body = %q, want only warn reason", got)
	}
}

func TestApplyAIActionSpamBanSendsSingleFeedbackAndRevokesMessages(t *testing.T) {
	const (
		chatID = -100123
		userID = 42
	)

	svc, transport, db, msg, policy := newModerationDedupeTestService(t, chatID, userID)
	output := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "spam"}, Model: "message-model"}
	if err := svc.applyAIAction(context.Background(), msg, policy, db.currentTrust(), output, "ban", false); err != nil {
		t.Fatalf("applyAIAction() error = %v", err)
	}

	assertModerationDedupeCounts(t, transport, 1, 1, 1)
	assertTelegramRequestParam(t, transport, "kickChatMember", "revoke_messages", "true")
}

func TestBanUserRevokesMessages(t *testing.T) {
	const (
		chatID = -100123
		userID = 42
	)

	botClient, transport := newMockTelegramBot(t, "")
	db := newModerationProfileMatchMockDB(chatID, userID)
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient}

	if err := svc.banUser(&tele.Chat{ID: chatID}, &tele.User{ID: userID}); err != nil {
		t.Fatalf("banUser() error = %v", err)
	}

	assertTelegramRequestParam(t, transport, "kickChatMember", "revoke_messages", "true")
}

func TestApplyAIActionWarnSendsOnlyWarnFeedbackAtBanThreshold(t *testing.T) {
	const (
		chatID = -100123
		userID = 42
	)

	svc, transport, db, msg, policy := newModerationDedupeTestService(t, chatID, userID)
	policy.Warnings.Enabled = true
	policy.Warnings.MaxWarns = 1
	policy.Warnings.ActionAtMax = "ban"
	output := ai.CheckOutput{Verdict: ai.Verdict{Verdict: "spam", Confidence: 0.99, Category: "spam", Reason: "spam"}, Model: "message-model"}
	if err := svc.applyAIAction(context.Background(), msg, policy, db.currentTrust(), output, "warn", false); err != nil {
		t.Fatalf("applyAIAction() error = %v", err)
	}

	assertModerationDedupeCounts(t, transport, 1, 1, 1)
	if got := joinedTelegramBodies(transport); !strings.Contains(got, "spam") {
		t.Fatalf("feedback body = %q, want warn reason", got)
	}
	assertTelegramRequestParam(t, transport, "kickChatMember", "revoke_messages", "true")
}

func TestSpamCommandSendsSingleFeedbackAndRevokesMessages(t *testing.T) {
	const (
		chatID  = -100123
		adminID = 7
		userID  = 42
	)

	botClient, transport := newMockTelegramBot(t, "")
	db := newModerationProfileMatchMockDB(chatID, userID)
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient}
	command := &tele.Message{
		ID:     2001,
		Text:   "/spam",
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup, Title: "test-group"},
		Sender: &tele.User{ID: adminID, Username: "admin"},
		ReplyTo: &tele.Message{
			ID:     1001,
			Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup, Title: "test-group"},
			Sender: &tele.User{ID: userID, Username: "new_user", FirstName: "new"},
			Text:   "spam message",
		},
	}

	if err := svc.handleSpamCommand(botClient.NewContext(tele.Update{Message: command})); err != nil {
		t.Fatalf("handleSpamCommand() error = %v", err)
	}

	assertModerationDedupeCounts(t, transport, 1, 1, 1)
	assertTelegramRequestParam(t, transport, "kickChatMember", "revoke_messages", "true")
	if got := joinedTelegramBodies(transport); !strings.Contains(got, "spam") || strings.Contains(got, "已将") {
		t.Fatalf("feedback body = %q, want configured spam feedback only", got)
	}
}

func TestModerationDedupe_RedisUnavailableFallsBackToLocalLocks(t *testing.T) {
	svc := &Service{
		logger: zap.NewNop(),
		redis:  redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Millisecond, ReadTimeout: time.Millisecond, WriteTimeout: time.Millisecond, MaxRetries: 0}),
	}
	t.Cleanup(func() {
		if client, ok := svc.redis.(*redis.Client); ok {
			_ = client.Close()
		}
	})

	if release, ok := svc.acquireUserActionLock(-100123, 42); !ok {
		t.Fatal("first user action lock acquisition failed")
	} else {
		release()
	}
	if _, ok := svc.acquireUserActionLock(-100123, 42); ok {
		t.Fatal("second user action lock acquisition succeeded, want local fallback dedupe")
	}
	if release, ok := svc.acquireMessageDeleteLock(-100123, 1001); !ok {
		t.Fatal("first message delete lock acquisition failed")
	} else {
		release()
	}
	if _, ok := svc.acquireMessageDeleteLock(-100123, 1001); ok {
		t.Fatal("second message delete lock acquisition succeeded, want local fallback dedupe")
	}
}

func newModerationDedupeTestService(t *testing.T, chatID, userID int64) (*Service, *telegramMockTransport, *moderationProfileMatchMockDB, *tele.Message, config.GuardPolicy) {
	t.Helper()
	botClient, transport := newMockTelegramBot(t, "")
	db := newModerationProfileMatchMockDB(chatID, userID)
	svc := &Service{logger: zap.NewNop(), queries: store.New(db), bot: botClient}
	policy := config.DefaultPolicy
	policy.Feedback.Ban = config.ActionFeedback{Enabled: true, Template: "{reason}"}
	policy.Feedback.Warn = config.ActionFeedback{Enabled: true, Template: "{reason}"}
	policy.Feedback.Mute = config.ActionFeedback{Enabled: true, Template: "{reason}"}
	policy.Feedback.DeleteMsg = config.ActionFeedback{Enabled: true, Template: "{reason}"}
	msg := &tele.Message{ID: 1001, Text: "trigger message", Chat: &tele.Chat{ID: chatID, Title: "test-group"}, Sender: &tele.User{ID: userID, Username: "new_user"}}
	return svc, transport, db, msg, policy
}

func assertModerationDedupeCounts(t *testing.T, transport *telegramMockTransport, wantBan, wantDelete, wantFeedback int) {
	t.Helper()
	methods := transport.Methods()
	if got := countString(methods, "kickChatMember"); got != wantBan {
		t.Fatalf("kickChatMember calls = %d, want %d; methods=%v", got, wantBan, methods)
	}
	if got := countString(methods, "deleteMessage"); got != wantDelete {
		t.Fatalf("deleteMessage calls = %d, want %d; methods=%v", got, wantDelete, methods)
	}
	if got := countString(methods, "sendMessage"); got != wantFeedback {
		t.Fatalf("sendMessage calls = %d, want %d; methods=%v", got, wantFeedback, methods)
	}
}

func joinedTelegramBodies(transport *telegramMockTransport) string {
	bodies := transport.RequestBodies()
	decoded := make([]string, 0, len(bodies))
	for _, body := range bodies {
		value, err := url.QueryUnescape(body)
		if err != nil {
			value = body
		}
		decoded = append(decoded, value)
	}
	return strings.Join(decoded, "\n")
}

func assertTelegramRequestParam(t *testing.T, transport *telegramMockTransport, method, key, want string) {
	t.Helper()
	transport.mu.Lock()
	methods := append([]string(nil), transport.methods...)
	bodies := append([]string(nil), transport.bodies...)
	transport.mu.Unlock()

	bodyIndex := 0
	for _, gotMethod := range methods {
		body := ""
		if bodyIndex < len(bodies) {
			body = bodies[bodyIndex]
			bodyIndex++
		}
		if gotMethod != method {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(body), "{") {
			var values map[string]string
			if err := json.Unmarshal([]byte(body), &values); err != nil {
				t.Fatalf("parse %s JSON body %q: %v", method, body, err)
			}
			if got := values[key]; got != want {
				t.Fatalf("%s %s = %q, want %q; body=%q", method, key, got, want, body)
			}
			return
		}
		values, err := url.ParseQuery(body)
		if err != nil {
			t.Fatalf("parse %s query body %q: %v", method, body, err)
		}
		if got := values.Get(key); got != want {
			t.Fatalf("%s %s = %q, want %q; body=%q", method, key, got, want, body)
		}
		return
	}
	t.Fatalf("method %q not called; methods=%v", method, methods)
}

type moderationProfileMatchMockDB struct {
	mu          sync.Mutex
	now         time.Time
	userTrust   store.UserTrust
	systemState store.SystemState
	aiDecisions []store.InsertAIDecisionParams
	violations  []store.InsertViolationParams
	profileLogs []store.InsertProfileCheckLogParams
	warnings    []store.Warning

	failTrustSideEffects bool
	trustSideEffects     int
}

func newModerationProfileMatchMockDB(chatID, userID int64) *moderationProfileMatchMockDB {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	return &moderationProfileMatchMockDB{
		now: now,
		userTrust: store.UserTrust{
			ChatID:          chatID,
			UserID:          userID,
			JoinedAt:        now,
			UpdatedAt:       now,
			StatusChangedAt: now,
			Status:          "new",
			Score:           0.5,
		},
		systemState: store.SystemState{
			ID:        1,
			UpdatedAt: now,
		},
	}
}

func (db *moderationProfileMatchMockDB) currentTrust() store.UserTrust {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.userTrust
}

func (db *moderationProfileMatchMockDB) trustSideEffectCalls() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.trustSideEffects
}

func (db *moderationProfileMatchMockDB) recordTrustSideEffectLocked() pgx.Row {
	if db.failTrustSideEffects {
		db.trustSideEffects++
		return mockErrorRow{err: fmt.Errorf("unexpected trust side effect on deduped action")}
	}
	return nil
}

func (db *moderationProfileMatchMockDB) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "UPDATE warnings") {
		db.mu.Lock()
		defer db.mu.Unlock()
		consumedAt := db.now.Add(4 * time.Minute)
		for i := range db.warnings {
			if db.warnings[i].ChatID == args[0].(int64) && db.warnings[i].UserID == args[1].(int64) && db.warnings[i].ConsumedAt == nil {
				db.warnings[i].ConsumedAt = &consumedAt
			}
		}
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	return pgconn.CommandTag{}, fmt.Errorf("unexpected Exec: %s", query)
}

func (db *moderationProfileMatchMockDB) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	if strings.Contains(query, "FROM warnings") {
		db.mu.Lock()
		items := make([]store.Warning, 0, len(db.warnings))
		for _, warning := range db.warnings {
			if warning.ChatID == args[0].(int64) && warning.UserID == args[1].(int64) && warning.ConsumedAt == nil {
				items = append(items, warning)
			}
		}
		db.mu.Unlock()
		return &mockWarningRows{items: items}, nil
	}
	return nil, fmt.Errorf("unexpected Query: %s", query)
}

func (db *moderationProfileMatchMockDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	switch {
	case strings.Contains(query, "FROM system_state"):
		db.mu.Lock()
		state := db.systemState
		db.mu.Unlock()
		return mockScanRow(
			state.ID,
			state.AIPaused,
			state.ActionsPaused,
			state.Frozen,
			state.AIPausedReason,
			state.UpdatedAt,
			state.UpdatedBy,
		)
	case strings.Contains(query, "FROM global_config"):
		return mockErrorRow{err: pgx.ErrNoRows}
	case strings.Contains(query, "FROM groups"):
		return mockErrorRow{err: pgx.ErrNoRows}
	case strings.Contains(query, "FROM admins"):
		return mockErrorRow{err: pgx.ErrNoRows}
	case strings.Contains(query, "FROM user_trust"):
		db.mu.Lock()
		trust := db.userTrust
		db.mu.Unlock()
		return mockScanRow(
			trust.ChatID,
			trust.UserID,
			trust.Username,
			trust.FirstName,
			trust.LastName,
			trust.JoinedAt,
			trust.UpdatedAt,
			trust.StatusChangedAt,
			trust.Status,
			trust.Score,
			trust.MessagesChecked,
			trust.MessagesClean,
			trust.GraduatedAt,
			trust.BannedAt,
			trust.BannedReason,
			trust.Notes,
			trust.IsBot,
		)
	case strings.Contains(query, "INSERT INTO ai_decisions"):
		params := store.InsertAIDecisionParams{
			ChatID:        args[0].(int64),
			UserID:        args[1].(int64),
			MessageID:     args[2].(int64),
			MessageText:   args[3].(*string),
			ProviderID:    args[4].(*int64),
			ModelID:       args[5].(*int64),
			Model:         args[6].(string),
			PromptVersion: args[7].(string),
			Verdict:       args[8].(string),
			Confidence:    args[9].(float64),
			Category:      args[10].(string),
			Reason:        args[11].(*string),
			ActionTaken:   args[12].(string),
			AdminOverride: args[13].(*string),
			LatencyMs:     args[14].(int32),
			Scene:         args[15].(string),
		}
		db.mu.Lock()
		db.aiDecisions = append(db.aiDecisions, params)
		id := int64(len(db.aiDecisions))
		createdAt := db.now.Add(time.Duration(id) * time.Minute)
		db.mu.Unlock()
		return mockScanRow(
			id,
			params.ChatID,
			params.UserID,
			params.MessageID,
			params.MessageText,
			params.ProviderID,
			params.ModelID,
			params.Model,
			params.PromptVersion,
			params.Verdict,
			params.Confidence,
			params.Category,
			params.Reason,
			params.ActionTaken,
			params.AdminOverride,
			params.LatencyMs,
			params.Scene,
			createdAt,
		)
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
		createdAt := db.now.Add(time.Duration(id) * time.Minute)
		db.mu.Unlock()
		return mockScanRow(
			id,
			params.ChatID,
			params.UserID,
			params.Username,
			params.Rule,
			params.Matched,
			params.Action,
			params.MessageText,
			createdAt,
		)
	case strings.Contains(query, "INSERT INTO profile_check_logs"):
		params := store.InsertProfileCheckLogParams{
			ChatID:       args[0].(int64),
			UserID:       args[1].(int64),
			UserName:     args[2].(*string),
			Username:     args[3].(*string),
			Bio:          args[4].(*string),
			CheckMode:    args[5].(string),
			Result:       args[6].(string),
			MatchedRule:  args[7].(*string),
			AiConfidence: args[8].(*float32),
			AiVerdict:    args[9].(*string),
		}
		db.mu.Lock()
		db.profileLogs = append(db.profileLogs, params)
		id := int64(len(db.profileLogs))
		createdAt := db.now.Add(time.Duration(id) * time.Minute)
		db.mu.Unlock()
		return mockScanRow(
			id,
			params.ChatID,
			params.UserID,
			params.UserName,
			params.Username,
			params.Bio,
			params.CheckMode,
			params.Result,
			params.MatchedRule,
			params.AiConfidence,
			params.AiVerdict,
			createdAt,
		)
	case strings.Contains(query, "INSERT INTO warnings"):
		reason := args[2].(*string)
		issuedBy := args[3].(*int64)
		db.mu.Lock()
		id := int64(len(db.warnings) + 1)
		createdAt := db.now.Add(time.Duration(id) * time.Minute)
		warning := store.Warning{ID: id, ChatID: args[0].(int64), UserID: args[1].(int64), Reason: reason, IssuedBy: issuedBy, CreatedAt: createdAt}
		db.warnings = append(db.warnings, warning)
		db.mu.Unlock()
		return mockScanRow(
			warning.ID,
			warning.ChatID,
			warning.UserID,
			warning.Reason,
			warning.IssuedBy,
			warning.CreatedAt,
			warning.ConsumedAt,
		)
	case strings.Contains(query, "INSERT INTO banned_users"):
		bannedAt := db.now.Add(5 * time.Minute)
		return mockScanRow(
			args[0].(int64),
			args[1].(*string),
			args[2].(string),
			bannedAt,
			args[3].(*int64),
		)
	case strings.Contains(query, "INSERT INTO config_audit"):
		createdAt := db.now.Add(6 * time.Minute)
		return mockScanRow(
			int64(1),
			args[0].(string),
			args[1].(*int64),
			args[2].(int64),
			args[3].(string),
			args[4].([]byte),
			args[5].([]byte),
			args[6].(*string),
			createdAt,
		)
	case strings.Contains(query, "SET messages_checked = messages_checked"):
		db.mu.Lock()
		if row := db.recordTrustSideEffectLocked(); row != nil {
			db.mu.Unlock()
			return row
		}
		db.userTrust.MessagesChecked += args[2].(int32)
		db.userTrust.MessagesClean += args[3].(int32)
		db.userTrust.Score = args[4].(float64)
		db.userTrust.UpdatedAt = db.now.Add(2 * time.Minute)
		trust := db.userTrust
		db.mu.Unlock()
		return mockScanRow(
			trust.ChatID,
			trust.UserID,
			trust.Username,
			trust.FirstName,
			trust.LastName,
			trust.JoinedAt,
			trust.UpdatedAt,
			trust.StatusChangedAt,
			trust.Status,
			trust.Score,
			trust.MessagesChecked,
			trust.MessagesClean,
			trust.GraduatedAt,
			trust.BannedAt,
			trust.BannedReason,
			trust.Notes,
			trust.IsBot,
		)
	case strings.Contains(query, "SET messages_clean = 0"):
		db.mu.Lock()
		if row := db.recordTrustSideEffectLocked(); row != nil {
			db.mu.Unlock()
			return row
		}
		db.userTrust.MessagesClean = 0
		db.userTrust.Score = args[2].(float64)
		db.userTrust.UpdatedAt = db.now.Add(2 * time.Minute)
		trust := db.userTrust
		db.mu.Unlock()
		return mockScanRow(
			trust.ChatID,
			trust.UserID,
			trust.Username,
			trust.FirstName,
			trust.LastName,
			trust.JoinedAt,
			trust.UpdatedAt,
			trust.StatusChangedAt,
			trust.Status,
			trust.Score,
			trust.MessagesChecked,
			trust.MessagesClean,
			trust.GraduatedAt,
			trust.BannedAt,
			trust.BannedReason,
			trust.Notes,
			trust.IsBot,
		)
	case strings.Contains(query, "SET status = $3"):
		db.mu.Lock()
		if row := db.recordTrustSideEffectLocked(); row != nil {
			db.mu.Unlock()
			return row
		}
		db.userTrust.Status = args[2].(string)
		db.userTrust.Score = args[3].(float64)
		db.userTrust.GraduatedAt = args[4].(*time.Time)
		db.userTrust.BannedAt = args[5].(*time.Time)
		db.userTrust.BannedReason = args[6].([]byte)
		db.userTrust.Notes = args[7].(*string)
		db.userTrust.StatusChangedAt = db.now.Add(3 * time.Minute)
		db.userTrust.UpdatedAt = db.now.Add(3 * time.Minute)
		trust := db.userTrust
		db.mu.Unlock()
		return mockScanRow(
			trust.ChatID,
			trust.UserID,
			trust.Username,
			trust.FirstName,
			trust.LastName,
			trust.JoinedAt,
			trust.UpdatedAt,
			trust.StatusChangedAt,
			trust.Status,
			trust.Score,
			trust.MessagesChecked,
			trust.MessagesClean,
			trust.GraduatedAt,
			trust.BannedAt,
			trust.BannedReason,
			trust.Notes,
			trust.IsBot,
		)
	default:
		return mockErrorRow{err: fmt.Errorf("unexpected query: %s", query)}
	}
}

type mockErrorRow struct {
	err error
}

func (r mockErrorRow) Scan(...any) error {
	return r.err
}

type mockRow struct {
	values []any
}

type mockWarningRows struct {
	items  []store.Warning
	index  int
	closed bool
}

func (r *mockWarningRows) Close() {
	r.closed = true
}

func (r *mockWarningRows) Err() error {
	return nil
}

func (r *mockWarningRows) CommandTag() pgconn.CommandTag {
	return pgconn.CommandTag{}
}

func (r *mockWarningRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}

func (r *mockWarningRows) Next() bool {
	if r.index >= len(r.items) {
		r.closed = true
		return false
	}
	r.index++
	return true
}

func (r *mockWarningRows) Scan(dest ...any) error {
	if r.index == 0 || r.index > len(r.items) {
		return fmt.Errorf("scan warning row without current item")
	}
	warning := r.items[r.index-1]
	return mockRow{values: []any{
		warning.ID,
		warning.ChatID,
		warning.UserID,
		warning.Reason,
		warning.IssuedBy,
		warning.CreatedAt,
		warning.ConsumedAt,
	}}.Scan(dest...)
}

func (r *mockWarningRows) Values() ([]any, error) {
	if r.index == 0 || r.index > len(r.items) {
		return nil, fmt.Errorf("values warning row without current item")
	}
	warning := r.items[r.index-1]
	return []any{warning.ID, warning.ChatID, warning.UserID, warning.Reason, warning.IssuedBy, warning.CreatedAt, warning.ConsumedAt}, nil
}

func (r *mockWarningRows) RawValues() [][]byte {
	return nil
}

func (r *mockWarningRows) Conn() *pgx.Conn {
	return nil
}

func mockScanRow(values ...any) pgx.Row {
	return mockRow{values: values}
}

func (r mockRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan mismatch: got %d destinations, want %d", len(dest), len(r.values))
	}
	for i := range dest {
		if err := assignScanValue(dest[i], r.values[i]); err != nil {
			return err
		}
	}
	return nil
}

func assignScanValue(dest any, value any) error {
	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Pointer || dv.IsNil() {
		return fmt.Errorf("destination must be a non-nil pointer")
	}
	target := dv.Elem()
	if value == nil {
		target.Set(reflect.Zero(target.Type()))
		return nil
	}
	vv := reflect.ValueOf(value)
	if vv.Type().AssignableTo(target.Type()) {
		target.Set(vv)
		return nil
	}
	if vv.Type().ConvertibleTo(target.Type()) {
		target.Set(vv.Convert(target.Type()))
		return nil
	}
	return fmt.Errorf("cannot assign %T to %T", value, dest)
}

type telegramMockTransport struct {
	mu      sync.Mutex
	bio     string
	methods []string
	bodies  []string
}

func newMockTelegramBot(t *testing.T, bio string) (*tele.Bot, *telegramMockTransport) {
	t.Helper()

	transport := &telegramMockTransport{bio: bio}
	client := &http.Client{Transport: transport}
	botClient, err := tele.NewBot(tele.Settings{
		Token:       "123:TEST",
		URL:         "http://telegram.test",
		Synchronous: true,
		Client:      client,
	})
	if err != nil {
		t.Fatalf("tele.NewBot() error = %v", err)
	}
	return botClient, transport
}

func (t *telegramMockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	method := path.Base(req.URL.Path)

	requestBody := ""
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		requestBody = string(raw)
	}

	t.mu.Lock()
	t.methods = append(t.methods, method)
	if requestBody != "" {
		t.bodies = append(t.bodies, requestBody)
	}
	t.mu.Unlock()

	body := `{"ok":true,"result":true}`
	switch method {
	case "getMe":
		body = `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"test-bot","username":"test_bot"}}`
	case "getChat":
		body = fmt.Sprintf(`{"ok":true,"result":{"id":42,"type":"private","bio":%q}}`, t.bio)
	case "getChatMember":
		body = `{"ok":true,"result":{"user":{"id":1,"is_bot":false,"first_name":"admin"},"status":"administrator"}}`
	case "sendMessage":
		body = `{"ok":true,"result":{"message_id":999,"chat":{"id":-100123,"type":"supergroup"}}}`
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (t *telegramMockTransport) Methods() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.methods...)
}

func (t *telegramMockTransport) RequestBodies() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.bodies...)
}

func countString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
