package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestAssistantSourceRefactorPostgres(t *testing.T) {
	dsn := os.Getenv("CG_SOURCE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("dedicated local assistant_source_test database required")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Hostname() != "127.0.0.1" || u.Path != "/assistant_source_test" || u.User == nil || u.User.Username() != "assistant_source_test" {
		t.Fatal("refusing non-task DB")
	}
	ctx := context.Background()
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(goose.UpToContext(ctx, db, "../../migrations", 34))
	pool, e := pgxpool.New(ctx, dsn)
	must(e)
	defer pool.Close()
	q := New(pool)
	exec := func(s string, args ...any) { t.Helper(); _, e := pool.Exec(ctx, s, args...); must(e) }
	exec(`INSERT INTO group_assistant_policies(chat_id,learning_enabled,chat_model_ref,system_prompt)VALUES(-51001,true,'keep:old','显式保留prompt')`)
	exec(`INSERT INTO group_assistant_pools(chat_id,config)VALUES(-51001,'{"strategy":"primary-overflow","task_assignments":{"chat":{"primary":"keep","backups":[]}},"endpoints":[{"id":"keep","model_ref":"keep:old"}]}')`)
	exec(`INSERT INTO group_assistant_prompt_overrides(chat_id,prompt_key,content)VALUES(-51001,'persona','显式画像override')`)
	var before string
	must(pool.QueryRow(ctx, `SELECT row_to_json(p)::TEXT FROM group_assistant_policies p WHERE chat_id=-51001`).Scan(&before))
	msg := func(chat int64, id int64, text string, expiry time.Time) GroupAssistantMessage {
		t.Helper()
		hash := sha256.Sum256([]byte(text))
		m, e := q.UpsertGroupAssistantMessage(ctx, UpsertGroupAssistantMessageParams{ChatID: chat, ThreadID: 7, TelegramMessageID: id, SenderID: 10, SenderName: "检索用户", Role: "user", Text: text, ContentHash: hex.EncodeToString(hash[:]), Approved: true, Delivered: true, ExpiresAt: expiry, SourceType: "telegram_approved_message"})
		must(e)
		return m
	}
	chinese := msg(-51001, 1, "我们这周六下午一起参加活动", time.Now().Add(time.Hour))
	english := msg(-51001, 2, "deploy the application on the weekend schedule", time.Now().Add(time.Hour))
	expired := msg(-51001, 3, "expiredsecret", time.Now().Add(-time.Minute))
	_ = expired
	other := msg(-51002, 1, "跨群 周六 活动", time.Now().Add(time.Hour))
	_ = other
	oldForgottenSource := msg(-51001, 4, "迁移前已忘记的原文", time.Now().Add(time.Hour))
	for i, source := range []GroupAssistantMessage{chinese, oldForgottenSource} {
		id := source.TelegramMessageID
		memory, e := q.CreateGroupAssistantMemory(ctx, CreateGroupAssistantMemoryParams{ChatID: -51001, Subject: fmt.Sprintf("迁移前事实%d", i), Content: source.Text, MemoryType: "learned", AuthorityLevel: "learned_fact", ValidScope: "this_week", SourceType: "telegram_approved_message", SourceMessageID: &id, SourceContentHash: source.ContentHash, SourceVerified: "verified", SourceSnippet: source.Text, ExpiresAt: source.ExpiresAt, DedupeHash: fmt.Sprintf("pre35-memory-%d", i)})
		must(e)
		if i == 1 {
			must(q.ForgetGroupAssistantMemory(ctx, memory.ID, -51001, 100))
		}
	}
	legacySnapshot := func() string {
		var snapshot string
		must(pool.QueryRow(ctx, `SELECT jsonb_build_object('memories',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM group_assistant_memories m),'versions',(SELECT jsonb_agg(to_jsonb(v) ORDER BY id) FROM group_assistant_memory_versions v),'pools',(SELECT jsonb_agg(to_jsonb(p) ORDER BY chat_id) FROM group_assistant_pools p))::TEXT`).Scan(&snapshot))
		return snapshot
	}
	legacyBefore := legacySnapshot()
	must(goose.UpToContext(ctx, db, "../../migrations", 35))
	if after := legacySnapshot(); after != legacyBefore {
		t.Fatal("migration mutated legacy learned facts, source/expiry/forget state, versions or model pools")
	}
	var after string
	must(pool.QueryRow(ctx, `SELECT row_to_json(p)::TEXT FROM group_assistant_policies p WHERE chat_id=-51001`).Scan(&after))
	if before != after {
		t.Fatal("migration mutated policy", before, after)
	}
	var merge float64
	must(pool.QueryRow(ctx, `SELECT inbound_merge_window_sec FROM group_assistant_global_settings WHERE id=1`).Scan(&merge))
	if merge != 0.4 {
		t.Fatalf("old explicit merge window changed: %v", merge)
	}
	var original string
	must(pool.QueryRow(ctx, `SELECT text FROM group_assistant_messages WHERE id=$1`, chinese.ID).Scan(&original))
	if original != chinese.Text {
		t.Fatal("migration lost original")
	}
	var prompt string
	must(pool.QueryRow(ctx, `SELECT content FROM group_assistant_prompt_overrides WHERE chat_id=-51001`).Scan(&prompt))
	if prompt != "显式画像override" {
		t.Fatal("override lost")
	}
	var indexCount int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname='idx_assistant_message_lexical'`).Scan(&indexCount))
	if indexCount != 1 {
		t.Fatal("persistent GIN missing")
	}
	thread := int32(7)
	rows, e := q.SearchAssistantLexical(ctx, -51001, &thread, "周六 活动", 8)
	must(e)
	if len(rows) == 0 || rows[0].ID != chinese.ID {
		t.Fatalf("Chinese non-substring lookup failed: %+v", rows)
	}
	rows, e = q.SearchAssistantLexical(ctx, -51001, &thread, "schedule deploy", 8)
	must(e)
	if len(rows) == 0 || rows[0].ID != english.ID {
		t.Fatalf("English token lookup failed: %+v", rows)
	}
	rows, e = q.SearchAssistantLexical(ctx, -51001, &thread, "expiredsecret", 8)
	must(e)
	if len(rows) != 0 {
		t.Fatal("expired source recalled")
	}
	wrongThread := int32(8)
	rows, e = q.SearchAssistantLexical(ctx, -51001, &wrongThread, "周六", 8)
	must(e)
	if len(rows) != 0 {
		t.Fatal("cross topic leak")
	}
	rows, e = q.AssistantRecallByIDs(ctx, -51001, &thread, []int64{other.ID, chinese.ID})
	must(e)
	if len(rows) != 1 || rows[0].ID != chinese.ID {
		t.Fatal("cross group keys leak")
	}
	neighbors, e := q.AssistantRecallNeighbors(ctx, chinese, 2)
	must(e)
	found := false
	for _, m := range neighbors {
		found = found || m.ID == english.ID
		if m.ChatID != -51001 || m.ExpiresAt.Before(time.Now()) {
			t.Fatal("invalid context source")
		}
	}
	if !found {
		t.Fatal("adjacent original not expanded")
	}
	must(q.PutAssistantMessageVector(ctx, chinese, "fake:embedding", []float64{1, 0, 0}))
	rows, e = q.SearchAssistantSemantic(ctx, -51001, &thread, "fake:embedding", []float64{1, 0, 0}, 8)
	must(e)
	if len(rows) != 1 || rows[0].ID != chinese.ID {
		t.Fatalf("persistent vector failed: %+v", rows)
	}
	_, e = q.UpdateGroupAssistantMessage(ctx, UpdateGroupAssistantMessageParams{ChatID: chinese.ChatID, TelegramMessageID: chinese.TelegramMessageID, ThreadID: 7, SenderID: 10, SenderName: "检索用户", Text: "更新后的原文", ContentHash: "updated-hash", SourceType: "telegram_edited_message"})
	must(e)
	rows, e = q.SearchAssistantSemantic(ctx, -51001, &thread, "fake:embedding", []float64{1, 0, 0}, 8)
	must(e)
	if len(rows) != 0 {
		t.Fatal("edited source vector survived")
	}
	if e = q.PutAssistantMessageVector(ctx, chinese, "fake:embedding", []float64{1, 0, 0}); e == nil {
		t.Fatal("stale indexing job committed after edit")
	}
	must(q.PutAssistantMessageVector(ctx, english, "fake:embedding", []float64{0, 1, 0}))
	must(q.InvalidateGroupAssistantSource(ctx, english.ChatID, english.TelegramMessageID))
	rows, e = q.SearchAssistantLexical(ctx, -51001, &thread, "schedule deploy", 8)
	must(e)
	if len(rows) != 0 {
		t.Fatal("blocked source recalled")
	}
	forgotten := msg(-51001, 9, "forgottenneedle", time.Now().Add(time.Hour))
	sid := forgotten.TelegramMessageID
	memory, e := q.CreateGroupAssistantMemory(ctx, CreateGroupAssistantMemoryParams{ChatID: -51001, Subject: "事实", Content: "forgottenneedle", MemoryType: "learned", AuthorityLevel: "learned_fact", ValidScope: "today", SourceType: "telegram_approved_message", SourceMessageID: &sid, SourceVerified: "verified", ExpiresAt: time.Now().Add(time.Hour), DedupeHash: "forget-source"})
	must(e)
	must(q.PutAssistantMessageVector(ctx, forgotten, "fake:embedding", []float64{0, 0, 1}))
	must(q.ForgetGroupAssistantMemory(ctx, memory.ID, memory.ChatID, 100))
	rows, e = q.SearchAssistantLexical(ctx, -51001, &thread, "forgottenneedle", 8)
	must(e)
	if len(rows) != 0 {
		t.Fatal("forgotten source lexical resurrection")
	}
	rows, e = q.SearchAssistantSemantic(ctx, -51001, &thread, "fake:embedding", []float64{0, 0, 1}, 8)
	must(e)
	if len(rows) != 0 {
		t.Fatal("forgotten source vector resurrection")
	}
	exec(`UPDATE group_assistant_pools SET strategy='weighted' WHERE chat_id=-51001`)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if e = q.PutAssistantMessageVector(canceled, forgotten, "fake:cancel", []float64{1}); e == nil {
		t.Fatal("canceled index committed")
	}
	t.Log(fmt.Sprintf("PASS goose34→35 policy/original/override/learned-memory/source/expiry/forgotten-state/versions/pool rows preserved; Chinese+English GIN, persisted vector, topic/group/expiry/edit/block/forget/cancel assertions; old merge=%v", merge))
}
