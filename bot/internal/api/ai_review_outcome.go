package api

import (
	"errors"
	tele "gopkg.in/telebot.v3"
	"regexp"
)

// Telegram's explicit 4xx response means the operation was rejected. An HTTP
// transport error, invalid response or 5xx can follow an applied operation.
var telegramExplicit4xx = regexp.MustCompile(`^telegram: [^\r\n]* \(4[0-9][0-9]\)$`)

func telegramDefinitivelyRejected(err error) bool {
	var v *tele.Error
	if errors.As(err, &v) {
		return v.Code >= 400 && v.Code < 500
	}
	var flood tele.FloodError
	if errors.As(err, &flood) {
		return true
	}
	// Telebot formats unrecognised Telegram API responses as plain errors.
	return err != nil && telegramExplicit4xx.MatchString(err.Error())
}
