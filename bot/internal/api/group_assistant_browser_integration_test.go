package api

// Test-only local browser launcher. No production auth bypass or business DTO mock.
// It is inert in ordinary go test runs and owns only its dedicated browser_it DB.
import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
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

type browserIntegrationTransport func(*http.Request) (*http.Response, error)

func (f browserIntegrationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGroupAssistantBrowserIntegrationServe(t *testing.T) {
	if os.Getenv("CG_BROWSER_IT_ENABLE") != "local-only" {
		t.Skip("dedicated local browser integration launcher disabled")
	}
	dsn := os.Getenv("CG_BROWSER_IT_DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Path != "/browser_it" || u.User == nil || u.User.Username() != "browser_it" {
		t.Fatal("refusing non-dedicated database")
	}
	nextURL, err := url.Parse(os.Getenv("CG_BROWSER_IT_NEXT_URL"))
	if err != nil || nextURL.Hostname() != "127.0.0.1" || nextURL.Scheme != "http" {
		t.Fatal("refusing non-local Next")
	}
	readyFile := os.Getenv("CG_BROWSER_IT_READY_FILE")
	if readyFile == "" {
		t.Fatal("private runtime session file required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbSQL, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer dbSQL.Close()
	if err = goose.UpContext(ctx, dbSQL, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	q := store.New(db)
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, e := db.Exec(ctx, statement, args...); e != nil {
			t.Fatal(e)
		}
	}
	fixesRound2 := os.Getenv("CG_BROWSER_IT_FIXES_ROUND2") == "1"
	fixesRound1 := os.Getenv("CG_BROWSER_IT_FIXES_ROUND1") == "1" || fixesRound2
	chats := []int64{-88001, -88002}
	if fixesRound1 {
		chats = append(chats, -88003, -88004)
	}
	if fixesRound2 {
		chats = append(chats, -88005)
	}
	for _, chat := range chats {
		exec(`INSERT INTO groups(chat_id,title,type,config) VALUES($1,$2,'supergroup','{"integration_keep":"unchanged","ai":{"message_rules":"existing moderation fixture"}}')`, chat, fmt.Sprintf("Integration PG 群 %d · 非线上", chat))
		exec(`INSERT INTO authorized_groups(chat_id) VALUES($1)`, chat)
	}
	exec(`INSERT INTO admins(telegram_id,username,role,group_scope) VALUES(901,'browser-owner','owner','[]'),(902,'browser-scoped','admin','[-88001]')`)
	if err = ai.ConfigureEncryption("browser-integration-ephemeral-only", zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	encrypted, err := ai.EncryptAPIKey("browser-fixture-not-a-real-key")
	if err != nil {
		t.Fatal(err)
	}
	var providerID int64
	if err = db.QueryRow(ctx, `INSERT INTO llm_providers(key,label,base_url,api_key_enc) VALUES('browser','Integration Provider','http://127.0.0.1:9/never-call',$1) RETURNING id`, encrypted).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO llm_models(provider_id,model_key,label,supports_tools,probe_enabled) VALUES($1,'tools','Integration Tools',true,false),($1,'tools2','备用模型二',true,false),($1,'tools3','备用模型三',true,false),($1,'text','Integration Text',false,false)`, providerID)
	// Independent FE-01/04 fixtures, opt-in and confined to this disposable DB.
	if fixesRound1 {
		exec(`UPDATE llm_models SET supports_vision=true,capability_tags=ARRAY['embedding'] WHERE model_key IN ('tools2','tools3')`)
		exec(`INSERT INTO llm_models(provider_id,model_key,label,supports_tools,enabled,probe_enabled) VALUES($1,'disabled','已停用验收模型',true,false,false)`, providerID)
		exec(`INSERT INTO group_assistant_policies(chat_id,chat_enabled,learning_enabled,chat_model_ref,learning_model_ref,system_prompt,proactive_interject_enabled,proactive_cold_topic_enabled,mimic_profile_text,tts_mode) VALUES(-88003,true,true,'browser:tools','browser:text','旧群显式Prompt保全 [ACTIVE_PERSONA]',true,false,'旧群语气保全','off')`)
		exec(`INSERT INTO group_assistant_pools(chat_id,strategy,config) VALUES(-88003,'primary-overflow','{"task_assignments":{"chat":{"primary":"legacy-main","backups":["legacy-backup"]}},"endpoints":[{"id":"legacy-main","model_ref":"browser:tools","role":"primary","priority":0,"max_concurrency":2,"timeout_ms":12000,"cooldown_duration_sec":30,"supports_tools":true},{"id":"legacy-backup","model_ref":"browser:tools2","role":"backup","priority":1,"max_concurrency":2,"timeout_ms":12000,"cooldown_duration_sec":30,"supports_tools":true}]}')`)
		exec(`INSERT INTO group_assistant_prompt_overrides(chat_id,prompt_key,content) VALUES(0,'persona','全局已编辑人格保全 [ACTIVE_PERSONA]'),(-88003,'casual','旧群日常Prompt覆盖保全')`)
	}
	if fixesRound2 {
		// Legacy explicit decision deliberately shares the exact chat endpoint IDs.
		// Separate learning parameters must survive adding backups in the browser.
		exec(`INSERT INTO group_assistant_policies(chat_id,chat_enabled,learning_enabled,chat_model_ref,learning_model_ref,system_prompt) VALUES(-88005,true,true,'browser:tools','browser:text','Round2 legacy explicit Prompt')`)
		exec(`INSERT INTO group_assistant_pools(chat_id,strategy,config) SELECT -88005,strategy,
		 jsonb_set(jsonb_set(config,'{task_assignments,decision}',config->'task_assignments'->'chat'),'{task_assignments,learning}','{"primary":"legacy-learning","backups":null,"strategy":"weighted","temperature":0.43,"max_tokens":777}')
		 || jsonb_build_object('endpoints',(config->'endpoints') || '[{"id":"legacy-learning","model_ref":"browser:text","role":"primary","priority":0,"weight":7,"max_concurrency":3,"timeout_ms":19000,"cooldown_duration_sec":41,"supports_tools":false}]'::jsonb)
		 FROM group_assistant_pools WHERE chat_id=-88003`)
	}
	providers := ai.NewProviderRegistry(zap.NewNop(), q)
	models := ai.NewModelRegistry(q)
	if err = providers.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if err = models.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	// Only Telegram's required constructor getMe response is stubbed. No polling/webhook is started.
	oldTransport := http.DefaultTransport
	var externalMu sync.Mutex
	external := []string{}
	http.DefaultTransport = browserIntegrationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "api.telegram.org" && strings.HasSuffix(r.URL.Path, "/getMe") {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":9900,"is_bot":true,"first_name":"Local integration","username":"browser_integration_bot"}}`)), Request: r}, nil
		}
		externalMu.Lock()
		external = append(external, r.Method+" "+r.URL.Hostname())
		externalMu.Unlock()
		return nil, fmt.Errorf("external request blocked by browser integration fixture")
	})
	defer func() { http.DefaultTransport = oldTransport }()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	cfg := config.Config{BotToken: "123:BROWSER_INTEGRATION_FAKE", JWTSecret: "browser-integration-ephemeral-jwt", PublicBaseURL: "http://127.0.0.1"}
	svc, err := bot.New(ctx, cfg, zap.NewNop(), q, rdb, providers, models, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()
	server := NewServer(cfg, zap.NewNop(), svc, nil)
	owner, err := q.GetAdminByTelegramID(ctx, 901)
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := q.GetAdminByTelegramID(ctx, 902)
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := server.signAdminJWT(owner)
	if err != nil {
		t.Fatal(err)
	}
	scopedToken, err := server.signAdminJWT(scoped)
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := newCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	controlKey, err := newCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	// Match repository Caddyfile: /api/* goes to actual Go NewServer; other paths go to actual Next.
	// Next itself has no API rewrite. This ingress forwards bytes, never creates assistant responses.
	proxy := httputil.NewSingleHostReverseProxy(nextURL)
	proxy.Transport = oldTransport
	ingress := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			server.echo.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/webhook/") {
			http.Error(w, "not part of local browser integration", 404)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer ingress.Close()
	// Separate loopback test preparer. It accepts only a random runtime key and finite seed/snapshot operations.
	var fixtureMu sync.Mutex
	seeded := false
	snapshot := func() ([]byte, error) {
		var raw []byte
		e := db.QueryRow(ctx, `SELECT jsonb_build_object(
 'groups',(SELECT jsonb_agg(jsonb_build_object('chat_id',chat_id,'config',config)) FROM groups),
 'global_config',(SELECT config FROM global_config WHERE id=1),
 'policies',coalesce((SELECT jsonb_agg(to_jsonb(p)) FROM group_assistant_policies p),'[]'::jsonb),
 'pools',coalesce((SELECT jsonb_agg(to_jsonb(p)) FROM group_assistant_pools p),'[]'::jsonb),
 'memories',coalesce((SELECT jsonb_agg(to_jsonb(m)) FROM group_assistant_memories m),'[]'::jsonb),
 'versions',coalesce((SELECT jsonb_agg(to_jsonb(v)) FROM group_assistant_memory_versions v),'[]'::jsonb),
 'conflicts',coalesce((SELECT jsonb_agg(to_jsonb(c)) FROM group_assistant_conflicts c),'[]'::jsonb),
 'messages',coalesce((SELECT jsonb_agg(to_jsonb(m)) FROM group_assistant_messages m),'[]'::jsonb))`).Scan(&raw)
		return raw, e
	}
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Browser-Fixture") != controlKey {
			http.Error(w, "forbidden", 403)
			return
		}
		fixtureMu.Lock()
		defer fixtureMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/snapshot" {
			raw, e := snapshot()
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			w.Write(raw)
			return
		}
		if fixesRound1 && r.Method == "POST" && (r.URL.Path == "/empty-ui-catalog" || r.URL.Path == "/disable-ui-catalog") {
			statement := `UPDATE llm_models SET enabled=false`
			if r.URL.Path == "/empty-ui-catalog" {
				statement = `DELETE FROM llm_models`
			}
			_, e := db.Exec(ctx, statement)
			if e == nil {
				e = models.Reload(ctx)
			}
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			w.Write([]byte(`{"test_catalog_updated":true}`))
			return
		}
		if r.Method == "POST" && r.URL.Path == "/seed" && !seeded {
			chat, msg := int64(-88001), int64(700)
			text := "Integration public accepted quote 50%"
			sum := sha256.Sum256([]byte(text))
			hash := hex.EncodeToString(sum[:])
			expires := time.Now().Add(7 * 24 * time.Hour)
			if _, e := q.UpsertGroupAssistantMessage(ctx, store.UpsertGroupAssistantMessageParams{ChatID: chat, ThreadID: 11, TelegramMessageID: msg, SenderID: 903, SenderName: "Integration member", Role: "user", Text: text, Approved: true, Delivered: true, ContentHash: hash, ExpiresAt: expires, SourceType: "telegram_approved_message", SourceID: "700"}); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			learned, e := q.CreateGroupAssistantMemory(ctx, store.CreateGroupAssistantMemoryParams{ChatID: chat, Subject: "Integration learned subject", Content: "Integration original learned fact", MemoryType: "learned", AuthorityLevel: "learned_fact", ValidScope: "this_week", SourceType: "telegram_approved_message", SourceMessageID: &msg, SourceChatID: &chat, SourceSnippet: text, SourceVerified: "verified", SourceContentHash: hash, ExpiresAt: expires, DedupeHash: "integration-learned"})
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			ids := []int64{}
			for _, kind := range []string{"accept", "reject"} {
				c, e := q.CreateGroupAssistantConflict(ctx, store.CreateGroupAssistantConflictParams{ChatID: chat, MemoryID: &learned.ID, Subject: "Integration " + kind + " conflict", CandidateContent: "Integration " + kind + " candidate", CandidateScope: "this_week", CandidateAuthority: "learned_fact", SourceType: "telegram_approved_message", SourceMessageID: &msg, SourceChatID: &chat, SourceSnippet: text})
				if e != nil {
					http.Error(w, e.Error(), 500)
					return
				}
				ids = append(ids, c.ID)
			}
			foreign, e := q.CreateGroupAssistantMemory(ctx, store.CreateGroupAssistantMemoryParams{ChatID: -88002, Subject: "Integration foreign subject", Content: "Integration other group only", MemoryType: "base", AuthorityLevel: "admin_base", ValidScope: "long_term", SourceType: "admin_base", SourceVerified: "verified", ExpiresAt: expires, DedupeHash: "integration-foreign"})
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			if _, e = db.Exec(ctx, `INSERT INTO group_assistant_messages(chat_id,thread_id,telegram_message_id,sender_id,role,text,content_hash,expires_at) VALUES(-88002,11,700,903,'user','Integration other group 50%','foreign',now()+interval '7 days'),(-88001,11,701,903,'user','Integration expired 50%','expired',now()-interval '1 day')`); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			seeded = true
			json.NewEncoder(w).Encode(map[string]any{"learned_id": learned.ID, "accept_id": ids[0], "reject_id": ids[1], "foreign_id": foreign.ID})
			return
		}
		http.Error(w, "unsupported fixture operation", 404)
	}))
	defer control.Close()
	ready := map[string]any{"baseURL": ingress.URL, "controlURL": control.URL, "controlKey": controlKey, "ownerToken": ownerToken, "scopedToken": scopedToken, "csrf": csrf, "ownerTelegramID": 901, "scopedTelegramID": 902}
	raw, err := json.Marshal(ready)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(readyFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("BROWSER_INTEGRATION_READY")
	// Parent owns this process, keeps stdin open, and sends newline in its finally block.
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	externalMu.Lock()
	defer externalMu.Unlock()
	if len(external) > 0 {
		t.Errorf("unexpected external calls blocked: %v", external)
	}
}
