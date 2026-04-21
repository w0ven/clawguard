package worker

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/store"
)

const budgetResetInterval = time.Minute

type BudgetReset struct {
	logger     *zap.Logger
	queries    *store.Queries
	botService *bot.Service
}

func NewBudgetReset(logger *zap.Logger, queries *store.Queries, botService *bot.Service) *BudgetReset {
	return &BudgetReset{
		logger:     logger,
		queries:    queries,
		botService: botService,
	}
}

func (w *BudgetReset) Run(ctx context.Context) {
	w.resetIfNeeded(ctx)

	ticker := time.NewTicker(budgetResetInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("budget reset worker stopped")
			return
		case <-ticker.C:
			w.resetIfNeeded(ctx)
		}
	}
}

func (w *BudgetReset) resetIfNeeded(ctx context.Context) {
	state, err := w.botService.GetSystemState(ctx)
	if err != nil {
		w.logger.Error("load system state for budget reset", zap.Error(err))
		return
	}
	if !state.AIBudgetLocked || state.AIBudgetLockedDate == nil {
		return
	}

	now := time.Now()
	if sameLocalDate(*state.AIBudgetLockedDate, now) {
		return
	}

	updated, err := w.botService.UpdateSystemState(ctx, store.UpdateSystemStateParams{
		AIPaused:           false,
		ActionsPaused:      state.ActionsPaused,
		Frozen:             state.Frozen,
		AIPausedReason:     "",
		AIBudgetLocked:     false,
		AIBudgetLockedDate: nil,
		UpdatedBy:          nil,
	})
	if err != nil {
		w.logger.Error("reset ai budget lock", zap.Error(err))
		return
	}

	w.botService.WriteRuntimeAudit(ctx, "global", nil, "ai_budget_unlock", state, updated)
}

func sameLocalDate(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
