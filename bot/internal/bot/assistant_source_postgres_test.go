package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSourceRefactorEffectivePostgres(t *testing.T) {
	dsn := os.Getenv("CG_SOURCE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("dedicated local assistant_source_test PostgreSQL required")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Hostname() != "127.0.0.1" || u.Path != "/assistant_source_test" || u.User == nil || u.User.Username() != "assistant_source_test" {
		t.Fatal("refusing non-task DB")
	}
	ctx := context.Background()
	db, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	q := store.New(db)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	exec := func(s string, args ...any) { t.Helper(); _, e := db.Exec(ctx, s, args...); must(e) }
	chat := -time.Now().UnixMilli()
	other := chat - 1
	for _, id := range []int64{chat, other} {
		exec(`INSERT INTO groups(chat_id,title,type,config)VALUES($1,'source local','supergroup','{"moderation":"unchanged"}')`, id)
		exec(`INSERT INTO authorized_groups(chat_id)VALUES($1)`, id)
		exec(`INSERT INTO group_assistant_policies(chat_id,chat_enabled,learning_enabled,system_prompt)VALUES($1,true,true,'保留用户prompt')`, id)
	}
	var mu sync.Mutex
	calls := []string{}
	embeds := 0
	batchInputs := []string{}
	a, legacy := sourceRefactorPool(t, func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model    string       `json:"model"`
			Messages []ai.Message `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Error(e)
		}
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, b.Model)
		if r.URL.Path == "/embeddings" {
			embeds++
			fmt.Fprint(w, `{"data":[{"embedding":[1,0,0]}]}`)
			return
		}
		if input := fmt.Sprint(b.Messages); strings.Contains(input, "第三片段") {
			// SGB reply candidates contain short previews of this batch in the
			// system message. Count current/history text, not those named aliases.
			contextText := ""
			for _, message := range b.Messages {
				if message.Role != "system" {
					contextText += fmt.Sprint(message.Content)
				}
			}
			batchInputs = append(batchInputs, contextText)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"schema\":\"smart-group-bot.reply.v2\",\"messages\":[{\"text\":\"合并确认\",\"delivery_mode\":\"message\"}]}"}}]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"facts\":[{\"subject\":\"活动安排\",\"content\":\"这周六下午活动\",\"valid_scope\":\"this_week\",\"source_quote\":\"这周六下午活动\"}]}"}}]}`)
	})
	a.queries = q
	a.service.queries = q
	var old []byte
	must(db.QueryRow(ctx, `SELECT model_roles FROM group_assistant_global_settings WHERE id=1`).Scan(&old))
	defer func() {
		_, _ = db.Exec(ctx, `UPDATE group_assistant_global_settings SET model_roles=$1 WHERE id=1`, old)
	}()
	roles := assistantRoleSet{Main: store.AssistantModelRoleConfig{ModelRef: "mock:main", Fallbacks: []string{"mock:backup", "mock:text"}, Strategy: "weighted", ModelOptions: map[string]store.AssistantModelLoadOptions{}}}
	for i, ref := range []string{"mock:main", "mock:backup", "mock:text"} {
		roles.Main.ModelOptions[ref] = store.AssistantModelLoadOptions{Weight: []int{5, 3, 2}[i], MaxConcurrency: 2, TimeoutMs: 1000, CooldownSeconds: 1}
	}
	exec(`UPDATE group_assistant_global_settings SET model_roles=$1 WHERE id=1`, marshalAssistantRoleSet(roles))
	legacy.Strategy = "primary-overflow"
	legacy.TaskAssignments = map[string]AssistantTaskAssignment{"chat": {Primary: "backup", Backups: []string{"text"}}}
	legacy.InheritGlobal = nil
	saved, e := q.UpsertGroupAssistantPool(ctx, store.UpsertGroupAssistantPoolParams{ChatID: chat, Strategy: legacy.Strategy, Config: EncodeAssistantPool(legacy)})
	must(e)
	effective, source, e := a.EffectivePool(ctx, chat)
	must(e)
	if source != "legacy_group" || effective.TaskAssignments["chat"].Primary != "backup" {
		t.Fatalf("legacy changed: %s %+v", source, effective)
	}
	_, _, e = a.dispatchTools(ctx, chat, effective, store.GroupAssistantPolicy{}, "s", nil, nil)
	must(e)
	if !reflect.DeepEqual(calls, []string{"backup"}) {
		t.Fatal("legacy actual upstream changed", calls)
	}
	// Dormant group references must not constrain active globally inherited capacity.
	inherit := true
	legacy.InheritGlobal = &inherit
	legacy.Endpoints[0].MaxConcurrency = 1
	_, e = q.UpsertGroupAssistantPool(ctx, store.UpsertGroupAssistantPoolParams{ChatID: chat, ExpectedVersion: saved.Version, Strategy: legacy.Strategy, Config: EncodeAssistantPool(legacy)})
	must(e)
	effective, source, e = a.EffectivePool(ctx, chat)
	must(e)
	if source != "global" {
		t.Fatal(source)
	}
	if limit := a.effectiveEndpointLimit(ctx, "mock:main", 2); limit != 2 {
		t.Fatalf("dormant draft constrained active capacity=%d", limit)
	}
	mu.Lock()
	calls = nil
	mu.Unlock()
	for i := 0; i < 10; i++ {
		_, _, e = a.dispatchTools(ctx, chat, effective, store.GroupAssistantPolicy{}, "s", nil, nil)
		must(e)
	}
	counts := map[string]int{}
	for _, m := range calls {
		counts[m]++
	}
	if !reflect.DeepEqual(counts, map[string]int{"main": 5, "backup": 3, "text": 2}) {
		t.Fatal("DB effective strategy not used", counts)
	}
	// Actual ordinary learning still writes a learned fact, not an admin fact.
	text := "这周六下午活动"
	hash := sha256.Sum256([]byte(text))
	message, e := q.UpsertGroupAssistantMessage(ctx, store.UpsertGroupAssistantMessageParams{ChatID: chat, ThreadID: 7, TelegramMessageID: 77, SenderID: 12, SenderName: "普通成员", Role: "user", Text: text, ContentHash: hex.EncodeToString(hash[:]), Approved: true, Delivered: true, ExpiresAt: time.Now().Add(time.Hour), SourceType: "telegram_approved_message"})
	must(e)
	policy, e := a.Policy(ctx, chat)
	must(e)
	a.extractAndStoreFact(assistantLearningJob{policy: policy, chatID: chat, threadID: 7, messageID: 77, senderID: 12, senderName: "普通成员", text: text, authority: "learned_fact", sourceType: "telegram_approved_message", sourceChat: chat, operatorID: 12})
	facts, e := a.loadMemories(ctx, chat, "")
	must(e)
	if len(facts) != 1 || facts[0].AuthorityLevel != "learned_fact" || facts[0].SourceMessageID == nil || *facts[0].SourceMessageID != 77 {
		t.Fatalf("learning/source authority not preserved %+v", facts)
	}
	a.indexAssistantGroup(ctx, chat)
	var persisted int
	must(db.QueryRow(ctx, `SELECT count(*) FROM group_assistant_message_vectors WHERE message_id=$1`, message.ID).Scan(&persisted))
	if persisted != 3 {
		t.Fatalf("production index pass did not persist all configured model spaces: %d", persisted)
	}
	mu.Lock()
	indexedCalls := embeds
	mu.Unlock()
	a.indexAssistantGroup(ctx, chat)
	mu.Lock()
	noNewWork := embeds == indexedCalls
	mu.Unlock()
	if !noNewWork {
		t.Fatal("incremental index pass re-embedded unchanged sources")
	}
	ref := "mock:main"
	// Fix the query model to the indexed model: no document re-embedding per recall.
	single := effective
	single.TaskAssignments = map[string]AssistantTaskAssignment{"chat": effective.TaskAssignments["chat"], "vector": {Primary: assistantRoleEndpointID("chat", ref)}}
	custom := false
	single.InheritGlobal = &custom
	row, e := q.GetGroupAssistantPool(ctx, chat)
	must(e)
	_, e = q.UpsertGroupAssistantPool(ctx, store.UpsertGroupAssistantPoolParams{ChatID: chat, ExpectedVersion: row.Version, Strategy: single.Strategy, Config: EncodeAssistantPool(single)})
	must(e)
	mu.Lock()
	beforeEmbeds := embeds
	mu.Unlock()
	rows, e := a.recallArchive(ctx, chat, int32Ptr(7), "完全无字面交集", nil, 0, 4, store.AssistantGlobalSettings{MemoryRecallEnabled: true})
	must(e)
	if len(rows) != 1 || rows[0].Text != text {
		t.Fatalf("persistent semantic recall missing %+v", rows)
	}
	mu.Lock()
	delta := embeds - beforeEmbeds
	mu.Unlock()
	if delta != 1 {
		t.Fatalf("recall re-embedded documents: calls=%d", delta)
	}
	_, e = a.recallArchive(ctx, other, int32Ptr(7), "", []string{assistantRecallKey(message)}, 2, 12, store.AssistantGlobalSettings{})
	if e == nil {
		t.Fatal("cross-group message_keys accepted")
	}
	// Full delivery entry: confirmed message IDs and reply associations persist.
	sender := &recordingTelegramSender{}
	a.service.sender, a.service.assistant = sender, a
	msg := &tele.Message{ID: 77, Chat: &tele.Chat{ID: chat}, ThreadID: 7, Text: text}
	must(a.service.deliverAssistantReply(ctx, msg, policy, assistantReplyDirect, store.AssistantGlobalSettings{}, effective, nil, `{"schema":"smart-group-bot.reply.v2","messages":[{"text":"收到","delivery_mode":"reply","reply_to":"latest_input"},{"text":"明白","delivery_mode":"message"}]}`))
	if len(sender.items) != 2 {
		t.Fatalf("multi bubble actual sends=%d", len(sender.items))
	}
	var associations []string
	delivered, err := q.ListGroupAssistantMessages(ctx, store.ListGroupAssistantMessagesParams{ChatID: chat, ThreadID: int32Ptr(7), Limit: 10})
	must(err)
	for _, m := range delivered {
		if m.Role == "assistant" {
			associations = append(associations, m.SourceID)
		}
	}
	if !reflect.DeepEqual(associations, []string{"", "77"}) {
		t.Fatalf("actual reply_to persistence=%v", associations)
	}
	a.ttsSynth = mockTTSSynth{available: true}
	voicePolicy := policy
	voicePolicy.TTSMode = "always"
	_, err = a.service.sendAssistantReplyPlan(ctx, msg.Chat, "你好", &tele.SendOptions{ThreadID: 7}, voicePolicy, store.AssistantGlobalSettings{})
	must(err)
	if sender.voices() != 1 || len(sender.items) != 3 {
		t.Fatal("always voice duplicated text")
	}
	mediaCtx := withAssistantRuntime(ctx, &assistantToolRuntime{msg: msg, voiceSent: true})
	must(a.service.deliverAssistantReply(mediaCtx, msg, voicePolicy, assistantReplyDirect, store.AssistantGlobalSettings{}, effective, nil, "不能重复"))
	if len(sender.items) != 3 {
		t.Fatal("already sent tool media duplicated final text")
	}
	a.ttsSynth = sourceFailTTSSynth{}
	_, err = a.service.sendAssistantReplyPlan(ctx, msg.Chat, "退回文本", &tele.SendOptions{}, voicePolicy, store.AssistantGlobalSettings{})
	must(err)
	if len(sender.items) != 4 || sender.voices() != 1 {
		t.Fatal("failed synthesis did not fall back to one text")
	}
	if _, err = a.executeDoubaoTTSTool(ctx, chat, voicePolicy, map[string]any{"text": "失败"}); err == nil {
		t.Fatal("failed TTS tool claimed success")
	}
	t.Log("real delivery: two bubbles with stored reply_to, always voice only, failed synth one text, successful tool media never duplicated")
	must(q.InvalidateGroupAssistantSource(ctx, chat, 77))
	rows, e = a.recallArchive(ctx, chat, int32Ptr(7), "活动", nil, 0, 4, store.AssistantGlobalSettings{})
	must(e)
	if len(rows) != 0 {
		t.Fatal("blocked source recalled")
	}
	facts, e = a.loadMemories(ctx, chat, "")
	must(e)
	if len(facts) != 0 {
		t.Fatal("blocked source fact still active")
	}
	// Execute the production flush→resolve-action→prompt→tool loop→delivery
	// path with three retained parts and one concurrently revoked batch source.
	batch := &assistantReplyBatch{}
	for i, fragment := range []string{"第一片段", "不应复活的片段", "第二片段", "第三片段"} {
		msg := &tele.Message{ID: 81 + i, Chat: &tele.Chat{ID: chat}, ThreadID: 7, Sender: &tele.User{ID: 12}, Text: fragment}
		must(a.storeIncoming(ctx, policy, msg, fragment, false))
		batch.items = append(batch.items, assistantReplyBatchItem{msg: msg, text: fragment, direct: i == 0})
	}
	must(q.InvalidateGroupAssistantSource(ctx, chat, 82))
	a.replyBatches["source-batch"] = batch
	a.flushReplyBatch("source-batch", batch, a.service)
	mu.Lock()
	batchInputCount := len(batchInputs)
	input := strings.Join(batchInputs, "\n")
	mu.Unlock()
	if batchInputCount != 1 || strings.Contains(input, "不应复活的片段") || strings.Count(input, "第一片段") != 1 || strings.Count(input, "第二片段") != 1 || strings.Count(input, "第三片段") != 1 {
		t.Fatalf("batch source/prompt/one-turn contract failed: count=%d input=%s", batchInputCount, input)
	}
	if len(sender.items) != 5 {
		t.Fatalf("merged turn sent %d messages, expected one extra", len(sender.items)-4)
	}
	t.Log("production pending flush: 3 valid parts → one upstream turn, invalidated part excluded, no duplicated hot-window parts; proactive-off still honors batch direct")
	var cfgText string
	must(db.QueryRow(ctx, `SELECT config::TEXT FROM groups WHERE chat_id=$1`, chat).Scan(&cfgText))
	if cfgText != `{"moderation": "unchanged"}` {
		t.Fatal("moderation changed", cfgText)
	}
	registry := a.models.(*assistantVerificationRegistry)
	model := registry.models[ai.NewModelRef("mock", "main")]
	model.Enabled = false
	registry.models[ai.NewModelRef("mock", "main")] = model
	status, err := a.Status(ctx, chat)
	must(err)
	for _, endpoint := range status.Endpoints {
		if endpoint.ModelRef == "mock:main" && endpoint.Status != "disabled" {
			t.Fatal("disabled model falsely reported healthy", endpoint)
		}
	}
	if !status.CanChat {
		t.Fatal("disabled primary suppressed capable backup")
	}
	t.Logf("real PG effective legacy→global actual HTTP=%v; ordinary auto-learning/source authority, persistent semantic query-only embedding, revocation and group/topic scope passed", counts)
}
