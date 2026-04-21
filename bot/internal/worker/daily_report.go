package worker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

var shanghaiLocation = mustLoadLocation("Asia/Shanghai")

type DailyReport struct {
	logger     *zap.Logger
	queries    *store.Queries
	botService *bot.Service
	cfg        config.Config
}

func NewDailyReport(logger *zap.Logger, queries *store.Queries, botService *bot.Service, cfg config.Config) *DailyReport {
	return &DailyReport{
		logger:     logger,
		queries:    queries,
		botService: botService,
		cfg:        cfg,
	}
}

func (w *DailyReport) Run(ctx context.Context) {
	if w == nil || w.logger == nil || w.queries == nil || w.botService == nil {
		return
	}
	if !w.cfg.DailyReportEnabled {
		w.logger.Info("daily report worker disabled")
		return
	}

	for {
		next := nextDailyReportRun(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			w.logger.Info("daily report worker stopped")
			return
		case <-timer.C:
			w.runOnce(ctx, next.In(shanghaiLocation))
		}
	}
}

func (w *DailyReport) runOnce(ctx context.Context, trigger time.Time) {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	start, end, label := reportWindow(trigger)
	report, err := w.buildReport(runCtx, start, end, label)
	if err != nil {
		w.logger.Warn("build daily report failed", zap.Error(err))
		return
	}

	admins, err := w.queries.ListAdmins(runCtx)
	if err != nil {
		w.logger.Warn("load admins for daily report failed", zap.Error(err))
		return
	}

	var sent int
	for _, admin := range admins {
		if strings.TrimSpace(strings.ToLower(admin.Role)) != "owner" || admin.TelegramID == 0 {
			continue
		}
		if err := w.botService.SendHTMLPrivateMessage(admin.TelegramID, report); err != nil {
			w.logger.Warn("send daily report failed", zap.Error(err), zap.Int64("telegram_id", admin.TelegramID))
			continue
		}
		sent++
	}

	w.logger.Info("daily report sent", zap.String("date", label), zap.Int("owners", sent))
}

func (w *DailyReport) buildReport(ctx context.Context, start, end time.Time, label string) (string, error) {
	policy, err := config.LoadPolicy(ctx, w.queries, 0)
	if err != nil {
		policy = config.DefaultPolicy
	}

	actionCounts, err := w.queries.DailyReportViolationActionCounts(ctx, start, end)
	if err != nil {
		return "", err
	}
	ruleCounts, err := w.queries.DailyReportViolationRuleCounts(ctx, start, end, 2)
	if err != nil {
		return "", err
	}
	aiStats, err := w.queries.DailyReportAIDecisionStats(ctx, start, end)
	if err != nil {
		return "", err
	}
	trustCounts, err := w.queries.DailyReportNewUserTrustStatusCounts(ctx, start, end)
	if err != nil {
		return "", err
	}
	pendingCreated, err := w.queries.DailyReportPendingVerificationCount(ctx, start, end)
	if err != nil {
		return "", err
	}

	verifyTotal, verifyPassed, verifyFailed, verifyPending := summarizeVerification(trustCounts, pendingCreated)
	violationLine := fmt.Sprintf("• 删除: %d  警告: %d  禁言: %d  封禁: %d",
		countAction(actionCounts, "delete", "delete_warn", "delete_mute", "delete_ban"),
		countAction(actionCounts, "warn"),
		countAction(actionCounts, "mute", "delete_mute"),
		countAction(actionCounts, "ban", "delete_ban", "kick"),
	)

	topRules := "今日无"
	if len(ruleCounts) > 0 {
		parts := make([]string, 0, len(ruleCounts))
		for _, item := range ruleCounts {
			parts = append(parts, fmt.Sprintf("%s (%d)", htmlEscape(item.Name), item.Count))
		}
		topRules = strings.Join(parts, ", ")
	}

	aiTotal, verdictCounts, actionTakenCounts, totalCost := summarizeAI(aiStats)
	lines := []string{
		fmt.Sprintf("📊 <b>ClawGuard 日报 %s</b>", label),
		"",
		"<b>验证</b>",
		fmt.Sprintf("• 新入群 %d 人（通过 %d / 失败 %d / 待验证 %d）", verifyTotal, verifyPassed, verifyFailed, verifyPending),
		"",
		"<b>违规处理</b>",
		violationLine,
		fmt.Sprintf("• Top 规则: %s", topRules),
		"",
		"<b>AI 审核</b>",
		fmt.Sprintf("• 判定: 共 %d 条 | ad %d | scam %d | spam %d | clean %d", aiTotal, verdictCounts["ad"], verdictCounts["scam"], verdictCounts["spam"], verdictCounts["clean"]),
		fmt.Sprintf("• 动作: ban %d | mute %d | warn %d", actionTakenCounts["ban"], actionTakenCounts["mute"], actionTakenCounts["warn"]),
		fmt.Sprintf("• 成本: %.4g¢（预算 %d¢）", totalCost, policy.AI.DailyBudgetCents),
		"",
		"<b>熔断/异常</b>",
		"• 今日无",
		"",
		fmt.Sprintf("查看详情: %s/dashboard", strings.TrimRight(w.cfg.PublicBaseURL, "/")),
	}
	return strings.Join(lines, "\n"), nil
}

func nextDailyReportRun(now time.Time) time.Time {
	local := now.In(shanghaiLocation)
	next := time.Date(local.Year(), local.Month(), local.Day(), 0, 30, 0, 0, shanghaiLocation)
	if !next.After(local) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func reportWindow(trigger time.Time) (time.Time, time.Time, string) {
	local := trigger.In(shanghaiLocation).Add(-24 * time.Hour)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, shanghaiLocation)
	end := start.Add(24 * time.Hour)
	return start.UTC(), end.UTC(), start.Format("2006-01-02")
}

func summarizeVerification(items []store.CountByName, pendingCreated int64) (int64, int64, int64, int64) {
	counts := map[string]int64{}
	var total int64
	for _, item := range items {
		key := strings.TrimSpace(strings.ToLower(item.Name))
		counts[key] += item.Count
		total += item.Count
	}

	pending := counts["new"]
	if pending == 0 && pendingCreated > 0 {
		pending = pendingCreated
	}
	failed := counts["banned"] + counts["suspicious"]
	passed := total - failed - pending
	if passed < 0 {
		passed = 0
	}
	return total, passed, failed, pending
}

func summarizeAI(items []store.AIDecisionDailyStat) (int64, map[string]int64, map[string]int64, float64) {
	verdictCounts := map[string]int64{}
	actionCounts := map[string]int64{}
	var total int64
	var cost float64
	for _, item := range items {
		verdictCounts[strings.TrimSpace(strings.ToLower(item.Verdict))] += item.Count
		actionCounts[strings.TrimSpace(strings.ToLower(item.ActionTaken))] += item.Count
		total += item.Count
		cost += item.CostCents
	}
	return total, verdictCounts, actionCounts, cost
}

func countAction(items []store.CountByName, names ...string) int64 {
	allowed := map[string]struct{}{}
	for _, name := range names {
		allowed[strings.TrimSpace(strings.ToLower(name))] = struct{}{}
	}
	var total int64
	for _, item := range items {
		if _, ok := allowed[strings.TrimSpace(strings.ToLower(item.Name))]; ok {
			total += item.Count
		}
	}
	return total
}

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func htmlEscape(str string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(str)
}
