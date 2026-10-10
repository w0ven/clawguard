package bot

import (
	"context"
	"errors"
	tele "gopkg.in/telebot.v3"
	"strings"
	"testing"
	"time"
)

func TestVerificationNetworkRecovery(t *testing.T) {
	reset := errors.New("telebot: Post: read: connection reset by peer")
	for _, tc := range []struct {
		name          string
		member        *tele.ChatMember
		inspectErr    error
		firstErr      error
		succeedSecond bool
		wantCalls     int
		wantErr       bool
	}{
		{"already_applied", &tele.ChatMember{Role: tele.Restricted, Member: true}, nil, reset, false, 1, false},
		{"not_applied_then_success", &tele.ChatMember{Role: tele.Member}, nil, reset, true, 2, false},
		{"bounded", &tele.ChatMember{Role: tele.Member}, nil, reset, false, 3, true},
		{"read_failed", nil, reset, reset, false, 1, true},
		{"left", &tele.ChatMember{Role: tele.Left}, nil, reset, false, 1, true},
		{"admin", &tele.ChatMember{Role: tele.Administrator}, nil, reset, false, 1, true},
		{"permission", nil, nil, errors.New("not enough rights"), false, 1, true},
		{"partial", &tele.ChatMember{Role: tele.Restricted, Member: true, Rights: tele.Rights{CanSendPhotos: true}}, nil, reset, true, 2, false},
		{"temporary", &tele.ChatMember{Role: tele.Restricted, Member: true, RestrictedUntil: 1234}, nil, reset, true, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := restrictVerificationWithRecovery(context.Background(), func() error {
				calls++
				if tc.succeedSecond && calls == 2 {
					return nil
				}
				return tc.firstErr
			}, func() (*tele.ChatMember, error) { return tc.member, tc.inspectErr }, func(context.Context, time.Duration) error { return nil })
			if calls != tc.wantCalls || (err != nil) != tc.wantErr {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
func TestVerificationRecoveryCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := restrictVerificationWithRecovery(ctx, func() error { t.Fatal("called after cancellation"); return nil }, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestVerificationFailureNotice(t *testing.T) {
	network := verificationStartFailureNotice(normalizeTelegramActionError("restrict", errors.New("connection reset by peer")))
	if strings.Contains(network, "请检查 bot 是否拥有") || !strings.Contains(network, "网络异常") {
		t.Fatal(network)
	}
	permission := verificationStartFailureNotice(normalizeTelegramActionError("restrict", errors.New("not enough rights")))
	if !strings.Contains(permission, "请检查 bot 是否拥有") {
		t.Fatal(permission)
	}
	other := verificationStartFailureNotice(errors.New("user not found"))
	if strings.Contains(other, "请检查 bot 是否拥有") {
		t.Fatal(other)
	}
}
