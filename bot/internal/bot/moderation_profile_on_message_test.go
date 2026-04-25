package bot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
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

type moderationProfileMatchMockDB struct {
	mu          sync.Mutex
	now         time.Time
	userTrust   store.UserTrust
	systemState store.SystemState
	aiDecisions []store.InsertAIDecisionParams
	violations  []store.InsertViolationParams
	profileLogs []store.InsertProfileCheckLogParams
}

func newModerationProfileMatchMockDB(chatID, userID int64) *moderationProfileMatchMockDB {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	return &moderationProfileMatchMockDB{
		now: now,
		userTrust: store.UserTrust{
			ChatID:    chatID,
			UserID:    userID,
			JoinedAt:  now,
			UpdatedAt: now,
			Status:    "new",
			Score:     0.5,
		},
		systemState: store.SystemState{
			ID:        1,
			UpdatedAt: now,
		},
	}
}

func (db *moderationProfileMatchMockDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, fmt.Errorf("unexpected Exec")
}

func (db *moderationProfileMatchMockDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected Query")
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
			state.AIBudgetLocked,
			state.AIBudgetLockedDate,
			state.UpdatedAt,
			state.UpdatedBy,
		)
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
			trust.Status,
			trust.Score,
			trust.MessagesChecked,
			trust.MessagesClean,
			trust.GraduatedAt,
			trust.BannedAt,
			trust.BannedReason,
			trust.Notes,
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
			CostCents:     args[15].(float64),
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
			params.CostCents,
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
	case strings.Contains(query, "SET messages_clean = 0"):
		db.mu.Lock()
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
			trust.Status,
			trust.Score,
			trust.MessagesChecked,
			trust.MessagesClean,
			trust.GraduatedAt,
			trust.BannedAt,
			trust.BannedReason,
			trust.Notes,
		)
	case strings.Contains(query, "SET status = $3"):
		db.mu.Lock()
		db.userTrust.Status = args[2].(string)
		db.userTrust.Score = args[3].(float64)
		db.userTrust.GraduatedAt = args[4].(*time.Time)
		db.userTrust.BannedAt = args[5].(*time.Time)
		db.userTrust.BannedReason = args[6].([]byte)
		db.userTrust.Notes = args[7].(*string)
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
			trust.Status,
			trust.Score,
			trust.MessagesChecked,
			trust.MessagesClean,
			trust.GraduatedAt,
			trust.BannedAt,
			trust.BannedReason,
			trust.Notes,
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

	t.mu.Lock()
	t.methods = append(t.methods, method)
	t.mu.Unlock()

	body := `{"ok":true,"result":true}`
	switch method {
	case "getMe":
		body = `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"test-bot","username":"test_bot"}}`
	case "getChat":
		body = fmt.Sprintf(`{"ok":true,"result":{"id":42,"type":"private","bio":%q}}`, t.bio)
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
