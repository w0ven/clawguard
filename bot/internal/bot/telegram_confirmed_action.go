package bot

import (
	"encoding/json"
	"errors"
	"strconv"

	tele "gopkg.in/telebot.v3"
)

// ErrTelegramResultUnconfirmed means that Telegram did not give a complete,
// affirmative result. An external effect may have happened; callers must not
// mistake this for a definite 4xx rejection or a confirmed success.
var ErrTelegramResultUnconfirmed = errors.New("telegram action result unconfirmed")

type telegramExplicitRejection struct{ err error }

func (e *telegramExplicitRejection) Error() string { return e.err.Error() }
func (e *telegramExplicitRejection) Unwrap() error { return e.err }
func isExplicitTelegramRejection(err error) bool {
	var rejected *telegramExplicitRejection
	return errors.As(err, &rejected)
}

func telegramConfirmedRaw(b *tele.Bot, method string, payload any) error {
	raw, err := b.Raw(method, payload)
	if err != nil {
		var envelope struct {
			OK   *bool `json:"ok"`
			Code int   `json:"error_code"`
		}
		if json.Unmarshal(raw, &envelope) == nil && envelope.OK != nil && !*envelope.OK && envelope.Code >= 400 && envelope.Code < 500 {
			return &telegramExplicitRejection{err: err}
		}
		return err
	}
	return confirmTelegramBoolean(raw)
}

// Match telebot's request shape exactly, but require ok=true/result=true.
func telegramConfirmedBan(b *tele.Bot, chat *tele.Chat, member *tele.ChatMember, revoke ...bool) error {
	params := map[string]string{"chat_id": chat.Recipient(), "user_id": member.User.Recipient(), "until_date": strconv.FormatInt(member.RestrictedUntil, 10)}
	if len(revoke) > 0 {
		params["revoke_messages"] = strconv.FormatBool(revoke[0])
	}
	return telegramConfirmedRaw(b, "kickChatMember", params)
}
func telegramConfirmedUnban(b *tele.Bot, chat *tele.Chat, user *tele.User, onlyBanned ...bool) error {
	params := map[string]string{"chat_id": chat.Recipient(), "user_id": user.Recipient()}
	if len(onlyBanned) > 0 {
		params["only_if_banned"] = strconv.FormatBool(onlyBanned[0])
	}
	return telegramConfirmedRaw(b, "unbanChatMember", params)
}
func telegramConfirmedRestrict(b *tele.Bot, chat *tele.Chat, member *tele.ChatMember) error {
	params := map[string]any{"chat_id": chat.Recipient(), "user_id": member.User.Recipient(), "until_date": strconv.FormatInt(member.RestrictedUntil, 10), "permissions": member.Rights}
	if member.Rights.Independent {
		params["use_independent_chat_permissions"] = true
	}
	return telegramConfirmedRaw(b, "restrictChatMember", params)
}
func telegramConfirmedBanSenderChat(b *tele.Bot, chat *tele.Chat, sender tele.Recipient) error {
	return telegramConfirmedRaw(b, "banChatSenderChat", map[string]string{"chat_id": chat.Recipient(), "sender_chat_id": sender.Recipient()})
}
