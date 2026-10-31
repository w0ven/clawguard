//go:build integration

package bot

import (
	"context"
	"errors"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
	"net/http"
	"testing"
)

func TestTrustPermissionEntryPointsKeepTelegramResultUnknownIntegration(t *testing.T) {
	for _, response := range []string{`<html>bad</html>`, `{"ok":true}`, `{"ok":true,"result":false}`} {
		t.Run(response, func(t *testing.T) {
			ctx := context.Background()
			_, _, s, _ := settlementFixture(t)
			tr := &confirmationTransport{response: response}
			b, err := tele.NewBot(tele.Settings{Token: "123:TEST", URL: "http://telegram.test", Client: &http.Client{Transport: tr}})
			if err != nil {
				t.Fatal(err)
			}
			s.bot = b
			for _, op := range []struct {
				name string
				call func() error
			}{
				{"manual ban", func() error { return s.BanChatUserNoLock(ctx, -90001, 101) }},
				{"manual unban", func() error { _, e := s.SafeUnbanChatUserNoLock(ctx, -90001, 101); return e }},
				{"trust restore", func() error {
					return s.RestoreTrustedChatPermissionsLocked(ctx, store.UserTrust{ChatID: -90001, UserID: 101, Status: "trusted"})
				}},
			} {
				if err := op.call(); !errors.Is(err, ErrTelegramResultUnconfirmed) {
					t.Fatalf("%s: expected unknown, got %v", op.name, err)
				}
			}
		})
	}
}
