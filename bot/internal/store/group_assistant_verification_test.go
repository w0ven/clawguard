package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Only the disposable assistant-test database is accepted. Never load application configuration.
func TestAssistantVerificationPostgres(t *testing.T) {
	dsn := os.Getenv("ASSISTANT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BLOCKED: use backend-verification/run-postgres.sh to provision dedicated PostgreSQL")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Path != "/assistant_test" || u.User.Username() != "assistant_test" {
		t.Fatal("refusing non-task database")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = goose.UpToContext(ctx, db, "../../migrations", 31); err != nil {
		t.Fatal("baseline migrations:", err)
	}
	var n int
	if err = db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_name='group_assistant_policies'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("baseline not 31: n=%d err=%v", n, err)
	}
	if err = goose.UpToContext(ctx, db, "../../migrations", 32); err != nil {
		t.Fatal("migration 32:", err)
	}
	t.Log("PASS: actual goose 00001..00031 then 00032 on disposable PostgreSQL")
	var hashType string
	if err = db.QueryRow(`SELECT data_type FROM information_schema.columns WHERE table_name='group_assistant_memories' AND column_name='source_content_hash'`).Scan(&hashType); err != nil || hashType != "text" {
		t.Fatalf("new source_content_hash schema missing: %s %v", hashType, err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mk := func(chat int64, key string) GroupAssistantMemory {
		t.Helper()
		m, e := q.CreateGroupAssistantMemory(ctx, CreateGroupAssistantMemoryParams{ChatID: chat, Subject: key, Content: "original", MemoryType: "learned", AuthorityLevel: "learned_fact", ValidScope: "today", SourceType: "telegram_message", SourceVerified: "verified", SourceSnippet: "original", ExpiresAt: time.Now().Add(time.Hour), DedupeHash: key})
		must(e)
		return m
	}
	upd := func(m GroupAssistantMemory) UpdateGroupAssistantMemoryParams {
		return UpdateGroupAssistantMemoryParams{ID: m.ID, ChatID: m.ChatID, ExpectedVersion: m.Version, Subject: m.Subject, Content: "changed", MemoryType: m.MemoryType, AuthorityLevel: m.AuthorityLevel, ValidScope: m.ValidScope, SourceType: m.SourceType, SourceVerified: m.SourceVerified, ExpiresAt: m.ExpiresAt, DedupeHash: m.DedupeHash}
	}
	t.Run("DefaultDisabledNoPolicyWrite", func(t *testing.T) {
		_, e := q.GetGroupAssistantPolicy(ctx, -1001)
		if !errors.Is(e, pgx.ErrNoRows) {
			t.Fatal(e)
		}
		var n int
		must(pool.QueryRow(ctx, "SELECT count(*) FROM group_assistant_policies").Scan(&n))
		if n != 0 {
			t.Fatal(n)
		}
	})
	t.Run("PolicyAndPoolCASArraysJSON", func(t *testing.T) {
		p := UpsertGroupAssistantPolicyParams{ChatID: -1001, TriggerMode: "mention_or_reply", FollowupWindowSec: 300, MaxFollowupTurns: 5, Temperature: .3, HistoryLimit: 30, RetentionDays: 7, CollectionPolicy: "history_7d_and_long_term_summary", ToolAllowlist: []string{"knowledge_query"}, AllowDomains: []string{"example.org"}, MaxQueueDepth: 10, MaxQueueWaitSec: 15}
		v, e := q.UpsertGroupAssistantPolicy(ctx, p)
		if e != nil {
			t.Fatal(e)
		}
		if v.Version != 1 || v.RetentionDays != 7 || len(v.AllowDomains) != 1 {
			t.Fatalf("bad scan: %+v", v)
		}
		if _, e = q.UpsertGroupAssistantPolicy(ctx, p); !errors.Is(e, pgx.ErrNoRows) {
			t.Fatalf("stale create: %v", e)
		}
		p.ExpectedVersion = 1
		p.ChatEnabled = true
		v, e = q.UpsertGroupAssistantPolicy(ctx, p)
		if e != nil || v.Version != 2 || !v.ChatEnabled {
			t.Fatalf("CAS update %+v %v", v, e)
		}
		if _, e = q.UpsertGroupAssistantPolicy(ctx, p); !errors.Is(e, pgx.ErrNoRows) {
			t.Fatalf("stale update: %v", e)
		}
		pp := UpsertGroupAssistantPoolParams{ChatID: -1001, Strategy: "primary-overflow", Config: []byte(`{"task_assignments":{},"endpoints":[]}`)}
		pv, e := q.UpsertGroupAssistantPool(ctx, pp)
		if e != nil || pv.Version != 1 || !strings.Contains(string(pv.Config), "endpoints") {
			t.Fatalf("pool %+v %v", pv, e)
		}
		if _, e = q.UpsertGroupAssistantPool(ctx, pp); !errors.Is(e, pgx.ErrNoRows) {
			t.Fatal("pool stale create", e)
		}
		pp.ExpectedVersion = 1
		pv, e = q.UpsertGroupAssistantPool(ctx, pp)
		if e != nil || pv.Version != 2 {
			t.Fatalf("pool update %+v %v", pv, e)
		}
	})
	t.Run("ConcurrentMemoryCASAndVersionChain", func(t *testing.T) {
		m := mk(-1002, "cas")
		var wins atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, e := q.UpdateGroupAssistantMemory(ctx, upd(m))
				if e == nil {
					wins.Add(1)
				} else if !errors.Is(e, pgx.ErrNoRows) {
					t.Errorf("CAS: %v", e)
				}
			}()
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("winners=%d", wins.Load())
		}
		vv, e := q.ListGroupAssistantMemoryVersions(ctx, m.ID, m.ChatID)
		if e != nil || len(vv) != 2 || vv[0].Version != 2 || vv[1].Version != 1 {
			t.Fatalf("versions %+v %v", vv, e)
		}
	})
	t.Run("VersionFailureRollsBackMemoryTransaction", func(t *testing.T) {
		m := mk(-1003, "rollback")
		_, e := pool.Exec(ctx, `INSERT INTO group_assistant_memory_versions(memory_id,version,content,memory_type,authority_level,source_type) VALUES($1,2,'sentinel','learned','learned_fact','telegram_message')`, m.ID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = q.UpdateGroupAssistantMemory(ctx, upd(m)); e == nil {
			t.Fatal("expected duplicate-version transaction failure")
		}
		got, e := q.GetGroupAssistantMemory(ctx, m.ID, m.ChatID)
		if e != nil || got.Version != 1 || got.Content != "original" {
			t.Fatalf("non-atomic update %+v %v", got, e)
		}
	})
	t.Run("GroupIsolation", func(t *testing.T) {
		m := mk(-1004, "scope")
		p := upd(m)
		p.ChatID = -1005
		if _, e := q.UpdateGroupAssistantMemory(ctx, p); !errors.Is(e, pgx.ErrNoRows) {
			t.Fatalf("cross-group UPDATE allowed: %v", e)
		}
		got, e := q.GetGroupAssistantMemory(ctx, m.ID, m.ChatID)
		if e != nil || got.Version != m.Version || got.Content != m.Content {
			t.Fatalf("cross-group UPDATE changed source %+v %v", got, e)
		}
		if _, e := q.GetGroupAssistantMemory(ctx, m.ID, -1005); !errors.Is(e, pgx.ErrNoRows) {
			t.Fatal("cross group memory", e)
		}
		v, e := q.ListGroupAssistantMemoryVersions(ctx, m.ID, -1005)
		if e != nil || len(v) != 0 {
			t.Fatalf("cross group versions %+v %v", v, e)
		}
		if e = q.ForgetGroupAssistantMemory(ctx, m.ID, -1005, 1); !errors.Is(e, pgx.ErrNoRows) {
			t.Fatal("cross group forget", e)
		}
	})
	t.Run("ForgetRepeatAndNoRecall", func(t *testing.T) {
		m := mk(-1006, "forget")
		if e := q.ForgetGroupAssistantMemory(ctx, m.ID, m.ChatID, 1); e != nil {
			t.Fatal(e)
		}
		if e := q.ForgetGroupAssistantMemory(ctx, m.ID, m.ChatID, 1); !errors.Is(e, pgx.ErrNoRows) {
			t.Fatal("repeat forget", e)
		}
		if _, e := q.UpdateGroupAssistantMemory(ctx, upd(m)); !errors.Is(e, pgx.ErrNoRows) {
			t.Fatal("forgotten update", e)
		}
		v, e := q.ListGroupAssistantMemories(ctx, m.ChatID, false, "", 100)
		if e != nil || len(v) != 0 {
			t.Fatalf("recall %+v %v", v, e)
		}
		vv, e := q.ListGroupAssistantMemoryVersions(ctx, m.ID, m.ChatID)
		if e != nil || len(vv) != 2 || vv[0].ChangeKind != "forget" {
			t.Fatalf("audit %+v %v", vv, e)
		}
	})
	t.Run("ConflictAcceptRejectScopeAndVersions", func(t *testing.T) {
		for _, accept := range []bool{false, true} {
			m := mk(-1007, fmt.Sprint("conflict", accept))
			c, e := q.CreateGroupAssistantConflict(ctx, CreateGroupAssistantConflictParams{ChatID: m.ChatID, MemoryID: &m.ID, Subject: m.Subject, CandidateContent: "candidate", CandidateAuthority: "learned_fact", CandidateScope: "today", SourceType: "telegram_message"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = q.ResolveGroupAssistantConflict(ctx, c.ID, -9999, 1, accept); !errors.Is(e, pgx.ErrNoRows) {
				t.Fatal("cross-group resolve", e)
			}
			got, e := q.ResolveGroupAssistantConflictWithOptions(ctx, ResolveGroupAssistantConflictParams{ID: c.ID, ChatID: m.ChatID, ActorID: 1, ActorName: "test-admin", Accept: accept, ExpectedMemoryVersion: m.Version, ResolutionMode: "admin_explicit_correction"})
			if e != nil {
				t.Fatal(e)
			}
			mm, e := q.GetGroupAssistantMemory(ctx, m.ID, m.ChatID)
			if e != nil {
				t.Fatal(e)
			}
			vv, e := q.ListGroupAssistantMemoryVersions(ctx, m.ID, m.ChatID)
			if e != nil {
				t.Fatal(e)
			}
			if accept {
				if got.Status != "accepted" || mm.Content != "candidate" || mm.AuthorityLevel != "admin_explicit" || mm.SourceType != "admin_conflict_accept" || mm.SourceOperatorID == nil || *mm.SourceOperatorID != 1 || mm.SourceOperatorName != "test-admin" || len(vv) != 2 || !mm.ExpiresAt.Equal(m.ExpiresAt) {
					t.Fatalf("accept %+v %+v %+v", got, mm, vv)
				}
			} else if got.Status != "rejected" || mm.Content != "original" || len(vv) != 1 {
				t.Fatalf("reject %+v %+v", got, mm)
			}
		}
	})
	t.Run("PendingConflictCannotUndoForget", func(t *testing.T) {
		m := mk(-1008, "forgotten-conflict")
		c, e := q.CreateGroupAssistantConflict(ctx, CreateGroupAssistantConflictParams{ChatID: m.ChatID, MemoryID: &m.ID, Subject: m.Subject, CandidateContent: "resurrect", CandidateAuthority: "learned_fact", SourceType: "telegram_message"})
		if e != nil {
			t.Fatal(e)
		}
		if e = q.ForgetGroupAssistantMemory(ctx, m.ID, m.ChatID, 1); e != nil {
			t.Fatal(e)
		}
		_, e = q.ResolveGroupAssistantConflictWithOptions(ctx, ResolveGroupAssistantConflictParams{ID: c.ID, ChatID: m.ChatID, ActorID: 1, Accept: true, ExpectedMemoryVersion: m.Version, ResolutionMode: "admin_explicit_correction"})
		if !errors.Is(e, ErrGroupAssistantConflictMemoryGone) {
			t.Errorf("forgotten conflict should reject: %v", e)
		}
		got, e := q.GetGroupAssistantMemory(ctx, m.ID, m.ChatID)
		if e != nil {
			t.Fatal(e)
		}
		if got.Active || got.ForgottenAt == nil {
			t.Errorf("forgotten tombstone resurrected: active=%v forgotten_at=%v version=%d", got.Active, got.ForgottenAt, got.Version)
		}
		v, e := q.ListGroupAssistantMemories(ctx, m.ChatID, false, "", 100)
		if e != nil || len(v) != 0 {
			t.Errorf("forgotten effective recall %+v %v", v, e)
		}
	})
	t.Run("HistoryApprovedDeliveredExpiryIdempotencyAndScope", func(t *testing.T) {
		base := UpsertGroupAssistantMessageParams{ChatID: -1009, ThreadID: 4, TelegramMessageID: 1, SenderID: 8, Role: "user", Text: "original", Approved: true, Delivered: true, ContentHash: "h", ExpiresAt: time.Now().Add(7 * 24 * time.Hour), SourceType: "telegram_message"}
		a, e := q.UpsertGroupAssistantMessage(ctx, base)
		if e != nil {
			t.Fatal(e)
		}
		b, e := q.UpsertGroupAssistantMessage(ctx, base)
		if e != nil || a.ID != b.ID {
			t.Fatalf("duplicate %v %v", b, e)
		}
		for i := 2; i <= 5; i++ {
			p := base
			p.TelegramMessageID = int64(i)
			switch i {
			case 2:
				p.Approved = false
			case 3:
				p.Delivered = false
			case 4:
				p.ExpiresAt = time.Now().Add(-time.Second)
			case 5:
				p.ChatID = -1010
			}
			if _, e = q.UpsertGroupAssistantMessage(ctx, p); e != nil {
				t.Fatal(e)
			}
		}
		v, e := q.ListGroupAssistantMessages(ctx, ListGroupAssistantMessagesParams{ChatID: base.ChatID, ThreadID: &base.ThreadID, SenderID: &base.SenderID})
		if e != nil || len(v) != 1 || v[0].ID != a.ID {
			t.Fatalf("history %+v %v", v, e)
		}
		if e = q.PruneGroupAssistantMessages(ctx); e != nil {
			t.Fatal(e)
		}
		var count int
		if e = pool.QueryRow(ctx, "SELECT count(*) FROM group_assistant_messages WHERE expires_at<=NOW()").Scan(&count); e != nil || count != 0 {
			t.Fatalf("prune %d %v", count, e)
		}
	})
	t.Run("EditedHistoryMustNotExtendOriginalRetention", func(t *testing.T) {
		p := UpsertGroupAssistantMessageParams{ChatID: -1011, TelegramMessageID: 1, SenderID: 8, Role: "user", Text: "old", Approved: true, Delivered: true, ContentHash: "old", ExpiresAt: time.Now().Add(time.Hour), SourceType: "telegram_message"}
		old, e := q.UpsertGroupAssistantMessage(ctx, p)
		if e != nil {
			t.Fatal(e)
		}
		p.Text = "edited"
		p.ContentHash = "new"
		p.ExpiresAt = time.Now().Add(7 * 24 * time.Hour)
		got, e := q.UpsertGroupAssistantMessage(ctx, p)
		if e != nil {
			t.Fatal(e)
		}
		if got.ID != old.ID || got.Text != "edited" {
			t.Fatalf("bad edit %+v", got)
		}
		if got.ExpiresAt.After(old.ExpiresAt) {
			t.Errorf("edit extends raw retention from %s to %s", old.ExpiresAt, got.ExpiresAt)
		}
	})
	t.Run("DispatchScopeAndScan", func(t *testing.T) {
		ms := int32(17)
		d, e := q.InsertGroupAssistantDispatch(ctx, CreateGroupAssistantDispatchParams{ChatID: -1012, RequestID: "fake", TaskType: "chat", EndpointID: "main", ModelRef: "fake:model", Reason: "primary", Status: "success", LatencyMs: &ms})
		if e != nil {
			t.Fatal(e)
		}
		v, e := q.ListGroupAssistantDispatches(ctx, -1012, 10)
		if e != nil || len(v) != 1 || v[0].ID != d.ID || *v[0].LatencyMs != 17 {
			t.Fatalf("dispatch %+v %v", v, e)
		}
		v, e = q.ListGroupAssistantDispatches(ctx, -1013, 10)
		if e != nil || len(v) != 0 {
			t.Fatalf("cross group dispatch %+v %v", v, e)
		}
	})
	t.Run("MissingChatIDMustNotInferForeignScope", func(t *testing.T) {
		m := mk(-1030, "missing-scope")
		p := upd(m)
		p.ChatID = 0
		if _, e := q.UpdateGroupAssistantMemory(ctx, p); e == nil {
			t.Fatal("scope-less UPDATE inferred target group and succeeded")
		}
	})
	t.Run("ManualConflictCASAndOrdinaryCannotOverrideAuthority", func(t *testing.T) {
		m := mk(-1031, "authority")
		p := upd(m)
		p.AuthorityLevel = "admin_base"
		p.MemoryType = "base"
		m, e := q.UpdateGroupAssistantMemory(ctx, p)
		if e != nil {
			t.Fatal(e)
		}
		sourceChat := int64(-1031)
		sourceMsg := int64(80)
		c, e := q.CreateGroupAssistantConflict(ctx, CreateGroupAssistantConflictParams{ChatID: m.ChatID, MemoryID: &m.ID, Subject: m.Subject, CandidateContent: "candidate", CandidateScope: "today", CandidateAuthority: "learned_fact", SourceType: "telegram_approved_message", SourceChatID: &sourceChat, SourceMessageID: &sourceMsg, SourceSnippet: "candidate quote"})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = q.ResolveGroupAssistantConflict(ctx, c.ID, m.ChatID, 7, true); !errors.Is(e, ErrGroupAssistantConflictAuthority) {
			t.Fatalf("ordinary candidate overrode admin: %v", e)
		}
		opts := ResolveGroupAssistantConflictParams{ID: c.ID, ChatID: m.ChatID, ActorID: 7, ActorName: "real test admin", Accept: true, ExpectedMemoryVersion: 1, ResolutionMode: "admin_explicit_correction"}
		if _, e = q.ResolveGroupAssistantConflictWithOptions(ctx, opts); !errors.Is(e, ErrGroupAssistantConflictMemoryChanged) {
			t.Fatalf("stale conflict CAS: %v", e)
		}
		opts.ExpectedMemoryVersion = m.Version
		if _, e = q.ResolveGroupAssistantConflictWithOptions(ctx, opts); e != nil {
			t.Fatal(e)
		}
		got, e := q.GetGroupAssistantMemory(ctx, m.ID, m.ChatID)
		if e != nil || got.AuthorityLevel != "admin_explicit" || got.SourceType != "admin_conflict_accept" || got.SourceOperatorID == nil || *got.SourceOperatorID != 7 || got.SourceChatID == nil || *got.SourceChatID != sourceChat || got.SourceMessageID == nil || *got.SourceMessageID != sourceMsg || got.SourceSnippet != "candidate quote" {
			t.Fatalf("manual attribution %+v %v", got, e)
		}
	})
	t.Run("ExistingApprovedEditAndInvalidationTransaction", func(t *testing.T) {
		// Fixture is inserted by explicit SQL to independently exercise UPDATE/invalidation even if production UPSERT is broken.
		chat, msg := int64(-1040), int64(1)
		exp := time.Now().Add(time.Hour)
		oldSum, newSum := sha256.Sum256([]byte("old")), sha256.Sum256([]byte("new"))
		oldHash, newHash := hex.EncodeToString(oldSum[:]), hex.EncodeToString(newSum[:])
		_, e := pool.Exec(ctx, `INSERT INTO group_assistant_messages(chat_id,telegram_message_id,role,text,content_hash,expires_at) VALUES($1,$2,'user','old',$3,$4)`, chat, msg, oldHash, exp)
		if e != nil {
			t.Fatal(e)
		}
		m, e := q.CreateGroupAssistantMemory(ctx, CreateGroupAssistantMemoryParams{ChatID: chat, Subject: "derived", Content: "old fact", MemoryType: "learned", AuthorityLevel: "learned_fact", ValidScope: "today", SourceType: "telegram_approved_message", SourceVerified: "verified", SourceMessageID: &msg, SourceChatID: &chat, SourceContentHash: oldHash, ExpiresAt: exp, DedupeHash: "derived"})
		if e != nil {
			t.Fatal(e)
		}
		p := upd(m)
		p.SourceMessageID = &msg
		p.SourceContentHash = "wrong"
		if _, e = q.UpdateGroupAssistantMemory(ctx, p); !errors.Is(e, ErrGroupAssistantSourceInvalid) {
			t.Fatalf("bad quotehash accepted %v", e)
		}
		edited, e := q.UpdateGroupAssistantMessage(ctx, UpdateGroupAssistantMessageParams{ChatID: chat, TelegramMessageID: msg, Text: "new", ContentHash: newHash})
		if e != nil || edited.ExpiresAt.After(exp) {
			t.Fatalf("edit %+v %v", edited, e)
		}
		if _, e = q.UpdateGroupAssistantMessage(ctx, UpdateGroupAssistantMessageParams{ChatID: chat, TelegramMessageID: 99, Text: "unreviewed"}); !errors.Is(e, pgx.ErrNoRows) {
			t.Fatalf("new edit source inserted: %v", e)
		}
		if e = q.InvalidateGroupAssistantSource(ctx, chat, msg); e != nil {
			t.Fatal(e)
		}
		hist, e := q.ListGroupAssistantMessages(ctx, ListGroupAssistantMessagesParams{ChatID: chat})
		if e != nil || len(hist) != 0 {
			t.Fatalf("invalid history %+v %v", hist, e)
		}
		facts, e := q.ListGroupAssistantMemories(ctx, chat, false, "", 100)
		if e != nil || len(facts) != 0 {
			t.Fatalf("invalid facts %+v %v", facts, e)
		}
		p.SourceContentHash = newHash
		if _, e = q.UpdateGroupAssistantMemory(ctx, p); !errors.Is(e, ErrGroupAssistantSourceInvalid) {
			t.Fatalf("invalidated source accepted %v", e)
		}
	})

}
