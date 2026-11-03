package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/store"
)

var errAppealConflict = errors.New("appeal already decided")

func (s *Server) decideAppealReview(c echo.Context) error {
	admin, _ := currentAdmin(c)
	scope, global := adminScopeFilter(admin)
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		return c.JSON(400, map[string]string{"error": "invalid id"})
	}
	var p struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if e = c.Bind(&p); e != nil {
		return c.JSON(400, map[string]string{"error": "invalid body"})
	}
	p.Note = strings.TrimSpace(p.Note)
	if (p.Decision != "approve" && p.Decision != "uphold") || p.Note == "" || utf8.RuneCountInString(p.Note) > 1000 {
		return c.JSON(400, map[string]string{"error": "请选择处理结果并填写公开说明（最多1000字）"})
	}
	return s.runAppealDecision(c, id, global, scope, admin, p.Decision, p.Note, false)
}
func (s *Server) retryAppealReview(c echo.Context) error {
	admin, _ := currentAdmin(c)
	scope, global := adminScopeFilter(admin)
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		return c.JSON(400, map[string]string{"error": "invalid id"})
	}
	return s.runAppealDecision(c, id, global, scope, admin, "", "", true)
}
func (s *Server) runAppealDecision(c echo.Context, id int64, global bool, scope []int64, admin store.Admin, decision, note string, retry bool) error {
	ctx := c.Request().Context()
	q := s.botService.Queries()
	v, e := q.GetAppealReview(ctx, id, global, scope)
	if errors.Is(e, pgx.ErrNoRows) {
		return c.JSON(404, map[string]string{"error": "appeal not found"})
	}
	if e != nil {
		return c.JSON(503, map[string]string{"error": "申诉记录暂不可用"})
	}
	release, e := s.botService.AcquireModerationReview(ctx, v.ChatID, v.UserID)
	if e != nil {
		return c.JSON(409, map[string]string{"error": "处理正在进行，请稍后重试"})
	}
	defer release()
	shouldExecute := false
	e = q.Transact(ctx, func(tx *store.Queries) error {
		current, err := tx.LockAppealReview(ctx, id, global, scope)
		if err != nil {
			return err
		}
		v = current
		if retry {
			if current.Decision == nil || *current.Decision != "approve" || (current.Status != "failed" && current.Status != "executing") {
				return errAppealConflict
			}
			shouldExecute = true
			return nil
		}
		if current.Decision != nil {
			if *current.Decision != decision {
				return errAppealConflict
			}
			return nil
		}
		if current.Status != "pending" {
			return errAppealConflict
		}
		before, _ := json.Marshal(map[string]any{"appeal_id": id, "status": "pending"})
		after, _ := json.Marshal(map[string]any{"appeal_id": id, "decision": decision, "public_note": note})
		if err = tx.SetAppealDecision(ctx, id, admin.ID, decision, note); err != nil {
			return err
		}
		if err = writeAuditWithQueries(ctx, tx, admin, "appeal", &current.ChatID, "appeal_decision", before, after); err != nil {
			return err
		}
		if decision == "uphold" {
			return tx.QueueAppealResult(ctx, id)
		}
		shouldExecute = true
		return nil
	})
	if errors.Is(e, pgx.ErrNoRows) {
		return c.JSON(404, map[string]string{"error": "appeal not found"})
	}
	if errors.Is(e, errAppealConflict) {
		return c.JSON(409, map[string]string{"error": "该申诉已有裁决，不能覆盖"})
	}
	if e != nil {
		return c.JSON(503, map[string]string{"error": "保存审核结果失败，请重试"})
	}
	if shouldExecute {
		state, result := s.executeAppealLocked(ctx, v)
		e = q.Transact(ctx, func(tx *store.Queries) error {
			current, err := tx.LockAppealReview(ctx, id, global, scope)
			if err != nil {
				return err
			}
			if current.Status == "completed" || current.Status == "manual_review" {
				return nil
			}
			if err = tx.SetAppealExecutionResult(ctx, id, state, result); err != nil {
				return err
			}
			before, _ := json.Marshal(map[string]any{"appeal_id": id, "status": current.Status})
			after, _ := json.Marshal(map[string]any{"appeal_id": id, "status": state, "execution_result": result})
			if err = writeAuditWithQueries(ctx, tx, admin, "appeal", &current.ChatID, "appeal_execution", before, after); err != nil {
				return err
			}
			if state == "completed" || state == "manual_review" {
				return tx.QueueAppealResult(ctx, id)
			}
			return nil
		})
		if e != nil {
			return c.JSON(503, map[string]string{"error": "审核决定已保存，执行结果未结算，请重试或核查"})
		}
	}
	v, e = q.GetAppealReview(ctx, id, global, scope)
	if e != nil {
		return c.JSON(503, map[string]string{"error": "申诉记录暂不可用"})
	}
	return c.JSON(http.StatusOK, map[string]any{"appeal": v})
}
func (s *Server) executeAppealLocked(ctx context.Context, v store.AppealReview) (string, string) {
	if v.CaseResult != "executed" {
		return "manual_review", "审核通过，但原处理实际执行情况需人工核查，现有限制未自动解除"
	}
	action := strings.ToLower(v.Action)
	if action == "delete" {
		return "completed", "审核通过，已删除的消息无法恢复"
	}
	if action == "kick" {
		return "manual_review", "审核通过，重新入群限制需管理员核查；系统不会自动将用户拉回"
	}
	var owner struct {
		AIDecisionID int64 `json:"ai_decision_id"`
		WarningID    int64 `json:"warning_id"`
	}
	if json.Unmarshal(v.Ownership, &owner) != nil {
		return "manual_review", "审核通过，处理归属不明，现有限制未自动解除"
	}
	if (action == "warn" || action == "delete_warn" || action == "delete_and_warn") && owner.WarningID > 0 {
		err := s.botService.Queries().ConsumeAppealWarning(ctx, v.ID, owner.WarningID, v.ChatID, v.UserID)
		if err == nil {
			if action == "warn" {
				return "completed", "审核通过，本次警告已撤销"
			}
			return "completed", "审核通过，本次警告已撤销；已删除的消息无法恢复"
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "failed", "审核通过，警告处理暂未完成，可由管理员重试"
		}
		return "manual_review", "审核通过，但警告归属或当前状态需人工核查"
	}
	if owner.AIDecisionID <= 0 {
		return "manual_review", "审核通过，处理归属不明，现有限制未自动解除"
	}
	q := s.botService.Queries()
	d, err := q.GetAIDecisionByID(ctx, owner.AIDecisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "manual_review", "审核通过，处理归属不明，现有限制未自动解除"
	}
	if err != nil {
		return "failed", "审核通过，处理信息暂不可用，可由管理员重试"
	}
	if d.ChatID != v.ChatID || d.UserID != v.UserID {
		return "manual_review", "审核通过，处理归属不明，现有限制未自动解除"
	}
	if strings.Contains(action, "ban") && strings.Contains(strings.ToLower(d.ActionTaken), "ban") {
		outcome, e := s.reviewAIBanFalsePositiveLocked(ctx, d.ID, 0, false)
		if e == nil && outcome.State == "completed" {
			return "completed", "审核通过，原封禁已解除；如有已删除的消息，无法恢复"
		}
		if e == nil {
			return "manual_review", "审核通过，原封禁状态需人工核查，未确认解除"
		}
		return s.appealBanFailureState(ctx, d.ID)
	}
	if strings.Contains(action, "mute") && strings.Contains(strings.ToLower(d.ActionTaken), "mute") {
		value := "false_positive"
		d, _, err = s.applyAIDecisionOverride(ctx, d.ID, &value)
		if err != nil {
			return "failed", "审核通过，解除步骤暂未完成，可由管理员重试"
		}
		_, warning := s.reviewAIMuteLocked(ctx, d, false)
		var state string
		err = q.Transact(ctx, func(tx *store.Queries) error {
			cl, e := tx.LockAIMuteClaim(ctx, d.ID)
			if e != nil {
				return e
			}
			state = cl.State
			return nil
		})
		if errors.Is(err, pgx.ErrNoRows) {
			if strings.Contains(warning, "请重试") {
				return "failed", "审核通过，解除步骤暂未完成，可由管理员重试"
			}
			return "manual_review", "审核通过，原禁言状态需人工核查，未确认解除"
		}
		if err != nil {
			return "failed", "审核通过，处理状态暂不可用，可由管理员重试"
		}
		switch state {
		case "completed":
			return "completed", "审核通过，原禁言已解除；如有已删除的消息，无法恢复"
		case "claimed", "telegram_done":
			return "failed", "审核通过，解除步骤暂未完成，可由管理员重试"
		default:
			return "manual_review", "审核通过，原禁言状态需人工核查，未确认解除"
		}
	}
	return "manual_review", "审核通过，原处理需要人工核查，现有限制未自动解除"
}
func (s *Server) appealBanFailureState(ctx context.Context, id int64) (string, string) {
	var state string
	err := s.botService.Queries().Transact(ctx, func(tx *store.Queries) error {
		cl, e := tx.GetAIReviewClaim(ctx, id)
		if e != nil {
			return e
		}
		state = cl.State
		return nil
	})
	if err != nil || state == "claimed" || state == "telegram_done" {
		return "failed", "审核通过，解除步骤暂未完成，可由管理员重试"
	}
	if err == nil && state == "completed" {
		return "completed", "审核通过，原封禁已解除；如有已删除的消息，无法恢复"
	}
	return "manual_review", "审核通过，原封禁状态需人工核查，未确认解除"
}

func (s *Server) handleBotAppealDecision(ctx context.Context, adminTelegramID, id int64, action string) (*store.AppealReview, string, error) {
	q := s.botService.Queries()
	if q == nil {
		return nil, "申诉处理服务暂未就绪", errors.New("queries unavailable")
	}

	admin, err := q.GetAdminByTelegramID(ctx, adminTelegramID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			isCfgAdmin := false
			for _, cfgID := range s.cfg.AdminTelegramIDs {
				if cfgID != 0 && cfgID == adminTelegramID {
					isCfgAdmin = true
					break
				}
			}
			if !isCfgAdmin {
				return nil, "仅管理员可处理申诉", errors.New("unauthorized admin")
			}
			admin = store.Admin{
				ID:         adminTelegramID,
				TelegramID: adminTelegramID,
				Role:       "owner",
				GroupScope: []byte(`[]`),
			}
		} else {
			return nil, "暂时无法验证管理员身份，请稍后重试", err
		}
	}

	scope, global := adminScopeFilter(admin)
	v, err := q.GetAppealReview(ctx, id, global, scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "该申诉不存在或不在您的管理范围内", err
	}
	if err != nil {
		return nil, "申诉记录暂不可用，请稍后重试", err
	}

	release, err := s.botService.AcquireModerationReview(ctx, v.ChatID, v.UserID)
	if err != nil {
		return &v, "处理正在进行，请稍后重试", err
	}
	defer release()

	retry := action == "retry"
	decision := action
	note := ""
	switch action {
	case "approve":
		note = "管理员通过 Bot 私信审核通过"
	case "uphold":
		note = "管理员通过 Bot 私信审核维持原处理"
	case "retry":
		decision = ""
	default:
		return &v, "操作类型无效", errors.New("invalid action")
	}

	shouldExecute := false
	err = q.Transact(ctx, func(tx *store.Queries) error {
		current, txErr := tx.LockAppealReview(ctx, id, global, scope)
		if txErr != nil {
			return txErr
		}
		v = current
		if retry {
			if current.Decision == nil || *current.Decision != "approve" || (current.Status != "failed" && current.Status != "executing") {
				return errAppealConflict
			}
			shouldExecute = true
			return nil
		}
		if current.Decision != nil {
			if *current.Decision != decision {
				return errAppealConflict
			}
			return nil
		}
		if current.Status != "pending" {
			return errAppealConflict
		}
		before, _ := json.Marshal(map[string]any{"appeal_id": id, "status": "pending", "source": "telegram_bot"})
		after, _ := json.Marshal(map[string]any{"appeal_id": id, "decision": decision, "public_note": note, "source": "telegram_bot"})
		if txErr = tx.SetAppealDecision(ctx, id, admin.ID, decision, note); txErr != nil {
			return txErr
		}
		if txErr = writeAuditWithQueries(ctx, tx, admin, "appeal", &current.ChatID, "appeal_decision", before, after); txErr != nil {
			return txErr
		}
		if decision == "uphold" {
			return tx.QueueAppealResult(ctx, id)
		}
		shouldExecute = true
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "该申诉不存在或不在您的管理范围内", err
	}
	if errors.Is(err, errAppealConflict) {
		latest, _ := q.GetAppealReview(ctx, id, global, scope)
		return &latest, "该申诉已由其他管理员或在其他入口处理，卡片已刷新", err
	}
	if err != nil {
		return &v, "保存审核结果失败，请稍后重试", err
	}

	if shouldExecute {
		state, result := s.executeAppealLocked(ctx, v)
		err = q.Transact(ctx, func(tx *store.Queries) error {
			current, txErr := tx.LockAppealReview(ctx, id, global, scope)
			if txErr != nil {
				return txErr
			}
			if current.Status == "completed" || current.Status == "manual_review" {
				return nil
			}
			if txErr = tx.SetAppealExecutionResult(ctx, id, state, result); txErr != nil {
				return txErr
			}
			before, _ := json.Marshal(map[string]any{"appeal_id": id, "status": current.Status, "source": "telegram_bot"})
			after, _ := json.Marshal(map[string]any{"appeal_id": id, "status": state, "execution_result": result, "source": "telegram_bot"})
			if txErr = writeAuditWithQueries(ctx, tx, admin, "appeal", &current.ChatID, "appeal_execution", before, after); txErr != nil {
				return txErr
			}
			if state == "completed" || state == "manual_review" {
				return tx.QueueAppealResult(ctx, id)
			}
			return nil
		})
		if err != nil {
			latest, _ := q.GetAppealReview(ctx, id, global, scope)
			return &latest, "审核决定已保存，但执行结果未结算，请点击重试或前往后台核查", err
		}
	}

	latest, err := q.GetAppealReview(ctx, id, global, scope)
	if err != nil {
		return &v, "已处理，请稍后刷新查看状态", nil
	}
	alertText := "✅ 已驳回申诉，将私信通知用户"
	if latest.Decision != nil && *latest.Decision == "approve" {
		switch latest.Status {
		case "completed":
			alertText = "✅ 申诉已通过，处罚已解除"
		case "manual_review":
			alertText = "⚠️ 申诉已通过，但原限制需人工核查"
		default:
			alertText = "⚠️ 申诉已记录通过，解除步骤未完成，可点击重试"
		}
	}
	return &latest, alertText, nil
}
