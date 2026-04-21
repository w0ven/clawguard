package store

import (
	"context"
	"time"
)

const dailyReportViolationActionCounts = `
SELECT action, COUNT(*)::BIGINT AS count
FROM violations
WHERE created_at >= $1
  AND created_at < $2
GROUP BY action
`

const dailyReportViolationRuleCounts = `
SELECT rule, COUNT(*)::BIGINT AS count
FROM violations
WHERE created_at >= $1
  AND created_at < $2
GROUP BY rule
ORDER BY count DESC, rule ASC
LIMIT $3
`

const dailyReportAIDecisionStats = `
SELECT verdict, action_taken, COUNT(*)::BIGINT AS count, COALESCE(SUM(cost_cents), 0)::DOUBLE PRECISION AS cost_cents
FROM ai_decisions
WHERE created_at >= $1
  AND created_at < $2
GROUP BY verdict, action_taken
`

const dailyReportNewUserTrustStatusCounts = `
SELECT status, COUNT(*)::BIGINT AS count
FROM user_trust
WHERE joined_at >= $1
  AND joined_at < $2
GROUP BY status
`

const dailyReportPendingVerificationCounts = `
SELECT COUNT(*)::BIGINT AS count
FROM pending_verifications
WHERE created_at >= $1
  AND created_at < $2
`

type CountByName struct {
	Name  string
	Count int64
}

type AIDecisionDailyStat struct {
	Verdict     string
	ActionTaken string
	Count       int64
	CostCents   float64
}

func (q *Queries) DailyReportViolationActionCounts(ctx context.Context, start, end time.Time) ([]CountByName, error) {
	rows, err := q.db.Query(ctx, dailyReportViolationActionCounts, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []CountByName
	for rows.Next() {
		var item CountByName
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (q *Queries) DailyReportViolationRuleCounts(ctx context.Context, start, end time.Time, limit int32) ([]CountByName, error) {
	rows, err := q.db.Query(ctx, dailyReportViolationRuleCounts, start, end, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []CountByName
	for rows.Next() {
		var item CountByName
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (q *Queries) DailyReportAIDecisionStats(ctx context.Context, start, end time.Time) ([]AIDecisionDailyStat, error) {
	rows, err := q.db.Query(ctx, dailyReportAIDecisionStats, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AIDecisionDailyStat
	for rows.Next() {
		var item AIDecisionDailyStat
		if err := rows.Scan(&item.Verdict, &item.ActionTaken, &item.Count, &item.CostCents); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (q *Queries) DailyReportNewUserTrustStatusCounts(ctx context.Context, start, end time.Time) ([]CountByName, error) {
	rows, err := q.db.Query(ctx, dailyReportNewUserTrustStatusCounts, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []CountByName
	for rows.Next() {
		var item CountByName
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (q *Queries) DailyReportPendingVerificationCount(ctx context.Context, start, end time.Time) (int64, error) {
	row := q.db.QueryRow(ctx, dailyReportPendingVerificationCounts, start, end)
	var count int64
	err := row.Scan(&count)
	return count, err
}
