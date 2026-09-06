package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/api"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/scheduler"
	"github.com/openclaw/clawguard/internal/store"
	"github.com/openclaw/clawguard/internal/worker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}

	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	logger = redact.ZapLogger(logger)
	defer func() {
		_ = logger.Sync()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbpool, err := pgxpool.New(ctx, cfg.DatabaseURL())
	if err != nil {
		logger.Fatal("connect postgres", zap.Error(err))
	}
	defer dbpool.Close()

	if err := dbpool.Ping(ctx); err != nil {
		logger.Fatal("ping postgres", zap.Error(err))
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr(),
		Password: cfg.RedisPassword,
		DB:       0,
	})
	defer func() {
		_ = rdb.Close()
	}()

	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Fatal("ping redis", zap.Error(err))
	}

	queries := store.New(dbpool)

	if err := ai.ConfigureEncryption(cfg.EncryptionKey, logger); err != nil {
		logger.Fatal("configure ai encryption", zap.Error(err))
	}

	providerRegistry := ai.NewProviderRegistry(logger, queries)
	modelRegistry := ai.NewModelRegistry(queries)
	if err := providerRegistry.Reload(ctx); err != nil {
		logger.Fatal("reload llm providers", zap.Error(err))
	}
	if err := modelRegistry.Reload(ctx); err != nil {
		logger.Fatal("reload llm models", zap.Error(err))
	}
	if err := ai.MigrateLegacyProviders(ctx, logger, queries, cfg); err != nil {
		logger.Fatal("migrate legacy llm providers", zap.Error(err))
	}
	if err := providerRegistry.Reload(ctx); err != nil {
		logger.Fatal("reload llm providers after migrate", zap.Error(err))
	}
	if err := modelRegistry.Reload(ctx); err != nil {
		logger.Fatal("reload llm models after migrate", zap.Error(err))
	}
	resolver := ai.NewResolver(modelRegistry).WithStats(queries)

	if err := seedAdmins(ctx, queries, cfg.SuperAdminIDs, cfg.AdminTelegramIDs); err != nil {
		logger.Fatal("seed admins", zap.Error(err))
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	botService, err := bot.New(workerCtx, cfg, logger, queries, rdb, providerRegistry, modelRegistry, resolver)
	if err != nil {
		logger.Fatal("create bot", zap.Error(err))
	}

	if err := botService.RegisterWebhook(ctx); err != nil {
		logger.Fatal("register webhook", zap.Error(err))
	}
	defer botService.Stop()

	scheduledMessages := scheduler.New(logger, queries, botService.TelegramBot(), botService.SendLimiter())
	if err := scheduledMessages.LoadActive(ctx); err != nil {
		logger.Fatal("load scheduled messages", zap.Error(err))
	}
	scheduledMessages.Start()
	defer func() {
		stopCtx := scheduledMessages.Stop()
		select {
		case <-stopCtx.Done():
		case <-time.After(5 * time.Second):
			logger.Warn("scheduled messages scheduler stop timeout")
		}
	}()

	expiryWorker := worker.NewVerificationExpiry(logger, queries, botService)
	go expiryWorker.Run(workerCtx)
	joinProtectionRecoveryWorker := worker.NewJoinProtectionRecovery(logger, botService)
	go joinProtectionRecoveryWorker.Run(workerCtx)
	healthcheckWorker := worker.NewHealthcheck(logger, queries, botService)
	go healthcheckWorker.Run(workerCtx)
	llmProberNotifier := &worker.OwnerNotifier{Bot: botService, Queries: queries, Logger: logger}
	llmPolicy := &worker.GlobalPolicyProvider{Queries: queries}
	llmProber := worker.NewLLMProber(logger, queries, providerRegistry, modelRegistry, resolver, llmPolicy, llmProberNotifier, rdb)
	go llmProber.Run(workerCtx)
	llmStats := worker.NewLLMStatsAggregator(logger, queries)
	go llmStats.Run(workerCtx)
	dailyReportWorker := worker.NewDailyReport(logger, queries, botService, cfg)
	go dailyReportWorker.Run(workerCtx)
	worker.StartRetentionCleanup(workerCtx, queries, cfg.Retention, logger)
	worker.StartProfileCheckLogsRetention(workerCtx, queries, cfg.Retention, logger)

	server := api.NewServer(cfg, logger, botService, scheduledMessages)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start()
	}()

	logger.Info(
		"clawguard started",
		zap.String("env", cfg.AppEnv),
		zap.String("http_addr", cfg.ListenAddr()),
	)

	select {
	case <-ctx.Done():
		workerCancel()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown http server", zap.Error(err))
		}
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("http server exited", zap.Error(err))
		}
	}
}

// seedAdmins ensures the bootstrap accounts exist on startup.
//
// SUPER_ADMIN_IDS seed as role "owner"; ADMIN_TELEGRAM_IDS seed as role "admin".
//
// Backward compatibility: older releases seeded ADMIN_TELEGRAM_IDS as "owner".
// Seeding is therefore create-only for the role. An admin row that already
// exists keeps its stored role, so an existing owner is never demoted here and
// role changes made in the web console are never reverted on restart. Only
// SUPER_ADMIN_IDS may promote an existing non-owner row to "owner", so a
// locked-out operator can always recover owner access via the environment.
func seedAdmins(ctx context.Context, queries *store.Queries, superAdminIDs, adminIDs []int64) error {
	seeded := make(map[int64]bool, len(superAdminIDs)+len(adminIDs))

	for _, id := range superAdminIDs {
		if id == 0 || seeded[id] {
			continue
		}
		seeded[id] = true
		if err := seedAdmin(ctx, queries, id, "owner"); err != nil {
			return err
		}
	}

	for _, id := range adminIDs {
		if id == 0 || seeded[id] {
			continue
		}
		seeded[id] = true
		if err := seedAdmin(ctx, queries, id, "admin"); err != nil {
			return err
		}
	}

	return nil
}

func seedAdmin(ctx context.Context, queries *store.Queries, id int64, role string) error {
	existing, err := queries.GetAdminByTelegramID(ctx, id)
	switch {
	case err == nil:
		// Only promote to owner; never demote or overwrite a console-managed role.
		if role != "owner" || existing.Role == "owner" {
			return nil
		}
		_, err := queries.UpdateAdminRole(ctx, store.UpdateAdminRoleParams{
			ID:   existing.ID,
			Role: "owner",
		})
		return err
	case errors.Is(err, pgx.ErrNoRows):
		_, err := queries.UpsertAdmin(ctx, store.UpsertAdminParams{
			TelegramID: id,
			Role:       role,
			GroupScope: []byte("[]"),
		})
		return err
	default:
		return err
	}
}
