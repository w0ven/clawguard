package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/pressly/goose/v3"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type assistantAPITransport func(*http.Request) (*http.Response, error)

func (f assistantAPITransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type assistantAPIModels struct{}

func (assistantAPIModels) Get(ref ai.ModelRef) (ai.Model, bool) {
	if ref == "fake:tools" {
		return ai.Model{ProviderKey: "fake", ModelKey: "tools", Enabled: true, SupportsTools: true}, true
	}
	if ref == "fake:text" {
		return ai.Model{ProviderKey: "fake", ModelKey: "text", Enabled: true}, true
	}
	if ref == "fake:disabled" {
		return ai.Model{ProviderKey: "fake", ModelKey: "disabled", Enabled: false, SupportsTools: true}, true
	}
	return ai.Model{}, false
}
func (assistantAPIModels) List(ai.ModelFilter) []ai.Model { return nil }
func (assistantAPIModels) Reload(context.Context) error   { return nil }

type assistantAPIProviders struct{}

func (assistantAPIProviders) GetByKey(key string) (ai.Provider, bool) {
	return ai.Provider{Key: "fake", BaseURL: "https://must-not-contact.invalid", APIKey: "assistant-test-never-expose", Enabled: true}, key == "fake"
}
func (assistantAPIProviders) List() []ai.Provider                { return nil }
func (assistantAPIProviders) Client(string) (ai.LLMClient, bool) { return nil, false }
func (assistantAPIProviders) Reload(context.Context) error       { return nil }

func TestAssistantVerificationAPIPostgres(t *testing.T) {
	dsn := os.Getenv("ASSISTANT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BLOCKED: dedicated assistant-test PG required")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Hostname() != "127.0.0.1" || u.Path != "/assistant_test" || u.User.Username() != "assistant_test" {
		t.Fatal("refusing non-task database")
	}
	ctx := context.Background()
	sqlDB, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer sqlDB.Close()
	if e = goose.UpContext(ctx, sqlDB, "../../migrations"); e != nil {
		t.Fatal(e)
	}
	db, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	q := store.New(db)
	for _, chat := range []int64{-3001, -3002} {
		if _, e = db.Exec(ctx, `INSERT INTO groups(chat_id,title,type) VALUES($1,'assistant-test','supergroup')`, chat); e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, `INSERT INTO authorized_groups(chat_id) VALUES($1)`, chat); e != nil {
			t.Fatal(e)
		}
	}
	var id int64
	if e = db.QueryRow(ctx, `INSERT INTO admins(telegram_id,username,role,group_scope) VALUES(77,'fixture-admin','admin','[-3001]') RETURNING id`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	// All default HTTP requests terminate here; no packet can reach Telegram or provider.
	old := http.DefaultTransport
	http.DefaultTransport = assistantAPITransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Errorf("unexpected external request intercepted: %s", r.URL.Path)
			return nil, fmt.Errorf("unapproved HTTP intercepted")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":900,"is_bot":true,"first_name":"Test","username":"assistant_test_bot"}}`)), Request: r}, nil
	})
	defer func() { http.DefaultTransport = old }()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	cfg := config.Config{BotToken: "123:ASSISTANT_TEST_FAKE", JWTSecret: "assistant-test-jwt-secret"}
	svc, e := bot.New(ctx, cfg, zap.NewNop(), q, rdb, assistantAPIProviders{}, assistantAPIModels{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer svc.Stop()
	s := NewServer(cfg, zap.NewNop(), svc, nil)
	token, e := jwt.NewWithClaims(jwt.SigningMethodHS256, adminClaims{Role: "admin", TgID: 77, RegisteredClaims: jwt.RegisteredClaims{Subject: fmt.Sprint(id), ID: "assistant-test-jti", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(cfg.JWTSecret))
	if e != nil {
		t.Fatal(e)
	}
	request := func(t *testing.T, method, path, body string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "/api/admin/groups/-3001/assistant"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, rec.Code, want, rec.Body)
		}
		var out map[string]any
		if e := json.Unmarshal(rec.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		for _, secret := range []string{"assistant-test-never-expose", "must-not-contact.invalid", `"api_key"`, `"base_url"`} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("secret/endpoint leak: %s", rec.Body)
			}
		}
		return out
	}
	t.Run("DefaultsReadWithoutWrites", func(t *testing.T) {
		out := request(t, "GET", "", "", 200)
		p := out["policy"].(map[string]any)
		if p["chat_enabled"] != false || p["learning_enabled"] != false || p["version"] != float64(0) {
			t.Fatal(out)
		}
		var n int
		if e := db.QueryRow(ctx, "SELECT count(*) FROM group_assistant_policies").Scan(&n); e != nil || n != 0 {
			t.Fatal("GET wrote policy", n, e)
		}
	})
	policy := `{"expected_version":0,"chat_enabled":true,"learning_enabled":false,"trigger_mode":"mention_or_reply","followup_window_sec":30,"max_followup_turns":5,"chat_model_ref":"fake:tools","learning_model_ref":"fake:text","temperature":0.3,"history_limit":30,"retention_days":7,"tool_allowlist":["knowledge_query","conversation_recall","webfetch_readonly"],"allow_domains":["example.org"],"max_queue_depth":2,"max_queue_wait_sec":1}`
	t.Run("PolicyRegistryStrictCAS", func(t *testing.T) {
		for _, model := range []string{"fake:missing", "fake:disabled", "fake:text"} {
			request(t, "PUT", "", strings.Replace(policy, `"chat_model_ref":"fake:tools"`, `"chat_model_ref":"`+model+`"`, 1), 400)
		}
		request(t, "PUT", "", policy+` {}`, 400)
		request(t, "PUT", "", strings.Replace(policy, `"expected_version":0`, `"expected_version":0,"chat_id":-3002`, 1), 400)
		request(t, "PUT", "", policy, 200)
		request(t, "PUT", "", policy, 409)
		request(t, "PUT", "", strings.Replace(policy, `"expected_version":0`, `"expected_version":1`, 1), 200)
	})
	pool := `{"expected_version":0,"strategy":"primary-overflow","task_assignments":{"chat":{"primary":"main","backups":[]},"learning":{"primary":"learn","backups":[]}},"endpoints":[{"id":"main","model_ref":"fake:tools","role":"primary","priority":0,"max_concurrency":2,"timeout_ms":1000,"cooldown_duration_sec":1},{"id":"learn","model_ref":"fake:text","role":"primary","priority":1,"max_concurrency":1,"timeout_ms":1000,"cooldown_duration_sec":1}],"max_queue_depth":2,"max_queue_wait_sec":1}`
	t.Run("PoolToolsStatusDispatchHistory", func(t *testing.T) {
		request(t, "PUT", "/model-pool", pool, 200)
		request(t, "PUT", "/model-pool", pool, 409)
		for _, path := range []string{"/model-pool", "/tools", "/status", "/history", "/dispatches", "/conflicts"} {
			request(t, "GET", path, "", 200)
		}
	})
	create := func(t *testing.T, subject string) int64 {
		out := request(t, "POST", "/memories", fmt.Sprintf(`{"subject":%q,"content":"base fact","valid_scope":"本周","source_type":"admin_base"}`, subject), http.StatusCreated)
		m := out["memory"].(map[string]any)
		if m["valid_scope"] != "this_week" {
			t.Fatal("scope not normalized", m)
		}
		return int64(m["id"].(float64))
	}
	t.Run("MemoryCRUDCrossGroup404CAS409SourceForgery", func(t *testing.T) {
		mid := create(t, "base")
		path := fmt.Sprintf("/memories/%d", mid)
		request(t, "GET", path, "", 200)
		request(t, "GET", path+"/versions", "", 200)
		foreign, e := q.CreateGroupAssistantMemory(ctx, store.CreateGroupAssistantMemoryParams{ChatID: -3002, Subject: "foreign", Content: "do not change", MemoryType: "base", AuthorityLevel: "admin_base", ValidScope: "current_group", SourceType: "admin_base", SourceVerified: "verified", ExpiresAt: time.Now().Add(time.Hour), DedupeHash: "foreign"})
		if e != nil {
			t.Fatal(e)
		}
		body := `{"expected_version":1,"subject":"base","content":"corrected","valid_scope":"current_group","source_type":"admin_base"}`
		foreignPath := fmt.Sprintf("/memories/%d", foreign.ID)
		request(t, "PUT", foreignPath, body, 404)
		request(t, "GET", foreignPath, "", 404)
		t.Run("CrossGroupVersions404", func(t *testing.T) { request(t, "GET", foreignPath+"/versions", "", 404) })
		got, e := q.GetGroupAssistantMemory(ctx, foreign.ID, -3002)
		if e != nil || got.Content != foreign.Content || got.Version != foreign.Version {
			t.Fatal("foreign mutation", got, e)
		}
		for _, field := range []string{`"source_message_id":999`, `"source_operator_id":8`, `"chat_id":-3002`, `"authority_level":"pinned_announcement"`} {
			t.Run("Refuse:"+field, func(t *testing.T) {
				request(t, "POST", "/memories", `{"subject":"forged","content":"bad","valid_scope":"today",`+field+`}`, 400)
			})
		}
		request(t, "PUT", path, body, 200)
		request(t, "PUT", path, body, 409)
		out := request(t, "POST", path+"/forget", `{}`, 200)
		if out["remote_telegram_deleted"] != false {
			t.Fatal("forget overclaimed", out)
		}
		request(t, "POST", path+"/forget", `{}`, 404)
	})
	t.Run("ManualConflictAttributionCASAndForget", func(t *testing.T) {
		mid := create(t, "conflict")
		sourceChat, sourceMessage := int64(-3001), int64(123)
		c, e := q.CreateGroupAssistantConflict(ctx, store.CreateGroupAssistantConflictParams{ChatID: -3001, MemoryID: &mid, Subject: "conflict", CandidateContent: "candidate", CandidateScope: "this_week", CandidateAuthority: "learned_fact", SourceType: "telegram_approved_message", SourceMessageID: &sourceMessage, SourceChatID: &sourceChat, SourceSnippet: "original member quote"})
		if e != nil {
			t.Fatal(e)
		}
		path := fmt.Sprintf("/conflicts/%d/resolve", c.ID)
		request(t, "POST", path, `{"accept":true}`, 400)
		request(t, "POST", path, `{"accept":true,"expected_memory_version":9,"resolution_mode":"admin_explicit_correction"}`, 409)
		request(t, "POST", path, `{"accept":true,"expected_memory_version":1,"resolution_mode":"admin_explicit_correction"}`, 200)
		m, e := q.GetGroupAssistantMemory(ctx, mid, -3001)
		if e != nil || m.AuthorityLevel != "admin_explicit" || m.SourceType != "admin_conflict_accept" || m.SourceOperatorID == nil || *m.SourceOperatorID != 77 || m.SourceOperatorName != "fixture-admin" || m.SourceChatID == nil || *m.SourceChatID != sourceChat || m.SourceMessageID == nil || *m.SourceMessageID != sourceMessage {
			t.Fatalf("bad admin attribution %+v %v", m, e)
		}
		c, e = q.CreateGroupAssistantConflict(ctx, store.CreateGroupAssistantConflictParams{ChatID: -3001, MemoryID: &mid, Subject: "conflict", CandidateContent: "resurrect", CandidateScope: "this_week", CandidateAuthority: "learned_fact", SourceType: "telegram_approved_message"})
		if e != nil {
			t.Fatal(e)
		}
		request(t, "POST", fmt.Sprintf("/memories/%d/forget", mid), `{}`, 200)
		request(t, "POST", fmt.Sprintf("/conflicts/%d/resolve", c.ID), `{"accept":true,"expected_memory_version":2,"resolution_mode":"admin_explicit_correction"}`, 409)
		request(t, "POST", fmt.Sprintf("/memories/%d/forget", mid), `{}`, 404)
	})
	t.Run("PopulatedHistoryLiteralSearchAndDispatch", func(t *testing.T) {
		for i, text := range []string{"sale 50%", "sale everything", "literal_under"} {
			if _, e := db.Exec(ctx, `INSERT INTO group_assistant_messages(chat_id,telegram_message_id,role,text,content_hash,expires_at) VALUES(-3001,$1,'user',$2,'fixture',NOW()+INTERVAL '7 days')`, i+1, text); e != nil {
				t.Fatal(e)
			}
		}
		if _, e := db.Exec(ctx, `INSERT INTO group_assistant_messages(chat_id,telegram_message_id,role,text,content_hash,expires_at) VALUES(-3002,1,'user','sale 50% other group','fixture',NOW()+INTERVAL '7 days'),(-3001,99,'user','sale 50% expired','fixture',NOW()-INTERVAL '1 day')`); e != nil {
			t.Fatal(e)
		}
		out := request(t, "GET", "/history?q=%25", "", 200)
		items := out["history"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["text"] != "sale 50%" {
			t.Fatalf("literal history expanded %+v", items)
		}
		for _, chat := range []int64{-3001, -3002} {
			if _, e := q.InsertGroupAssistantDispatch(ctx, store.CreateGroupAssistantDispatchParams{ChatID: chat, TaskType: "chat", EndpointID: "main", Status: "success", Reason: "primary_selected"}); e != nil {
				t.Fatal(e)
			}
		}
		out = request(t, "GET", "/dispatches", "", 200)
		if len(out["dispatches"].([]any)) != 1 {
			t.Fatal("dispatch group leak", out)
		}
	})
	t.Run("TelegramMemoryEditRequiresSavedSourceHash", func(t *testing.T) {
		chat, msg := int64(-3001), int64(701)
		text := "original quote"
		sum := sha256.Sum256([]byte(text))
		hash := hex.EncodeToString(sum[:])
		if _, e := q.UpsertGroupAssistantMessage(ctx, store.UpsertGroupAssistantMessageParams{ChatID: chat, TelegramMessageID: msg, Role: "user", Text: text, ContentHash: hash, Approved: true, Delivered: true, ExpiresAt: time.Now().Add(7 * 24 * time.Hour), SourceType: "telegram_approved_message"}); e != nil {
			t.Fatal(e)
		}
		m, e := q.CreateGroupAssistantMemory(ctx, store.CreateGroupAssistantMemoryParams{ChatID: chat, Subject: "telegram-source", Content: "original derived", MemoryType: "learned", AuthorityLevel: "learned_fact", ValidScope: "today", SourceType: "telegram_approved_message", SourceMessageID: &msg, SourceChatID: &chat, SourceSnippet: text, SourceContentHash: hash, SourceVerified: "verified", ExpiresAt: time.Now().Add(time.Hour), DedupeHash: "api-source-check"})
		if e != nil {
			t.Fatal(e)
		}
		if m.SourceContentHash != hash {
			t.Fatal("saved hash missing from memory scan")
		}
		path := fmt.Sprintf("/memories/%d", m.ID)
		request(t, "PUT", path, `{"expected_version":1,"subject":"telegram-source","content":"admin corrected","valid_scope":"today"}`, 200)
		got, e := q.GetGroupAssistantMemory(ctx, m.ID, chat)
		if e != nil || got.Version != 2 || got.SourceContentHash != hash || got.SourceMessageID == nil || *got.SourceMessageID != msg || got.SourceChatID == nil || *got.SourceChatID != chat {
			t.Fatalf("unchanged source successful edit lost verification %+v %v", got, e)
		}
		for _, provided := range []string{`"source_message_id":999`, `"source_message_id":0`, `"source_content_hash":"forged"`} {
			request(t, "PUT", path, `{"expected_version":2,"subject":"telegram-source","content":"forged","valid_scope":"today",`+provided+`}`, 400)
		}
		for _, mode := range []string{"changed", "inactive", "expired"} {
			t.Run(mode, func(t *testing.T) {
				sourceText := text
				sourceHash := hash
				approved := true
				expiry := time.Now().Add(time.Hour)
				switch mode {
				case "changed":
					sourceText = "changed source"
					h := sha256.Sum256([]byte(sourceText))
					sourceHash = hex.EncodeToString(h[:])
				case "inactive":
					approved = false
				case "expired":
					expiry = time.Now().Add(-time.Second)
				}
				if _, e := db.Exec(ctx, `UPDATE group_assistant_messages SET text=$1,content_hash=$2,approved=$3,expires_at=$4 WHERE chat_id=$5 AND telegram_message_id=$6`, sourceText, sourceHash, approved, expiry, chat, msg); e != nil {
					t.Fatal(e)
				}
				request(t, "PUT", path, `{"expected_version":2,"subject":"telegram-source","content":"must not save","valid_scope":"today"}`, 409)
				after, e := q.GetGroupAssistantMemory(ctx, m.ID, chat)
				if e != nil || after.Version != got.Version || after.Content != got.Content || after.SourceContentHash != hash {
					t.Fatalf("invalid source mutated memory %+v %v", after, e)
				}
				versions, e := q.ListGroupAssistantMemoryVersions(ctx, m.ID, chat)
				if e != nil || len(versions) != 2 {
					t.Fatalf("invalid edit left partial audit %+v %v", versions, e)
				}
			})
		}
	})
	t.Run("RealJWTGroupScopeAndAuthorizedEnabled", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/admin/groups/-3002/assistant", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Fatalf("JWT scope status=%d", rec.Code)
		}
		if _, e := db.Exec(ctx, "UPDATE authorized_groups SET enabled=false WHERE chat_id=-3001"); e != nil {
			t.Fatal(e)
		}
		defer db.Exec(ctx, "UPDATE authorized_groups SET enabled=true WHERE chat_id=-3001")
		request(t, "GET", "", "", 404)
	})
	t.Run("JWTRevocationRealRedisStub", func(t *testing.T) {
		mr.Set(adminJWTRevokedKey("assistant-test-jti"), "1")
		request(t, "GET", "", "", 401)
	})
}
