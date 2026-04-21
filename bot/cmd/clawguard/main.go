package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/api"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
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

	if err := seedAdmins(ctx, queries, cfg.AdminTelegramIDs); err != nil {
		logger.Fatal("seed admins", zap.Error(err))
	}

	botService, err := bot.New(cfg, logger, queries, rdb, providerRegistry, modelRegistry, resolver)
	if err != nil {
		logger.Fatal("create bot", zap.Error(err))
	}

	if err := botService.RegisterWebhook(ctx); err != nil {
		logger.Fatal("register webhook", zap.Error(err))
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	expiryWorker := worker.NewVerificationExpiry(logger, queries, botService)
	go expiryWorker.Run(workerCtx)
	budgetWorker := worker.NewBudgetReset(logger, queries, botService)
	go budgetWorker.Run(workerCtx)
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

	server := api.NewServer(cfg, logger, botService)

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

func seedAdmins(ctx context.Context, queries *store.Queries, ids []int64) error {
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, err := queries.UpsertAdmin(ctx, store.UpsertAdminParams{
			TelegramID: id,
			Role:       "owner",
			GroupScope: []byte("[]"),
		}); err != nil {
			return err
		}
	}
	return nil
}
