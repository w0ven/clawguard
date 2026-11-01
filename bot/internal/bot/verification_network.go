package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

func verificationStartHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	// The dialer's own deadline must expire before Client.Timeout; the latter
	// can discard the typed net.OpError and would then be correctly treated as
	// an unknown outcome rather than a proven pre-dispatch failure.
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// The dedicated start client never follows redirects. A typed dial error on
// that client therefore proves this POST did not reach Telegram. Timeouts,
// EOF, TLS and read/write errors by themselves never establish this fact.
func verificationRequestNotSent(err error) bool {
	var u *url.Error
	if !errors.As(err, &u) || errors.Is(err, context.Canceled) {
		return false
	}
	var op *net.OpError
	return errors.As(u.Err, &op) && op.Op == "dial" && (op.Net == "tcp" || op.Net == "tcp4" || op.Net == "tcp6")
}

type verificationStartNotSentError struct{ cause error }

func (e *verificationStartNotSentError) Error() string {
	return "verification start not sent; manual review required: " + e.cause.Error()
}
func (e *verificationStartNotSentError) Unwrap() error { return e.cause }

// Check authoritative DB state rather than cache. The shared safety query
// rejects group-ban records and active/pending independent AI mute ownership.
func (s *Service) verificationStartRecoveryAllowed(ctx context.Context, chatID, userID int64) (bool, error) {
	if s.queries == nil {
		return false, errors.New("recovery state unavailable")
	}
	state, err := s.queries.GetSystemState(ctx)
	if err != nil || state.Frozen || state.ActionsPaused {
		return false, err
	}
	safe, err := s.queries.DualCaptchaSafety(ctx, chatID, userID)
	if err != nil || !safe {
		return false, err
	}
	exempt, banned, err := s.queries.GroupExemptionStatus(ctx, chatID, userID)
	return err == nil && !exempt && !banned, err
}

// Only a newly claimed verification_start calls this function. Old dispatched
// records never enter it. Each dedicated HTTP call is bounded to eight seconds,
// with at most three restrict calls and two reads; no new call is begun after
// 35 seconds, so the transport bounds the total to at most 43 seconds plus DB.
func (s *Service) restrictVerificationStartWithRecovery(ctx context.Context, chat *tele.Chat, user *tele.User, member *tele.ChatMember, generation string) error {
	b := s.verificationStartBot
	if b == nil {
		// Services without the dedicated no-redirect transport cannot prove a
		// request was unsent. Preserve the existing conservative path.
		return telegramConfirmedRestrict(s.bot, chat, member)
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	started := time.Now()
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		last = telegramConfirmedRestrict(b, chat, member)
		if last == nil || !verificationRequestNotSent(last) {
			return last
		}
		if attempt == 2 || generation == "" || time.Since(started) >= 35*time.Second {
			break
		}
		if err := waitVerificationRetry(ctx, time.Duration(attempt+1)*300*time.Millisecond); err != nil {
			return &verificationStartNotSentError{errors.Join(last, err)}
		}
		if time.Since(started) >= 35*time.Second {
			break
		}
		current, err := s.membershipSessionCurrent(ctx, chat.ID, user.ID, generation)
		if err != nil || !current {
			return &verificationStartNotSentError{errors.Join(last, errors.New("membership changed"), err)}
		}
		allowed, err := s.verificationStartRecoveryAllowed(ctx, chat.ID, user.ID)
		if err != nil || !allowed {
			return &verificationStartNotSentError{errors.Join(last, errors.New("action, exemption or independent punishment state"), err)}
		}
		actual, err := b.ChatMemberOf(chat, user)
		if err != nil || actual == nil || actual.User == nil || actual.User.ID != user.ID || actual.Role != tele.Member {
			return &verificationStartNotSentError{errors.Join(last, errors.New("member absent, privileged, restricted, or unavailable"), err)}
		}
		allowed, err = s.verificationStartRecoveryAllowed(ctx, chat.ID, user.ID)
		if err != nil || !allowed || ctx.Err() != nil {
			return &verificationStartNotSentError{errors.Join(last, errors.New("action, exemption or independent punishment state"), err, ctx.Err())}
		}
		current, err = s.membershipSessionCurrent(ctx, chat.ID, user.ID, generation)
		if err != nil || !current || time.Since(started) >= 35*time.Second {
			return &verificationStartNotSentError{errors.Join(last, errors.New("membership or action state changed"), err)}
		}
	}
	return &verificationStartNotSentError{last}
}

func transientVerificationNetworkError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var tg *tele.Error
	if errors.As(err, &tg) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"connection reset by peer", "broken pipe", "tls handshake timeout", "i/o timeout", "unexpected eof"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func verificationRestrictionApplied(m *tele.ChatMember) bool {
	if m == nil || m.Role != tele.Restricted || !m.Member || m.RestrictedUntil != 0 {
		return false
	}
	r := m.Rights
	return !r.CanSendMessages && !r.CanSendMedia && !r.CanSendAudios && !r.CanSendDocuments && !r.CanSendPhotos && !r.CanSendVideos && !r.CanSendVideoNotes && !r.CanSendVoiceNotes && !r.CanSendPolls && !r.CanSendOther && !r.CanAddPreviews && !r.CanInviteUsers && !r.CanChangeInfo && !r.CanPinMessages && !r.CanManageTopics
}

// Only retry this particular restriction after reading its actual outcome.
// Never add generic HTTP retries: other Telegram methods are not idempotent.
func restrictVerificationWithRecovery(ctx context.Context, restrict func() error, inspect func() (*tele.ChatMember, error), wait func(context.Context, time.Duration) error) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := restrict()
		if err == nil || !transientVerificationNetworkError(err) {
			return err
		}
		if waitErr := wait(ctx, time.Duration(attempt+1)*300*time.Millisecond); waitErr != nil {
			return waitErr
		}
		member, checkErr := inspect()
		if checkErr != nil {
			return fmt.Errorf("Telegram 网络异常，限制结果未确认；未重复处罚: %w", errors.Join(redact.Error(err), redact.Error(checkErr)))
		}
		if verificationRestrictionApplied(member) {
			return nil
		}
		if member == nil || (member.Role != tele.Member && !(member.Role == tele.Restricted && member.Member)) {
			return fmt.Errorf("Telegram 网络异常后成员身份已变化；未重试限制: %w", redact.Error(err))
		}
		if attempt == 2 {
			return err
		}
	}
	return nil
}

func waitVerificationRetry(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// A transaction serializes notices for the same generation, and rolls back
// on a failed send. Telegram delivery followed by a crash before commit can
// still produce a duplicate; the API does not support an idempotency key.
func (s *Service) sendVerificationStartFailureNotice(ctx context.Context, chat *tele.Chat, user *tele.User, generation string, cause error) error {
	message := fmt.Sprintf("%s 成员 ID：<code>%d</code>；请管理员核查成员状态并处理验证。", verificationStartFailureNotice(cause), user.ID)
	send := func() error {
		_, err := s.sendThrottled(ctx, chat, message, &tele.SendOptions{ParseMode: tele.ModeHTML, DisableWebPagePreview: true})
		return err
	}
	key := membershipCleanupKey("verification_start_notice", chat.ID, user.ID, generation)
	var err error
	if key == "" || s.queries == nil {
		err = send()
	} else {
		err = s.queries.Transact(ctx, func(q *store.Queries) error {
			sent, err := q.LockVerificationStartNotice(ctx, key, chat.ID, user.ID)
			if err != nil || sent {
				return err
			}
			if err := send(); err != nil {
				return err
			}
			return q.CompleteCleanupDispatch(ctx, key, "sent")
		})
	}
	if err != nil {
		s.logger.Warn("send verification start warning failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
	}
	return err
}

func verificationStartFailureNotice(err error) string {
	text := strings.ToLower(redact.ErrorString(err))
	suffix := "请检查 Telegram 返回错误及成员状态。"
	if transientVerificationNetworkError(err) || strings.Contains(text, "网络异常") {
		suffix = "连接 Telegram 时发生网络异常，无法确认验证限制已生效；不代表 bot 缺少权限。"
	} else if strings.Contains(text, "缺 ban/禁言权限") || strings.Contains(text, "not enough rights") || strings.Contains(text, "administrator rights") {
		suffix = "请检查 bot 是否拥有封禁/禁言成员权限。"
	}
	return "⚠️ 新人入群验证启动失败。" + suffix
}
