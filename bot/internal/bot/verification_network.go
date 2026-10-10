package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/openclaw/clawguard/internal/redact"
	tele "gopkg.in/telebot.v3"
)

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

func verificationStartFailureNotice(err error) string {
	text := strings.ToLower(redact.ErrorString(err))
	suffix := "请检查 Telegram 返回错误及成员状态。"
	if transientVerificationNetworkError(err) || strings.Contains(text, "网络异常") {
		suffix = "连接 Telegram 时发生网络异常，无法确认验证限制已生效；不代表 bot 缺少权限。"
	} else if strings.Contains(text, "缺 ban/禁言权限") || strings.Contains(text, "not enough rights") || strings.Contains(text, "administrator rights") {
		suffix = "请检查 bot 是否拥有封禁/禁言成员权限。"
	}
	return "⚠️ 新人入群验证启动失败：" + htmlEscape(redact.ErrorString(err)) + "。" + suffix
}
