package bot

import (
	"errors"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestNormalizeTelegramDeleteNotFoundIsIdempotent(t *testing.T) {
	for _, err := range []error{
		errors.New("telegram: message to delete not found (400)"),
		tele.NewError(400, "Bad Request: message to delete not found"),
	} {
		if got := normalizeTelegramActionError("delete", err); got != nil {
			t.Fatalf("normalizeTelegramActionError() = %v, want nil", got)
		}
	}
}

func TestNormalizeTelegramDeletePreservesRealFailure(t *testing.T) {
	err := errors.New("temporary network failure")
	if got := normalizeTelegramActionError("delete", err); got == nil {
		t.Fatal("normalizeTelegramActionError() = nil, want error")
	}
}
