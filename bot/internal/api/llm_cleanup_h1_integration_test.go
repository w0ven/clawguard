//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/openclaw/clawguard/internal/store"
)

var h1MigrateMu sync.Mutex

const h1SeedConfig = `{
  "verify": {"enabled": true},
  "ai": {
    "message_rules": "keep message",
    "bio_rules": "keep bio",
    "custom_rules": "",
    "primary_model_ref": "newapi:glm-5",
    "fallback_model_refs": ["newapi:minimax-m2.5"],
    "adkiller": {"enabled": false, "timeout_ms": 1500},
    "auto_degrade": false,
    "probe_enabled": false,
    "probe_interval_seconds": 120
  }
}`

type h1Fixture struct {
	pool    *pgxpool.Pool
	queries *store.Queries
	admin   store.Admin
	target  llmModelReferenceTarget
}

func TestLegacyGetThenUpsertLosesConcurrentAdminSectionWrite(t *testing.T) {
	fx := openH1Fixture(t)
	seed := seedH1GlobalConfig(t, fx.queries)

	readDone := make(chan struct{})
	adminDone := make(chan struct{})
	var cleanupErr, adminErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		cleanupErr = legacyCleanupGetThenUpsert(context.Background(), fx.queries, fx.admin, fx.target, func() {
			close(readDone)
			<-adminDone
		})
	}()
	go func() {
		defer wg.Done()
		<-readDone
		_, adminErr = putGlobalConfigSection(context.Background(), fx.queries, promptSectionRequest("admin concurrent prompt"))
		close(adminDone)
	}()
	wg.Wait()
	if cleanupErr != nil {
		t.Fatalf("legacy cleanup: %v", cleanupErr)
	}
	if adminErr != nil {
		t.Fatalf("admin section put: %v", adminErr)
	}

	got := mustGetGlobalConfig(t, fx.queries)
	ai := mustAI(t, got.Config)
	if got.Version != seed.Version+2 {
		t.Fatalf("version = %d, want %d (admin write + stale upsert)", got.Version, seed.Version+2)
	}
	if messageRules(ai) == "admin concurrent prompt" {
		t.Fatalf("legacy Get+Upsert unexpectedly kept the admin prompt: %s", got.Config)
	}
	if messageRules(ai) != "keep message" {
		t.Fatalf("message_rules = %q, want stale snapshot %q", messageRules(ai), "keep message")
	}
	if _, ok := ai["primary_model_ref"]; ok {
		t.Fatalf("legacy cleanup did not drop primary_model_ref: %s", got.Config)
	}
}

func TestCleanupDeletedLLMModelReferencesKeepsConcurrentAdminSectionWrite(t *testing.T) {
	t.Run("admin then cleanup", func(t *testing.T) {
		fx := openH1Fixture(t)
		seed := seedH1GlobalConfig(t, fx.queries)
		if _, err := putGlobalConfigSection(context.Background(), fx.queries, promptSectionRequest("admin concurrent prompt")); err != nil {
			t.Fatalf("admin section put: %v", err)
		}
		if err := runCleanupInDeleteTx(context.Background(), fx.queries, fx.admin, fx.target); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		assertCleanupKeptAdminPrompt(t, fx.queries, seed.Version+2)
	})

	t.Run("cleanup then admin", func(t *testing.T) {
		fx := openH1Fixture(t)
		seed := seedH1GlobalConfig(t, fx.queries)
		if err := runCleanupInDeleteTx(context.Background(), fx.queries, fx.admin, fx.target); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		if _, err := putGlobalConfigSection(context.Background(), fx.queries, promptSectionRequest("admin concurrent prompt")); err != nil {
			t.Fatalf("admin section put: %v", err)
		}
		assertCleanupKeptAdminPrompt(t, fx.queries, seed.Version+2)
	})

	t.Run("for-update serializes overlapping writers", func(t *testing.T) {
		fx := openH1Fixture(t)
		seed := seedH1GlobalConfig(t, fx.queries)

		held := make(chan struct{})
		release := make(chan struct{})
		holdDone := make(chan struct{})
		holdGlobalConfigRow(t, fx.pool, held, release, holdDone)
		<-held

		var cleanupErr, adminErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			cleanupErr = runCleanupInDeleteTx(context.Background(), fx.queries, fx.admin, fx.target)
		}()
		go func() {
			defer wg.Done()
			_, adminErr = putGlobalConfigSection(context.Background(), fx.queries, promptSectionRequest("admin concurrent prompt"))
		}()
		time.Sleep(150 * time.Millisecond)
		close(release)
		wg.Wait()
		<-holdDone
		if cleanupErr != nil {
			t.Fatalf("cleanup: %v", cleanupErr)
		}
		if adminErr != nil {
			t.Fatalf("admin section put: %v", adminErr)
		}
		assertCleanupKeptAdminPrompt(t, fx.queries, seed.Version+2)
	})
}

func TestConcurrentPromptAndLLMSectionPutsKeepBothFields(t *testing.T) {
	fx := openH1Fixture(t)
	seed := seedH1GlobalConfig(t, fx.queries)

	held := make(chan struct{})
	release := make(chan struct{})
	holdDone := make(chan struct{})
	holdGlobalConfigRow(t, fx.pool, held, release, holdDone)
	<-held

	var promptErr, llmErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, promptErr = putGlobalConfigSection(context.Background(), fx.queries, promptSectionRequest("new message"))
	}()
	go func() {
		defer wg.Done()
		_, llmErr = putGlobalConfigSection(context.Background(), fx.queries, llmSectionRequest(true, true, 300))
	}()
	time.Sleep(150 * time.Millisecond)
	close(release)
	wg.Wait()
	<-holdDone
	if promptErr != nil {
		t.Fatalf("prompt section: %v", promptErr)
	}
	if llmErr != nil {
		t.Fatalf("llm section: %v", llmErr)
	}

	got := mustGetGlobalConfig(t, fx.queries)
	ai := mustAI(t, got.Config)
	if got.Version != seed.Version+2 {
		t.Fatalf("version = %d, want %d", got.Version, seed.Version+2)
	}
	if messageRules(ai) != "new message" {
		t.Fatalf("message_rules = %q, want %q", messageRules(ai), "new message")
	}
	if bioRules(ai) != "keep bio" {
		t.Fatalf("bio_rules = %q, want keep bio", bioRules(ai))
	}
	if !boolField(ai, "auto_degrade") || !boolField(ai, "probe_enabled") {
		t.Fatalf("llm probe fields missing: %s", got.Config)
	}
	if intField(ai, "probe_interval_seconds") != 300 {
		t.Fatalf("probe_interval_seconds = %v, want 300", ai["probe_interval_seconds"])
	}
	if ai["primary_model_ref"] != "newapi:glm-5" {
		t.Fatalf("primary_model_ref overwritten: %s", got.Config)
	}
}

func TestStaleDocumentVersionConflictDoesNotWrite(t *testing.T) {
	fx := openH1Fixture(t)
	seed := seedH1GlobalConfig(t, fx.queries)
	stale := seed.Version - 1
	if stale < 0 {
		stale = 0
	}

	var conflict *globalConfigVersionConflictError
	err := fx.queries.Transact(context.Background(), func(tx *store.Queries) error {
		current, err := tx.GetGlobalConfigForUpdate(context.Background())
		if err != nil {
			return err
		}
		next, err := applyGlobalConfigPut(current, globalConfigPutRequest{
			Section: globalConfigSectionDocument,
			Version: &stale,
			Config:  []byte(`{"verify":{"enabled":false},"ai":{"message_rules":"stale overwrite"}}`),
		})
		if err != nil {
			return err
		}
		_, err = tx.UpdateGlobalConfig(context.Background(), store.UpdateGlobalConfigParams{Config: next})
		return err
	})
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want version conflict", err)
	}

	got := mustGetGlobalConfig(t, fx.queries)
	if got.Version != seed.Version {
		t.Fatalf("version mutated on 409 path: got %d want %d", got.Version, seed.Version)
	}
	ai := mustAI(t, got.Config)
	if messageRules(ai) != "keep message" {
		t.Fatalf("stale document wrote config: %s", got.Config)
	}

	currentVersion := seed.Version
	updated, err := putGlobalConfigDocument(context.Background(), fx.queries, currentVersion, []byte(`{
		"verify": {"enabled": true},
		"ai": {
			"message_rules": "doc write",
			"bio_rules": "keep bio",
			"primary_model_ref": "newapi:glm-5"
		}
	}`))
	if err != nil {
		t.Fatalf("matching document write: %v", err)
	}
	if updated.Version != seed.Version+1 {
		t.Fatalf("matching document version = %d, want %d", updated.Version, seed.Version+1)
	}
	if messageRules(mustAI(t, updated.Config)) != "doc write" {
		t.Fatalf("matching document config = %s", updated.Config)
	}
}

func openH1Fixture(t *testing.T) *h1Fixture {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}

	ctx := context.Background()
	bootstrap, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}
	schema := fmt.Sprintf("h1_%d", time.Now().UnixNano())
	if _, err := bootstrap.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = bootstrap.Close(ctx)
		t.Fatalf("create schema %s: %v", schema, err)
	}
	if err := bootstrap.Close(ctx); err != nil {
		t.Fatalf("close bootstrap: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, databaseURL)
		if err != nil {
			t.Logf("cleanup connect: %v", err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
	})

	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse pool config: %v", err)
	}
	poolCfg.ConnConfig.RuntimeParams["search_path"] = schema
	poolCfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	migrateH1Schema(t, databaseURL, schema)

	return &h1Fixture{
		pool:    pool,
		queries: store.New(pool),
		admin:   store.Admin{Role: "owner"},
		target:  newLLMModelReferenceTarget("newapi", "glm-5"),
	}
}

func migrateH1Schema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	h1MigrateMu.Lock()
	defer h1MigrateMu.Unlock()

	sqlDB, err := sql.Open("pgx", withSearchPath(databaseURL, schema))
	if err != nil {
		t.Fatalf("open goose db: %v", err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	if err := goose.UpContext(context.Background(), sqlDB, h1MigrationsDir(t)); err != nil {
		t.Fatalf("goose up: %v", err)
	}
}

func h1MigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", "migrations"))
	if err != nil {
		t.Fatalf("migrations dir: %v", err)
	}
	return dir
}

func withSearchPath(databaseURL, schema string) string {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return databaseURL
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func seedH1GlobalConfig(t *testing.T, queries *store.Queries) store.GlobalConfig {
	t.Helper()
	updated, err := queries.UpdateGlobalConfig(context.Background(), store.UpdateGlobalConfigParams{
		Config: []byte(h1SeedConfig),
	})
	if err != nil {
		t.Fatalf("seed global config: %v", err)
	}
	return updated
}

func mustGetGlobalConfig(t *testing.T, queries *store.Queries) store.GlobalConfig {
	t.Helper()
	got, err := queries.GetGlobalConfig(context.Background())
	if err != nil {
		t.Fatalf("GetGlobalConfig: %v", err)
	}
	return got
}

func runCleanupInDeleteTx(ctx context.Context, queries *store.Queries, admin store.Admin, target llmModelReferenceTarget) error {
	return queries.Transact(ctx, func(tx *store.Queries) error {
		_, err := cleanupDeletedLLMModelReferences(ctx, tx, admin, target)
		return err
	})
}

// legacyCleanupGetThenUpsert reconstructs the pre-fix cleanup (unlocked Get + Upsert)
// with actual store methods so the lost-write race can be reproduced at a determined
// interleaving. afterUnlockedRead runs after GetGlobalConfig and before Upsert.
func legacyCleanupGetThenUpsert(ctx context.Context, queries *store.Queries, admin store.Admin, target llmModelReferenceTarget, afterUnlockedRead func()) error {
	return queries.Transact(ctx, func(tx *store.Queries) error {
		globalConfig, err := tx.GetGlobalConfig(ctx)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("load global config: %w", err)
		}
		cleaned, _, changed, err := cleanLLMModelReferencesFromConfig(globalConfig.Config, target)
		if err != nil {
			return fmt.Errorf("clean global config: %w", err)
		}
		if afterUnlockedRead != nil {
			afterUnlockedRead()
		}
		if changed {
			if _, err := tx.UpsertGlobalConfig(ctx, store.UpsertGlobalConfigParams{Config: cleaned}); err != nil {
				return fmt.Errorf("upsert global config: %w", err)
			}
			if err := writeAuditWithQueries(ctx, tx, admin, "global", nil, "delete_llm_model_cleanup_refs", globalConfig.Config, cleaned); err != nil {
				return err
			}
		}
		return nil
	})
}

func putGlobalConfigSection(ctx context.Context, queries *store.Queries, req globalConfigPutRequest) (store.GlobalConfig, error) {
	var updated store.GlobalConfig
	err := queries.Transact(ctx, func(tx *store.Queries) error {
		current, err := tx.GetGlobalConfigForUpdate(ctx)
		if err != nil {
			return err
		}
		next, err := applyGlobalConfigPut(current, req)
		if err != nil {
			return err
		}
		if err := validateGlobalPolicyUpdate(ctx, tx, next); err != nil {
			return globalConfigValidationError{err: err}
		}
		updated, err = tx.UpdateGlobalConfig(ctx, store.UpdateGlobalConfigParams{Config: next})
		return err
	})
	return updated, err
}

func putGlobalConfigDocument(ctx context.Context, queries *store.Queries, version int64, config []byte) (store.GlobalConfig, error) {
	return putGlobalConfigSection(ctx, queries, globalConfigPutRequest{
		Section: globalConfigSectionDocument,
		Version: &version,
		Config:  config,
	})
}

func promptSectionRequest(message string) globalConfigPutRequest {
	payload, _ := json.Marshal(map[string]any{
		"ai": map[string]any{"message_rules": message},
	})
	return globalConfigPutRequest{Section: globalConfigSectionPrompt, Config: payload}
}

func llmSectionRequest(autoDegrade, probeEnabled bool, interval int) globalConfigPutRequest {
	payload, _ := json.Marshal(map[string]any{
		"ai": map[string]any{
			"auto_degrade":           autoDegrade,
			"probe_enabled":          probeEnabled,
			"probe_interval_seconds": interval,
		},
	})
	return globalConfigPutRequest{Section: globalConfigSectionLLM, Config: payload}
}

func holdGlobalConfigRow(t *testing.T, pool *pgxpool.Pool, held chan struct{}, release <-chan struct{}, done chan struct{}) {
	t.Helper()
	go func() {
		defer close(done)
		signalHeld := sync.OnceFunc(func() { close(held) })
		defer signalHeld()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Errorf("hold acquire: %v", err)
			return
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Errorf("hold begin: %v", err)
			return
		}
		defer tx.Rollback(ctx)
		if _, err := store.New(tx).GetGlobalConfigForUpdate(ctx); err != nil {
			t.Errorf("hold FOR UPDATE: %v", err)
			return
		}
		signalHeld()
		select {
		case <-release:
		case <-ctx.Done():
			t.Errorf("hold wait: %v", ctx.Err())
		}
	}()
}

func assertCleanupKeptAdminPrompt(t *testing.T, queries *store.Queries, wantVersion int64) {
	t.Helper()
	got := mustGetGlobalConfig(t, queries)
	ai := mustAI(t, got.Config)
	if got.Version != wantVersion {
		t.Fatalf("version = %d, want %d", got.Version, wantVersion)
	}
	if messageRules(ai) != "admin concurrent prompt" {
		t.Fatalf("message_rules = %q, want admin concurrent prompt; config=%s", messageRules(ai), got.Config)
	}
	if bioRules(ai) != "keep bio" {
		t.Fatalf("bio_rules = %q, want keep bio", bioRules(ai))
	}
	if _, ok := ai["primary_model_ref"]; ok {
		t.Fatalf("primary_model_ref still present: %s", got.Config)
	}
	refs, _ := ai["fallback_model_refs"].([]any)
	if len(refs) != 1 || refs[0] != "newapi:minimax-m2.5" {
		t.Fatalf("fallback_model_refs = %#v", ai["fallback_model_refs"])
	}
}

func mustAI(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	ai, ok := doc["ai"].(map[string]any)
	if !ok {
		t.Fatalf("missing ai object: %s", raw)
	}
	return ai
}

func messageRules(ai map[string]any) string {
	got, _ := ai["message_rules"].(string)
	return got
}

func bioRules(ai map[string]any) string {
	got, _ := ai["bio_rules"].(string)
	return got
}

func boolField(ai map[string]any, key string) bool {
	got, _ := ai[key].(bool)
	return got
}

func intField(ai map[string]any, key string) int {
	switch got := ai[key].(type) {
	case float64:
		return int(got)
	case int:
		return got
	case int64:
		return int(got)
	case json.Number:
		n, _ := got.Int64()
		return int(n)
	default:
		return 0
	}
}
