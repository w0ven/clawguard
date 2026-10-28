//go:build integration

package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
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
		if id == "103" {
			return nil, fmt.Errorf("fixture lookup failure")
		}
		body = fmt.Sprintf(`{"ok":true,"result":{"user":{"id":%s,"is_bot":false,"first_name":"fixture"},"status":%q}}`, id, status)
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
