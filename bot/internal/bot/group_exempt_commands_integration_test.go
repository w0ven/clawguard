//go:build integration

package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

type groupCommandTransport struct{}

func (groupCommandTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	method := path.Base(r.URL.Path)
	raw, _ := io.ReadAll(r.Body)
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	body := `{"ok":true,"result":true}`
	switch method {
	case "getMe":
		body = `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"fixture","username":"fixture_bot"}}`
	case "getChatMember":
		chat := fmt.Sprint(v["chat_id"])
		id := fmt.Sprint(v["user_id"])
		status := "member"
		if id == "101" && chat == "-100777001" {
			status = "administrator"
		}
		if id == "103" || id == "801" {
			return nil, fmt.Errorf("fixture lookup failure")
		}
		isBot := id == "800"
		body = fmt.Sprintf(`{"ok":true,"result":{"user":{"id":%s,"is_bot":%t,"first_name":"fixture"},"status":%q}}`, id, isBot, status)
	case "sendMessage":
		body = `{"ok":true,"result":{"message_id":999,"chat":{"id":-100777001,"type":"supergroup"}}}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
func TestGroupExemptCommandsIntegration(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := store.New(pool)
	const a int64 = -100777001
	const b int64 = -100777002
	_, _ = pool.Exec(ctx, `DELETE FROM group_exempt_users WHERE chat_id IN ($1,$2)`, a, b)
	_, _ = pool.Exec(ctx, `DELETE FROM config_audit WHERE chat_id IN ($1,$2)`, a, b)
	// A Web owner who is not a Telegram admin cannot use the group command.
	if _, err = pool.Exec(ctx, `INSERT INTO admins(telegram_id,role) VALUES(102,'owner') ON CONFLICT (telegram_id) DO UPDATE SET role='owner'`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM admins WHERE telegram_id=102`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM group_exempt_users WHERE chat_id IN ($1,$2)`, a, b)
		_, _ = pool.Exec(context.Background(), `DELETE FROM config_audit WHERE chat_id IN ($1,$2)`, a, b)
	}()
	client, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Synchronous: true, Client: &http.Client{Transport: groupCommandTransport{}}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{logger: zap.NewNop(), queries: q, bot: client, sender: client}
	svc.verifyBtn = tele.Btn{Unique: "verify_human"}
	svc.verifyMathBtn = tele.Btn{Unique: "verify_math"}
	svc.verifyRandBtn = tele.Btn{Unique: "verify_random"}
	svc.verifyAdminBtn = tele.Btn{Unique: "verify_admin"}
	svc.registerHandlers()
	for _, id := range []int64{a, b} {
		svc.cacheAuthorizedGroup(ctx, id, true)
		svc.policySnapshots.Store(id, keywordReplyPolicy("none", 0))
		svc.policySnapshotAt.Store(id, time.Now())
	}
	svc.cacheSystemState(ctx, store.SystemState{})
	update := func(chatID, actor int64, text string, reply *tele.Message, senderChat *tele.Chat) {
		msg := &tele.Message{ID: 1, Text: text, Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: actor, FirstName: "actor"}, ReplyTo: reply, SenderChat: senderChat}
		client.ProcessUpdate(tele.Update{Message: msg})
	}
	assert := func(chat, id int64, want bool) {
		t.Helper()
		active, _, err := q.GroupExemptionStatus(ctx, chat, id)
		if err != nil || active != want {
			t.Fatalf("group %d user %d active=%v err=%v", chat, id, active, err)
		}
	}
	update(a, 101, "/exempt@fixture_bot 444 1d reason", nil, nil)
	assert(a, 444, true)
	assert(b, 444, false)
	record, err := q.GetGroupExemptUser(ctx, a, 444)
	if err != nil || record.Note != "reason" || record.ExpiresAt == nil {
		t.Fatalf("duration/note: %+v %v", record, err)
	}
	reply := &tele.Message{Sender: &tele.User{ID: 445, FirstName: "target"}}
	update(a, 101, "/exempt 2h reply", reply, nil)
	assert(a, 445, true)
	update(a, 102, "/exempt 446", nil, nil)
	update(b, 101, "/exempt 446", nil, nil)
	update(a, 103, "/exempt 446", nil, nil)
	update(a, 101, "/exempt 446", nil, &tele.Chat{ID: -5})
	assert(a, 446, false)
	assert(b, 446, false)
	update(a, 101, "/exempt +446", nil, nil)
	update(a, 101, "/exempt 0", nil, nil)
	update(a, 101, "/exempt 446 0d", nil, nil)
	update(a, 101, "/exempt 446 999999999999d", nil, nil)
	assert(a, 446, false)
	update(a, 101, "/exempt 446", &tele.Message{Sender: &tele.User{ID: 446}, SenderChat: &tele.Chat{ID: -7}}, nil)
	assert(a, 446, false)
	private := &tele.Message{ID: 1, Text: "/exempt 446", Chat: &tele.Chat{ID: 101, Type: tele.ChatPrivate}, Sender: &tele.User{ID: 101}}
	client.ProcessUpdate(tele.Update{Message: private})
	assert(a, 446, false)
	update(a, 101, "/exemptlist@fixture_bot 1", nil, nil)
	update(a, 101, "/unexempt@fixture_bot 444", nil, nil)
	assert(a, 444, false)
	update(a, 101, "/unexempt", reply, nil)
	assert(a, 445, false)
	var audits int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM config_audit WHERE chat_id=$1 AND admin_id=0 AND action IN ('command_exempt','command_unexempt') AND diff::jsonb->>'telegram_user_id'='101'`, a).Scan(&audits)
	if err != nil || audits != 4 {
		t.Fatalf("command audit=%d %v", audits, err)
	}
}

type countedExemptPool struct {
	*pgxpool.Pool
	exemptionQueries atomic.Int64
}

func (p *countedExemptPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "FROM group_exempt_users") && strings.Contains(sql, "EXISTS") {
		p.exemptionQueries.Add(1)
	}
	return p.Pool.QueryRow(ctx, sql, args...)
}

type reviewCommandTransport struct {
	groupCommandTransport
	methods  []string
	bodies   []string
	onLookup func([]byte)
}

func (r *reviewCommandTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.methods = append(r.methods, path.Base(req.URL.Path))
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		r.bodies = append(r.bodies, string(raw))
		if path.Base(req.URL.Path) == "getChatMember" && r.onLookup != nil {
			r.onLookup(raw)
		}
		req.Body = io.NopCloser(strings.NewReader(string(raw)))
	}
	return r.groupCommandTransport.RoundTrip(req)
}
func TestReviewCommandDispatchIntegration(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const chatID int64 = -100777001
	_, err = pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES($1,'command fixture','supergroup') ON CONFLICT DO NOTHING`, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO authorized_groups(chat_id) VALUES($1) ON CONFLICT DO NOTHING`, chatID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM authorized_groups WHERE chat_id=$1`, chatID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM groups WHERE chat_id=$1`, chatID)
	}()
	tr := &reviewCommandTransport{}
	client, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Synchronous: true, Client: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	counted := &countedExemptPool{Pool: pool}
	s := &Service{logger: zap.NewNop(), queries: store.New(counted), bot: client, sender: client, sendLimiter: NewSendLimiter(), joinProtector: newJoinProtector()}
	s.verifyBtn = tele.Btn{Unique: "verify_human"}
	s.verifyMathBtn = tele.Btn{Unique: "verify_math"}
	s.verifyRandBtn = tele.Btn{Unique: "verify_random"}
	s.verifyAdminBtn = tele.Btn{Unique: "verify_admin"}
	s.registerHandlers()
	s.cacheAuthorizedGroup(ctx, chatID, true)
	s.cacheSystemState(ctx, store.SystemState{})
	p := keywordReplyPolicy("none", 0)
	p.Filter.Keywords.Enabled = true
	p.Filter.Keywords.List = []string{"blocked"}
	p.Filter.Keywords.Action = "delete"
	p.AI.Enabled = false
	s.policySnapshots.Store(chatID, p)
	s.policySnapshotAt.Store(chatID, time.Now())
	s.chatAdmins.Store(fmt.Sprintf("chat_admin:%d:%d", chatID, 102), chatAdminCacheEntry{IsAdmin: false, ExpireAt: time.Now().Add(time.Hour)})
	for i, text := range []string{"/exempt blocked", "/exempt@other_bot blocked", "/help blocked"} {
		before := countString(tr.methods, "deleteMessage")
		beforeQueries := counted.exemptionQueries.Load()
		msg := &tele.Message{ID: 500 + i, Text: text, Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 102, FirstName: "user"}}
		if err = s.ProcessUpdate(tele.Update{ID: 500 + i, Message: msg}); err != nil {
			t.Fatal(err)
		}
		if i != 1 && counted.exemptionQueries.Load()-beforeQueries != 2 {
			t.Fatalf("%s: want one initial and one fresh action query, got %d", text, counted.exemptionQueries.Load()-beforeQueries)
		}
		if countString(tr.methods, "deleteMessage") != before+1 {
			t.Fatalf("%q bypassed filter: %v", text, tr.methods)
		}
	}
	tr.onLookup = func(raw []byte) {
		if strings.Contains(string(raw), `"user_id":"104"`) || strings.Contains(string(raw), `"user_id":104`) {
			_, e := pool.Exec(ctx, `INSERT INTO group_exempt_users(chat_id,user_id,note) VALUES($1,104,'added while checking') ON CONFLICT (chat_id,user_id) DO UPDATE SET expires_at=NULL`, chatID)
			if e != nil {
				t.Error(e)
			}
		}
	}
	s.chatAdmins.Store(fmt.Sprintf("chat_admin:%d:%d", chatID, 104), chatAdminCacheEntry{IsAdmin: false, ExpireAt: time.Now().Add(time.Hour)})
	freshBefore := countString(tr.methods, "deleteMessage")
	fresh := &tele.Message{ID: 509, Text: "/exempt blocked", Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 104, FirstName: "new"}}
	if err = s.ProcessUpdate(tele.Update{ID: 509, Message: fresh}); err != nil {
		t.Fatal(err)
	}
	if countString(tr.methods, "deleteMessage") != freshBefore {
		t.Fatal("new exemption did not stop pending punishment")
	}
	var added bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM group_exempt_users WHERE chat_id=$1 AND user_id=104)`, chatID).Scan(&added); err != nil || !added {
		t.Fatalf("fixture did not add exemption: %v %v", added, err)
	}
	tr.onLookup = nil
	before := countString(tr.methods, "deleteMessage")
	m := &tele.Message{ID: 505, Text: "/exempt 123 --note blocked", Chat: &tele.Chat{ID: chatID, Type: tele.ChatSuperGroup}, Sender: &tele.User{ID: 101, FirstName: "admin"}}
	if err = s.ProcessUpdate(tele.Update{ID: 505, Message: m}); err != nil {
		t.Fatal(err)
	}
	if countString(tr.methods, "deleteMessage") != before {
		t.Fatal("admin command target/remark filtered")
	}
	m.ID = 508
	m.Text = "/exempt 125 1"
	if err = s.ProcessUpdate(tele.Update{ID: 508, Message: m}); err != nil {
		t.Fatal(err)
	}
	var invalidCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM group_exempt_users WHERE chat_id=$1 AND user_id=125`, chatID).Scan(&invalidCount); err != nil || invalidCount != 0 {
		t.Fatalf("ambiguous duration wrote exemption: %d %v", invalidCount, err)
	}
	if !strings.Contains(strings.Join(tr.bodies, "\n"), "永久") {
		t.Fatal("permanent confirmation missing")
	}
	m.ID = 506
	m.Text = "/exempt 124 1d 备注"
	if err = s.ProcessUpdate(tele.Update{ID: 506, Message: m}); err != nil {
		t.Fatal(err)
	}
	var expires *time.Time
	if err = pool.QueryRow(ctx, `SELECT expires_at FROM group_exempt_users WHERE chat_id=$1 AND user_id=124`, chatID).Scan(&expires); err != nil || expires == nil || !strings.Contains(strings.Join(tr.bodies, "\n"), expires.UTC().Format(time.RFC3339)) {
		t.Fatalf("expiry confirmation missing: %v %v %v", expires, err, tr.bodies)
	}
	s.chatAdmins.Store(fmt.Sprintf("chat_admin:%d:%d", chatID, 103), chatAdminCacheEntry{IsAdmin: false, ExpireAt: time.Now().Add(time.Hour)})
	before = countString(tr.methods, "deleteMessage")
	bad := &tele.Message{ID: 507, Text: "/exempt blocked", Chat: m.Chat, Sender: &tele.User{ID: 103, FirstName: "unknown"}}
	if err = s.ProcessUpdate(tele.Update{ID: 507, Message: bad}); err != nil {
		t.Fatal(err)
	}
	if countString(tr.methods, "deleteMessage") != before+1 {
		t.Fatalf("admin lookup failure bypassed ordinary filter: %v", tr.methods)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM group_exempt_users WHERE chat_id=$1`, chatID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO group_exempt_users(chat_id,user_id) SELECT $1,i FROM generate_series(9000,9020) i`, chatID); err != nil {
		t.Fatal(err)
	}
	beforeBodies := len(tr.bodies)
	m.ID = 510
	m.Text = "/exemptlist 2"
	if err = s.ProcessUpdate(tele.Update{ID: 510, Message: m}); err != nil {
		t.Fatal(err)
	}
	pageBody := strings.Join(tr.bodies[beforeBodies:], "\n")
	if !strings.Contains(pageBody, "9020") || strings.Contains(pageBody, "9000") {
		t.Fatalf("command page wrong: %s", pageBody)
	}
	_, err = pool.Exec(ctx, `INSERT INTO banned_users(user_id,source,reason) VALUES(600,'manual','legacy global'); INSERT INTO group_ban_records(chat_id,user_id,source,reason) VALUES(-999,600,'manual','other group')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM group_ban_records WHERE user_id=600`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM banned_users WHERE user_id=600`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_trust WHERE chat_id=$1 AND user_id=600`, chatID)
	}()
	// Telegram identity lookup failure must not dispatch either management command.
	blockedBefore := countString(tr.methods, "kickChatMember") + countString(tr.methods, "banChatMember")
	bad.ID = 511
	bad.Text = "/spam 600"
	if err = s.ProcessUpdate(tele.Update{ID: 511, Message: bad}); err != nil {
		t.Fatal(err)
	}
	if countString(tr.methods, "kickChatMember")+countString(tr.methods, "banChatMember") != blockedBefore {
		t.Fatal("unknown admin executed spam")
	}
	m.ID = 512
	m.Text = "/spam 600"
	if err = s.ProcessUpdate(tele.Update{ID: 512, Message: m}); err != nil {
		t.Fatal(err)
	}
	var n int
	var reason string
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM group_ban_records WHERE chat_id=$1 AND user_id=600`, chatID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("spam record %d %v bodies=%v", n, err, tr.bodies)
	}
	if err = pool.QueryRow(ctx, `SELECT reason FROM banned_users WHERE user_id=600`).Scan(&reason); err != nil || reason != "legacy global" {
		t.Fatalf("legacy overwritten %q %v", reason, err)
	}
	bad.ID = 513
	bad.Text = "/unban 600"
	unbanBefore := countString(tr.methods, "unbanChatMember")
	if err = s.ProcessUpdate(tele.Update{ID: 513, Message: bad}); err != nil {
		t.Fatal(err)
	}
	if countString(tr.methods, "unbanChatMember") != unbanBefore {
		t.Fatal("unknown admin executed unban")
	}
	m.ID = 514
	m.Text = "/unban 600"
	if err = s.ProcessUpdate(tele.Update{ID: 514, Message: m}); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM group_ban_records WHERE chat_id=$1 AND user_id=600`, chatID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unban record %d %v", n, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM group_ban_records WHERE chat_id=-999 AND user_id=600`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("other group record %d %v", n, err)
	}
	if err = pool.QueryRow(ctx, `SELECT reason FROM banned_users WHERE user_id=600`).Scan(&reason); err != nil || reason != "legacy global" {
		t.Fatalf("legacy lost %q %v", reason, err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM group_exempt_users WHERE chat_id=$1`, chatID)
}

func TestDeferredCandidateIdentityIntegration(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := store.New(pool)
	const chatID int64 = -100777001
	_, err = pool.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES($1,'fixture','supergroup') ON CONFLICT DO NOTHING`, chatID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM group_exempt_users WHERE chat_id=$1`, chatID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM groups WHERE chat_id=$1`, chatID)
	}()
	tr := &reviewCommandTransport{}
	client, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Synchronous: true, Client: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{logger: zap.NewNop(), queries: q, bot: client, sender: client, sendLimiter: NewSendLimiter(), joinProtector: newJoinProtector(), cleanupBreaker: newTelegramCleanupBreaker()}
	s.cacheAuthorizedGroup(ctx, chatID, true)
	s.cacheSystemState(ctx, store.SystemState{})
	s.policySnapshots.Store(chatID, keywordReplyPolicy("none", 0))
	s.policySnapshotAt.Store(chatID, time.Now())
	for _, id := range []int64{800, 801, 802} {
		if _, err = q.UpsertGroupExemptUser(ctx, chatID, id, "fixture", nil); err != nil {
			t.Fatal(err)
		}
		pending, e := q.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{ChatID: chatID, UserID: id, Method: "button", Payload: []byte(`{}`), ExpiresAt: time.Now().Add(-time.Minute)})
		if e != nil {
			t.Fatal(e)
		}
		before := countString(tr.methods, "kickChatMember")
		result, e := s.HandleVerificationExpiry(ctx, pending)
		if e != nil {
			t.Fatal(e)
		}
		if id == 800 {
			if !result.RetryAt.IsZero() || countString(tr.methods, "kickChatMember") != before+1 {
				t.Fatalf("bot was exempt: %+v %v", result, tr.methods)
			}
		} else if result.RetryAt.IsZero() || countString(tr.methods, "kickChatMember") != before {
			t.Fatalf("human or unknown punished: id=%d %+v %v", id, result, tr.methods)
		}
	}
}
