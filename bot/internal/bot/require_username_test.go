package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

type requireUsernameTestTransport struct {
	mu             sync.Mutex
	methods        []string
	sentTexts      []string
	restrictRights []tele.Rights
	restrictUntils []int64
	callbacks      []string
	memberUsername string
	memberRole     string
}

func (tr *requireUsernameTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	method := path.Base(r.URL.Path)
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.methods = append(tr.methods, method)

	response := `{"ok":true,"result":true}`
	switch method {
	case "getMe":
		response = `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"GuardBot","username":"guard_bot"}}`
	case "sendMessage":
		if text, ok := body["text"].(string); ok {
			tr.sentTexts = append(tr.sentTexts, text)
		}
		response = `{"ok":true,"result":{"message_id":701,"chat":{"id":-100123,"type":"supergroup"}}}`
	case "editMessageText":
		if text, ok := body["text"].(string); ok {
			tr.sentTexts = append(tr.sentTexts, text)
		}
		response = `{"ok":true,"result":{"message_id":702,"chat":{"id":888001,"type":"private"}}}`
	case "restrictChatMember":
		if u, ok := body["until_date"]; ok {
			switch v := u.(type) {
			case float64:
				tr.restrictUntils = append(tr.restrictUntils, int64(v))
			case string:
				var parsed int64
				_ = json.Unmarshal([]byte(v), &parsed)
				tr.restrictUntils = append(tr.restrictUntils, parsed)
			}
		}
		if permsRaw, ok := body["permissions"]; ok {
			rawBytes, _ := json.Marshal(permsRaw)
			var rights tele.Rights
			_ = json.Unmarshal(rawBytes, &rights)
			tr.restrictRights = append(tr.restrictRights, rights)
		}
		response = `{"ok":true,"result":true}`
	case "getChatMember":
		role := string(tr.memberRole)
		if role == "" {
			role = "restricted"
		}
		response = `{"ok":true,"result":{"status":"` + role + `","is_member":true,"user":{"id":5001,"is_bot":false,"first_name":"看喔竹耶","username":"` + tr.memberUsername + `"}}}`
	case "answerCallbackQuery":
		if text, ok := body["text"].(string); ok {
			tr.callbacks = append(tr.callbacks, text)
		}
		response = `{"ok":true,"result":true}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(response)),
	}, nil
}

func TestRequireUsernameFilterAndSelfUnmute(t *testing.T) {
	const (
		chatID = int64(-100123)
		userID = int64(5001)
	)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "new"

	tr := &requireUsernameTestTransport{}
	b, err := tele.NewBot(tele.Settings{
		Token:       "123:TEST",
		URL:         "http://telegram.test",
		Synchronous: true,
		Client:      &http.Client{Transport: tr},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		bot:         b,
		sender:      b,
		sendLimiter: NewSendLimiter(),
		logger:      zap.NewNop(),
		queries:     store.New(db),
	}
	svc.registerHandlers()

	policy := config.DefaultPolicy
	policy.Filter.RequireUsername.Enabled = true
	policy.Filter.RequireUsername.Scope = "ungraduated"
	policy.Filter.RequireUsername.MuteSeconds = 600
	policy.Filter.RequireUsername.AutoDeleteSeconds = 0
	policy.Filter.NewUser.Enabled = true
	policy.Filter.NewUser.NoMedia = true

	// 1. Ungraduated user without username sends message -> deleted, muted, notice sent
	msg := &tele.Message{
		ID:     301,
		Text:   "你好，我是新人",
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: userID, FirstName: "看喔竹耶"},
	}
	handled, err := svc.applyFilterChecks(context.Background(), msg, policy, false, false, false)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v; want handled require_username hit", handled, err)
	}

	tr.mu.Lock()
	if !containsString(tr.methods, "deleteMessage") || !containsString(tr.methods, "restrictChatMember") || !containsString(tr.methods, "sendMessage") {
		t.Fatalf("expected deleteMessage, restrictChatMember, sendMessage; got %v", tr.methods)
	}
	if len(tr.sentTexts) == 0 || !strings.Contains(tr.sentTexts[len(tr.sentTexts)-1], "请先设置用户名后才能发言.") || !strings.Contains(tr.sentTexts[len(tr.sentTexts)-1], "惩罚: 禁言到 - ") || !strings.Contains(tr.sentTexts[len(tr.sentTexts)-1], "+0800") {
		t.Fatalf("unexpected prompt text: %v", tr.sentTexts)
	}
	tr.mu.Unlock()

	db.mu.Lock()
	if len(db.violations) != 1 || db.violations[0].Rule != requireUsernameRule || db.violations[0].Action != "delete_mute" {
		t.Fatalf("unexpected violations: %+v", db.violations)
	}
	db.mu.Unlock()

	// 2.Trusted user without username in "ungraduated" scope -> allowed
	db.mu.Lock()
	db.userTrust.Status = "trusted"
	db.mu.Unlock()
	tr.mu.Lock()
	tr.methods = nil
	tr.mu.Unlock()

	msgTrusted := &tele.Message{
		ID:     302,
		Text:   "我是老群友",
		Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
		Sender: &tele.User{ID: userID, FirstName: "看喔竹耶"},
	}
	handled, err = svc.applyFilterChecks(context.Background(), msgTrusted, policy, false, false, false)
	if err != nil || handled {
		t.Fatalf("trusted user should pass in ungraduated scope: handled=%t err=%v", handled, err)
	}

	// 3. Trusted user without username in "all" scope -> intercepted, but duplicate prompt suppressed within window
	policy.Filter.RequireUsername.Scope = "all"
	tr.mu.Lock()
	tr.sentTexts = nil
	tr.mu.Unlock()
	handled, err = svc.applyFilterChecks(context.Background(), msgTrusted, policy, false, false, false)
	if err != nil || !handled {
		t.Fatalf("trusted user should be intercepted in all scope: handled=%t err=%v", handled, err)
	}
	tr.mu.Lock()
	if len(tr.sentTexts) != 0 {
		t.Fatalf("expected duplicate require_username prompt to be suppressed within cooldown window, got %v", tr.sentTexts)
	}
	tr.mu.Unlock()

	// 4. Self-unmute callback: non-owner click rejected
	db.mu.Lock()
	db.userTrust.Status = "new"
	db.mu.Unlock()
	cacheDeletedAccountTestSystemState(svc, false)
	svc.policySnapshots.Store(chatID, policy)
	svc.policySnapshotAt.Store(chatID, time.Now())

	otherUpdate := tele.Update{
		ID: 401,
		Callback: &tele.Callback{
			ID:      "cb-other",
			Sender:  &tele.User{ID: 9999, Username: "other_user"},
			Message: &tele.Message{ID: 701, Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}},
			Data:    "\freq_uname_unmute|5001",
		},
	}
	if err := svc.ProcessUpdate(otherUpdate); err != nil {
		t.Fatalf("ProcessUpdate(otherUpdate) error = %v", err)
	}
	tr.mu.Lock()
	if len(tr.callbacks) == 0 || !strings.Contains(tr.callbacks[len(tr.callbacks)-1], "仅限本人点击自助解禁") {
		t.Fatalf("expected non-owner alert, got %v", tr.callbacks)
	}
	tr.mu.Unlock()

	// 5. Self-unmute callback: owner clicks but still has no username -> rejected
	noNameUpdate := tele.Update{
		ID: 402,
		Callback: &tele.Callback{
			ID:      "cb-noname",
			Sender:  &tele.User{ID: userID, FirstName: "看喔竹耶"},
			Message: &tele.Message{ID: 701, Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}},
			Data:    "\freq_uname_unmute|5001",
		},
	}
	if err := svc.ProcessUpdate(noNameUpdate); err != nil {
		t.Fatalf("ProcessUpdate(noNameUpdate) error = %v", err)
	}
	tr.mu.Lock()
	if len(tr.callbacks) == 0 || !strings.Contains(tr.callbacks[len(tr.callbacks)-1], "请先在 Telegram 设置中添加 @用户名") {
		t.Fatalf("expected missing username alert, got %v", tr.callbacks)
	}
	tr.mu.Unlock()

	// 6. Self-unmute callback: owner sets username -> unmuted with ungraduated restricted rights and prompt deleted
	tr.mu.Lock()
	tr.methods = nil
	tr.restrictRights = nil
	tr.memberUsername = "kanwozhuye"
	tr.mu.Unlock()

	okUpdate := tele.Update{
		ID: 403,
		Callback: &tele.Callback{
			ID:      "cb-ok",
			Sender:  &tele.User{ID: userID, FirstName: "看喔竹耶", Username: "kanwozhuye"},
			Message: &tele.Message{ID: 701, Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}},
			Data:    "\freq_uname_unmute|5001",
		},
	}
	if err := svc.ProcessUpdate(okUpdate); err != nil {
		t.Fatalf("ProcessUpdate(okUpdate) error = %v", err)
	}
	tr.mu.Lock()
	if len(tr.callbacks) == 0 || !strings.Contains(tr.callbacks[len(tr.callbacks)-1], "已核实用户名，禁言已解除") {
		t.Fatalf("expected success alert, got %v", tr.callbacks)
	}
	if !containsString(tr.methods, "restrictChatMember") || !containsString(tr.methods, "deleteMessage") {
		t.Fatalf("expected restrictChatMember and deleteMessage on self-unmute, got %v", tr.methods)
	}
	if len(tr.restrictRights) == 0 || !tr.restrictRights[len(tr.restrictRights)-1].CanSendMessages || tr.restrictRights[len(tr.restrictRights)-1].CanSendPhotos {
		t.Fatalf("expected ungraduated rights (CanSendMessages=true, CanSendPhotos=false), got %+v", tr.restrictRights)
	}
	tr.mu.Unlock()
}

func TestAppealAdminCallbackInteraction(t *testing.T) {
	tr := &requireUsernameTestTransport{}
	b, err := tele.NewBot(tele.Settings{
		Token:       "123:TEST",
		URL:         "http://telegram.test",
		Synchronous: true,
		Client:      &http.Client{Transport: tr},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		cfg:         config.Config{PublicBaseURL: "https://rfcguard.misaka.si"},
		bot:         b,
		sender:      b,
		sendLimiter: NewSendLimiter(),
		logger:      zap.NewNop(),
	}
	svc.registerHandlers()

	var gotAdmin, gotAppeal int64
	var gotAction string
	svc.SetAppealDecisionHandler(func(_ context.Context, adminTelegramID, appealID int64, action string) (*store.AppealReview, string, error) {
		gotAdmin = adminTelegramID
		gotAppeal = appealID
		gotAction = action
		dec := "approve"
		reviewer := adminTelegramID
		return &store.AppealReview{
			ID:              appealID,
			CaseID:          88,
			ChatID:          -1002699516772,
			UserID:          6001,
			GroupTitle:      "RFCHOST 官方群",
			Status:          "completed",
			Decision:        &dec,
			PublicNote:      "管理员通过 Bot 私信审核通过",
			ExecutionResult: "审核通过，原禁言已解除",
			ReviewerID:      &reviewer,
			Reason:          "我是正常讨论主机配置",
			Action:          "mute",
			CaseResult:      "executed",
		}, "✅ 申诉已通过，处罚已解除", nil
	})

	update := tele.Update{
		ID: 501,
		Callback: &tele.Callback{
			ID:      "cb-appeal-approve",
			Sender:  &tele.User{ID: 888001, Username: "admin_owner"},
			Message: &tele.Message{ID: 702, Chat: &tele.Chat{ID: 888001, Type: tele.ChatPrivate}},
			Data:    "\fappeal_admin_act|42|approve",
		},
	}
	if err := svc.ProcessUpdate(update); err != nil {
		t.Fatalf("ProcessUpdate(appeal approve) error = %v", err)
	}

	if gotAdmin != 888001 || gotAppeal != 42 || gotAction != "approve" {
		t.Fatalf("got (%d, %d, %q), want (888001, 42, approve)", gotAdmin, gotAppeal, gotAction)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if !containsString(tr.methods, "editMessageText") {
		t.Fatalf("expected editMessageText to update admin private card, got %v", tr.methods)
	}
	if len(tr.sentTexts) == 0 || !strings.Contains(tr.sentTexts[len(tr.sentTexts)-1], "同意申诉") || !strings.Contains(tr.sentTexts[len(tr.sentTexts)-1], "审核通过，原禁言已解除") {
		t.Fatalf("expected updated card text, got %v", tr.sentTexts)
	}
	if len(tr.callbacks) == 0 || !strings.Contains(tr.callbacks[len(tr.callbacks)-1], "申诉已通过，处罚已解除") {
		t.Fatalf("expected callback alert, got %v", tr.callbacks)
	}
}

func TestPendingVerificationSuppressesRequireUsernameConflict(t *testing.T) {
	const (
		chatID = int64(-100123)
		userID = int64(5001)
	)
	db := newModerationProfileMatchMockDB(chatID, userID)
	db.userTrust.Status = "new"
	db.activePending = &store.PendingVerification{
		ID:        901,
		ChatID:    chatID,
		UserID:    userID,
		Method:    "turnstile_hcaptcha",
		Payload:   []byte(`{"token":"pending-tok"}`),
		ExpiresAt: time.Now().Add(3 * time.Minute),
		CreatedAt: time.Now(),
	}

	tr := &requireUsernameTestTransport{}
	b, err := tele.NewBot(tele.Settings{
		Token:       "123:TEST",
		URL:         "http://telegram.test",
		Synchronous: true,
		Client:      &http.Client{Transport: tr},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		bot:         b,
		sender:      b,
		sendLimiter: NewSendLimiter(),
		logger:      zap.NewNop(),
		queries:     store.New(db),
	}
	svc.registerHandlers()

	policy := config.DefaultPolicy
	policy.Filter.RequireUsername.Enabled = true
	policy.Filter.RequireUsername.Scope = "ungraduated"
	policy.Filter.RequireUsername.MuteSeconds = 600
	cacheDeletedAccountTestSystemState(svc, false)
	svc.policySnapshots.Store(chatID, policy)
	svc.policySnapshotAt.Store(chatID, time.Now())

	update := tele.Update{
		ID: 601,
		Message: &tele.Message{
			ID:     601,
			Text:   "e",
			Chat:   &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup},
			Sender: &tele.User{ID: userID, FirstName: "ninglu"},
		},
	}
	if err := svc.ProcessUpdate(update); err != nil {
		t.Fatalf("ProcessUpdate(pending user message) error = %v", err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if !containsString(tr.methods, "deleteMessage") {
		t.Fatalf("expected raced message during pending verification to be deleted, got %v", tr.methods)
	}
	if containsString(tr.methods, "restrictChatMember") || len(tr.sentTexts) > 0 {
		t.Fatalf("expected no conflicting require_username restrict or prompt during pending verification, got methods=%v texts=%v", tr.methods, tr.sentTexts)
	}
}
