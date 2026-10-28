package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

var errTrustSyncConflict = errors.New("trust sync conflict")
var errTrustSyncBusy = errors.New("trust sync in progress")

type trustSyncPayload struct {
	Status string  `json:"status"`
	Score  float64 `json:"score"`
	Notes  *string `json:"notes"`
}

func (s *Server) syncTrustStatus(c echo.Context, chatID, userID int64, p trustSyncPayload) error {
	ctx := c.Request().Context()
	q := s.botService.Queries()
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return c.JSON(500, map[string]string{"error": "无法创建同步操作"})
	}
	token := hex.EncodeToString(random)
	var trust store.UserTrust
	var op store.TrustSync
	var sameCompleted bool
	var conflicted bool
	err := q.Transact(ctx, func(tx *store.Queries) error {
		var e error
		trust, e = tx.LockTrustForSync(ctx, chatID, userID)
		if e != nil {
			return e
		}
		if trust.Status == "archived" {
			return pgx.ErrNoRows
		}
		op, e = tx.GetTrustSync(ctx, chatID, userID)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		exists := e == nil
		if exists && (op.State == "claimed" || op.State == "retry") && (trust.Status != op.Base || !trust.StatusChangedAt.Equal(op.BaseChanged)) {
			if e = tx.ConflictTrustSync(ctx, chatID, userID, op.Token); e != nil {
				return e
			}
			conflicted = true
			return nil
		}
		if exists && op.State == "claimed" && op.LeaseActive {
			return errTrustSyncBusy
		}
		if exists && (op.State == "claimed" || op.State == "retry") && op.Target != p.Status {
			return errTrustSyncBusy
		}
		if exists && (op.State == "claimed" || op.State == "retry") && op.Target == p.Status {
			if trust.Status != op.Base || !trust.StatusChangedAt.Equal(op.BaseChanged) {
				return errTrustSyncConflict
			}
		} else {
			sameCompleted = exists && op.State == "completed" && op.Target == p.Status && trust.Status == p.Status && trust.StatusChangedAt.Equal(op.BaseChanged)
			op = store.TrustSync{Target: p.Status, Base: trust.Status, BaseChanged: trust.StatusChangedAt, ActionDone: true, PermissionDone: true}
			if !sameCompleted && p.Status == "banned" {
				op.Action = "ban"
				op.ActionDone = false
			}
			if !sameCompleted && p.Status != "banned" && trust.Status == "banned" {
				op.Action = "unban"
				op.ActionDone = false
			}
			if !sameCompleted && p.Status == "trusted" && trust.Status != "trusted" {
				op.PermissionDone = false
			}
		}
		op.Token = token
		return tx.ClaimTrustSync(ctx, chatID, userID, op)
	})
	if err == nil && conflicted {
		return c.JSON(http.StatusConflict, map[string]string{"error": "用户状态已变化，旧同步已取消，请核查后重试"})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(404, map[string]string{"error": "user trust not found"})
	}
	if errors.Is(err, errTrustSyncBusy) || errors.Is(err, errTrustSyncConflict) {
		return c.JSON(409, map[string]string{"error": "用户状态已变化或同步正在进行，请重新检查后重试"})
	}
	if err != nil {
		return c.JSON(500, map[string]string{"error": "创建同步失败，请重试"})
	}
	// Recheck durable claim and current state before each external action.
	check := func() error {
		changed := false
		e := q.Transact(ctx, func(tx *store.Queries) error {
			u, e := tx.LockTrustForSync(ctx, chatID, userID)
			if e != nil {
				return e
			}
			v, e := tx.GetTrustSync(ctx, chatID, userID)
			if e != nil {
				return e
			}
			if v.Token != token || v.State != "claimed" {
				changed = true
				return nil
			}
			if u.Status != op.Base || !u.StatusChangedAt.Equal(op.BaseChanged) {
				if e = tx.ConflictTrustSync(ctx, chatID, userID, token); e != nil {
					return e
				}
				changed = true
			}
			return nil
		})
		if e != nil {
			return e
		}
		if changed {
			return errTrustSyncConflict
		}
		return nil
	}
	checkFailure := func(e error) error {
		if errors.Is(e, errTrustSyncConflict) {
			return c.JSON(409, map[string]string{"error": "用户状态已变化，请核查后重试"})
		}
		return c.JSON(500, map[string]string{"error": "无法检查用户状态，请重试"})
	}
	advance := func(stage string) error {
		ok, e := q.AdvanceTrustSync(ctx, chatID, userID, token, stage)
		if e != nil {
			return e
		}
		if !ok {
			return errTrustSyncConflict
		}
		return nil
	}
	fail := func(field string, e error) error {
		if x := advance("retry"); x != nil {
			return c.JSON(500, map[string]string{"error": "同步结果无法保存，请重新核查后重试"})
		}
		return c.JSON(http.StatusOK, map[string]any{"trust": serializeUserTrust(trust), field: redact.ErrorString(e), "sync_pending": true})
	}
	if !op.ActionDone {
		if e := check(); e != nil {
			return checkFailure(e)
		}
		var e error
		if op.Action == "ban" {
			e = s.botService.BanChatUser(ctx, chatID, userID)
		} else {
			e = s.botService.UnbanChatUser(ctx, chatID, userID)
		}
		if e != nil {
			return fail("telegram_action_error", e)
		}
		if e = advance("action"); e != nil {
			return c.JSON(500, map[string]string{"error": "Telegram 已执行，但同步状态保存失败，请核查后重试"})
		}
	}
	if !op.PermissionDone {
		if e := check(); e != nil {
			return checkFailure(e)
		}
		target := trust
		target.Status = "trusted"
		if e := s.botService.RestoreTrustedChatPermissions(ctx, target); e != nil {
			return fail("telegram_permission_error", e)
		}
		if e := advance("permission"); e != nil {
			return c.JSON(500, map[string]string{"error": "Telegram 已执行，但同步状态保存失败，请核查后重试"})
		}
	}
	err = q.Transact(ctx, func(tx *store.Queries) error {
		current, e := tx.LockTrustForSync(ctx, chatID, userID)
		if e != nil {
			return e
		}
		v, e := tx.GetTrustSync(ctx, chatID, userID)
		if e != nil {
			return e
		}
		if v.Token != token || v.State != "claimed" {
			conflicted = true
			return nil
		}
		if current.Status != op.Base || !current.StatusChangedAt.Equal(op.BaseChanged) {
			if e = tx.ConflictTrustSync(ctx, chatID, userID, token); e != nil {
				return e
			}
			conflicted = true
			return nil
		}
		if op.Action == "ban" {
			admin, _ := currentAdmin(c)
			if e = tx.UpsertGroupBan(ctx, chatID, userID, stringPtr("manual trust ban"), "manual", &admin.TelegramID); e != nil {
				return e
			}
		} else if op.Action == "unban" {
			if e = tx.DeleteGroupBan(ctx, chatID, userID); e != nil {
				return e
			}
		}
		trust, e = tx.SetTrustStatusAfterSync(ctx, chatID, userID, p.Status, p.Score, trimStringPtr(p.Notes))
		if e != nil {
			return e
		}
		ok, e := tx.CompleteTrustSync(ctx, chatID, userID, token, trust)
		if e != nil {
			return e
		}
		if !ok {
			return errTrustSyncConflict
		}
		return nil
	})
	if errors.Is(err, errTrustSyncConflict) || (err == nil && conflicted) {
		return c.JSON(409, map[string]string{"error": "Telegram 已执行，但用户状态已变化，请核查后重试"})
	}
	if err != nil {
		return c.JSON(500, map[string]string{"error": "Telegram 已执行，但保存状态失败，请重试"})
	}
	return c.JSON(http.StatusOK, map[string]any{"trust": serializeUserTrust(trust)})
}
