package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	tele "gopkg.in/telebot.v3"

	"github.com/jackc/pgx/v5"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

var ErrDualCaptchaUnavailable = errors.New("dual verification is unavailable")
var ErrDualCaptchaConflict = errors.New("verification could not safely restore permissions; contact an administrator")

type dualCaptchaGenerationKey struct{}
type dualCaptchaStartTimeKey struct{}

type dualCaptchaPayload struct {
	Token                string `json:"token"`
	MembershipGeneration string `json:"membership_generation"`
	MiniApp              bool   `json:"miniapp,omitempty"`
}

func (s *Service) startDualCaptchaVerification(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) error {
	generation, _ := ctx.Value(dualCaptchaGenerationKey{}).(string)
	if !s.cfg.DualCaptchaConfigured() || generation == "" {
		return ErrDualCaptchaUnavailable
	}
	token, err := generateVerificationToken()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(dualCaptchaPayload{Token: token, MembershipGeneration: generation, MiniApp: true})
	if err != nil {
		return err
	}
	verifyURL := "https://t.me/" + strings.TrimPrefix(s.cfg.BotUsername, "@") + "?start=v_" + token
	markup := &tele.ReplyMarkup{}
	button := markup.URL("开始验证", verifyURL)
	markup.Inline(markup.Row(button), s.adminVerificationRow(markup, user.ID))
	prompt := fmt.Sprintf("欢迎加入 %s\n请在 %s 内完成验证，验证前暂时无法发言。点击下方按钮，到机器人私聊继续。", mentionHTML(user), formatTimeout(policy.Verify.TimeoutSeconds))
	sent, err := s.sendOrEditVerificationText(ctx, chat, s.existingVerificationMessageID(ctx, chat.ID, user.ID), prompt, markup)
	if err != nil {
		return err
	}
	if err = s.storePendingVerification(ctx, chat.ID, user, "turnstile_hcaptcha", payload, sent.ID, policy.Verify.TimeoutSeconds, policy.Verify.FailAction); err != nil {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}
	return nil
}

func (s *Service) VerifyDualCaptcha(ctx context.Context, token, cfResponse, hResponse, ip string) error {
	return s.verifyDualCaptchaForUser(ctx, token, cfResponse, hResponse, ip, 0, false)
}

func (s *Service) VerifyMiniAppDualCaptcha(ctx context.Context, token, cfResponse, hResponse, ip string, userID int64) error {
	if userID <= 0 {
		return pgx.ErrNoRows
	}
	return s.verifyDualCaptchaForUser(ctx, token, cfResponse, hResponse, ip, userID, false)
}

// RecoverMiniAppDualCaptcha only resumes a previously verified join task.
func (s *Service) RecoverMiniAppDualCaptcha(ctx context.Context, token string, userID int64) error {
	if userID <= 0 {
		return pgx.ErrNoRows
	}
	return s.verifyDualCaptchaForUser(ctx, token, "", "", "", userID, true)
}

func (s *Service) verifyDualCaptchaForUser(ctx context.Context, token, cfResponse, hResponse, ip string, userID int64, recoverOnly bool) error {
	if !recoverOnly && !s.cfg.DualCaptchaConfigured() {
		return ErrDualCaptchaUnavailable
	}
	if len(token) != 36 || (!recoverOnly && (strings.TrimSpace(cfResponse) == "" || strings.TrimSpace(hResponse) == "")) || len(cfResponse) > 2048 || len(hResponse) > 8192 {
		return errDualProvider
	}
	claim, err := generateVerificationToken()
	if err != nil {
		return ErrDualCaptchaUnavailable
	}
	pending, state, err := s.queries.ClaimDualCaptchaForUser(ctx, token, claim, userID)
	if errors.Is(err, store.ErrDualCaptchaConflict) {
		return ErrDualCaptchaConflict
	}
	if err != nil {
		return err
	}
	defer func() { _ = s.queries.ReleaseDualCaptchaClaim(context.Background(), pending.ID, claim) }()
	var payload dualCaptchaPayload
	if json.Unmarshal(pending.Payload, &payload) != nil || payload.Token != token || payload.MembershipGeneration == "" {
		return ErrDualCaptchaConflict
	}
	if recoverOnly && state != "verified" && state != "applying" {
		return errDualProvider
	}
	if state == "validating" {
		u, err := url.Parse(s.cfg.PublicBaseURL)
		if err != nil || u.Hostname() == "" {
			return ErrDualCaptchaUnavailable
		}
		if err := verifyDualProviders(ctx, dualCaptchaHTTPClient, s.cfg.TurnstileSecret, s.cfg.HCaptchaSecret, s.cfg.HCaptchaSiteKey, u.Hostname(), token, cfResponse, hResponse, ip); err != nil {
			return err
		}
		ok, err := s.queries.UpdateDualCaptchaClaim(ctx, pending.ID, claim, "validating", "verified")
		if err != nil || !ok {
			return ErrDualCaptchaConflict
		}
		state = "verified"
	}
	// Serialize the safety checks, Telegram permission change and durable finish
	// with administrative review and new penalties for this same subject.
	release, err := s.AcquireModerationReview(ctx, pending.ChatID, pending.UserID)
	if err != nil {
		return ErrDualCaptchaUnavailable
	}
	defer release()
	current, err := s.membershipSessionCurrent(ctx, pending.ChatID, pending.UserID, payload.MembershipGeneration)
	if err != nil {
		return ErrDualCaptchaUnavailable
	}
	if !current {
		return s.markDualCaptchaConflict(ctx, pending.ID, claim)
	}
	safe, err := s.queries.DualCaptchaSafety(ctx, pending.ChatID, pending.UserID)
	if err != nil {
		return ErrDualCaptchaUnavailable
	}
	if !safe {
		return s.markDualCaptchaConflict(ctx, pending.ID, claim)
	}
	system, err := s.queries.GetSystemState(ctx)
	if err != nil || system.Frozen || system.ActionsPaused {
		return ErrDualCaptchaConflict
	}
	policy, err := config.LoadPolicy(ctx, s.queries, pending.ChatID)
	if err != nil || !policy.Verify.Enabled {
		return ErrDualCaptchaConflict
	}
	chat, user := &tele.Chat{ID: pending.ChatID}, &tele.User{ID: pending.UserID, Username: derefString(pending.Username), FirstName: derefString(pending.FirstName)}
	if exempt, _, err := s.queries.GroupExemptionStatus(ctx, pending.ChatID, pending.UserID); err != nil || exempt {
		return ErrDualCaptchaConflict
	}
	member, err := s.bot.ChatMemberOf(chat, user)
	if err != nil || member == nil {
		return ErrDualCaptchaConflict
	}
	var target dualPermissionTarget
	if state == "applying" {
		raw, err := s.queries.DualCaptchaTargetRights(ctx, pending.ID, claim)
		if err != nil {
			return ErrDualCaptchaUnavailable
		}
		if len(raw) == 0 || json.Unmarshal(raw, &target) != nil {
			return s.markDualCaptchaConflict(ctx, pending.ID, claim)
		}
		if dualPermissionsMatch(member, target) {
			return s.finishDualCaptcha(ctx, pending, claim, chat, user)
		}
	}
	if member.Role != tele.Restricted || member.RestrictedUntil != 0 || member.CanSendMessages || member.CanSendOther || member.CanSendPolls || member.CanSendAudios || member.CanSendDocuments || member.CanSendPhotos || member.CanSendVideos || member.CanSendVoiceNotes || member.CanSendVideoNotes || member.CanInviteUsers || member.CanPinMessages || member.CanChangeInfo || member.CanManageTopics || member.CanAddPreviews {
		return s.markDualCaptchaConflict(ctx, pending.ID, claim)
	}
	if state == "verified" {
		target, err = s.dualVerificationTarget(ctx, chat, user, policy)
		if errors.Is(err, ErrDualCaptchaConflict) {
			return s.markDualCaptchaConflict(ctx, pending.ID, claim)
		}
		if err != nil {
			return ErrDualCaptchaUnavailable
		}
		raw, err := json.Marshal(target)
		if err != nil {
			return ErrDualCaptchaUnavailable
		}
		ok, err := s.queries.BeginDualCaptchaApply(ctx, pending.ID, claim, raw)
		if err != nil || !ok {
			return ErrDualCaptchaConflict
		}
	}
	if ok, err := s.queries.UpdateDualCaptchaClaim(ctx, pending.ID, claim, "applying", "applying"); err != nil || !ok {
		return ErrDualCaptchaConflict
	}
	if safe, err := s.queries.DualCaptchaSafety(ctx, pending.ChatID, pending.UserID); err != nil {
		return ErrDualCaptchaUnavailable
	} else if !safe {
		return s.markDualCaptchaConflict(ctx, pending.ID, claim)
	}
	if current, err := s.membershipSessionCurrent(ctx, pending.ChatID, pending.UserID, payload.MembershipGeneration); err != nil {
		return ErrDualCaptchaUnavailable
	} else if !current {
		return s.markDualCaptchaConflict(ctx, pending.ID, claim)
	}
	if err := s.bot.Restrict(chat, &tele.ChatMember{User: user, Rights: target.Rights}); err != nil {
		return ErrDualCaptchaUnavailable
	}
	return s.finishDualCaptcha(ctx, pending, claim, chat, user)
}

func (s *Service) markDualCaptchaConflict(ctx context.Context, id int64, claim string) error {
	_ = s.queries.MarkDualCaptchaConflict(ctx, id, claim)
	return ErrDualCaptchaConflict
}

type dualPermissionTarget struct {
	Restricted bool        `json:"restricted"`
	Rights     tele.Rights `json:"rights"`
}

func (s *Service) dualVerificationTarget(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) (dualPermissionTarget, error) {
	trust, err := s.queries.GetUserTrust(ctx, chat.ID, user.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return dualPermissionTarget{}, err
	}
	if err == nil && trust.Status == "banned" {
		return dualPermissionTarget{}, ErrDualCaptchaConflict
	}
	if err == nil && ungraduatedPermissionRestrictionEnabled(policy) && isUngraduatedTrustStatus(trust.Status) {
		return dualPermissionTarget{Restricted: true, Rights: ungraduatedRestrictedRights(policy)}, nil
	}
	return dualPermissionTarget{Rights: normalMemberRights()}, nil
}

func dualPermissionsMatch(member *tele.ChatMember, target dualPermissionTarget) bool {
	if !target.Restricted {
		return member.Role == tele.Member
	}
	if member.Role != tele.Restricted || member.RestrictedUntil != 0 {
		return false
	}
	a, b := member.Rights, target.Rights
	return a.CanSendMessages == b.CanSendMessages && a.CanSendAudios == b.CanSendAudios &&
		a.CanSendDocuments == b.CanSendDocuments && a.CanSendPhotos == b.CanSendPhotos &&
		a.CanSendVideos == b.CanSendVideos && a.CanSendVideoNotes == b.CanSendVideoNotes &&
		a.CanSendVoiceNotes == b.CanSendVoiceNotes && a.CanSendPolls == b.CanSendPolls &&
		a.CanSendOther == b.CanSendOther && a.CanAddPreviews == b.CanAddPreviews &&
		a.CanInviteUsers == b.CanInviteUsers && a.CanPinMessages == b.CanPinMessages &&
		a.CanChangeInfo == b.CanChangeInfo && a.CanManageTopics == b.CanManageTopics
}

func (s *Service) finishDualCaptcha(ctx context.Context, pending store.PendingVerification, claim string, chat *tele.Chat, user *tele.User) error {
	var payload dualCaptchaPayload
	if json.Unmarshal(pending.Payload, &payload) != nil || payload.MembershipGeneration == "" {
		return ErrDualCaptchaConflict
	}
	if safe, err := s.queries.DualCaptchaSafety(ctx, pending.ChatID, pending.UserID); err != nil {
		return ErrDualCaptchaUnavailable
	} else if !safe {
		return s.markDualCaptchaConflict(ctx, pending.ID, claim)
	}
	if current, err := s.membershipSessionCurrent(ctx, pending.ChatID, pending.UserID, payload.MembershipGeneration); err != nil {
		return ErrDualCaptchaUnavailable
	} else if !current {
		return s.markDualCaptchaConflict(ctx, pending.ID, claim)
	}
	ok, err := s.queries.FinishDualCaptcha(ctx, pending.ID, claim)
	if err != nil || !ok {
		return ErrDualCaptchaConflict
	}
	if payload.MiniApp {
		s.notifyMiniAppVerification(pending.ID, pending.UserID)
	}
	s.ensureRuntimeGuards()
	s.joinProtector.ReleasePending(chat.ID)
	if !s.sendWelcomeMessage(ctx, chat, user, &pending) {
		s.finalizeVerificationPrompt(chat, pending, verificationResultHTML(user, "passed"))
	}
	return nil
}
