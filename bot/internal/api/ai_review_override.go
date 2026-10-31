package api

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/store"
	"strings"
)

// applyAIDecisionOverride shares the original AI review transaction and score semantics.
func (s *Server) applyAIDecisionOverride(ctx context.Context, id int64, nextOverride *string) (store.AIDecision, float64, error) {
	var updated store.AIDecision
	var changed float64
	err := s.botService.Queries().Transact(ctx, func(tx *store.Queries) error {
		if e := tx.LockAIDecisionReview(ctx, id); e != nil {
			return e
		}
		current, e := tx.GetAIDecisionByID(ctx, id)
		if e != nil {
			return e
		}
		if strings.Contains(strings.ToLower(current.ActionTaken), "mute") && !stringPtrEqual(current.AdminOverride, nextOverride) {
			claim, claimErr := tx.LockAIMuteClaim(ctx, id)
			if claimErr != nil && !errors.Is(claimErr, pgx.ErrNoRows) {
				return claimErr
			}
			if claimErr == nil && (claim.State == "telegram_done" || claim.State == "dispatching" || (claim.State == "claimed" && claim.LeaseActive)) {
				return errReviewBusy
			}
		}
		updated, e = tx.UpdateAIDecisionOverride(ctx, id, nextOverride)
		if e != nil {
			return e
		}
		if nextOverride == nil || stringPtrEqual(current.AdminOverride, nextOverride) {
			return nil
		}
		var delta float64
		switch *nextOverride {
		case "false_positive":
			delta = 0.1
		case "confirm":
			delta = -0.1
		case "false_negative":
			delta = -0.2
		default:
			return nil
		}
		prior, e := tx.GetUserTrust(ctx, current.ChatID, current.UserID)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		scoreBefore := 0.5
		if e == nil {
			scoreBefore = prior.Score
		}
		trust, e := tx.AdjustUserTrustScore(ctx, store.AdjustUserTrustScoreParams{ChatID: current.ChatID, UserID: current.UserID, Delta: delta})
		if e != nil {
			return e
		}
		changed = trust.Score - scoreBefore
		return nil
	})
	return updated, changed, err
}
