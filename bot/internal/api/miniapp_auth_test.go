package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func signedMiniAppData(t *testing.T, token string, now time.Time) string {
	t.Helper()
	values := url.Values{
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
		"query_id":  {"query-1"},
		"signature": {"telegram-ed25519-signature"},
		"user":      {`{"id":42,"first_name":"Mini","username":"admin"}`},
	}
	keys := []string{"auth_date", "query_id", "signature", "user"}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(token))
	signature := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = signature.Write([]byte(strings.Join(parts, "\n")))
	values.Set("hash", hex.EncodeToString(signature.Sum(nil)))
	return values.Encode()
}

func TestValidateTelegramMiniApp(t *testing.T) {
	now := time.Unix(1_720_000_000, 0)
	raw := signedMiniAppData(t, "bot-token", now)
	user, err := validateTelegramMiniApp(raw, "bot-token", now)
	if err != nil || user.ID != 42 || user.Username != "admin" {
		t.Fatalf("validateTelegramMiniApp() user=%+v err=%v", user, err)
	}
	if _, err := validateTelegramMiniApp(raw, "wrong-token", now); err == nil {
		t.Fatal("validateTelegramMiniApp() accepted wrong token")
	}
	tampered, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse signed data: %v", err)
	}
	tampered.Set("signature", "changed-ed25519-signature")
	if _, err := validateTelegramMiniApp(tampered.Encode(), "bot-token", now); err == nil {
		t.Fatal("validateTelegramMiniApp() accepted a changed signature field")
	}
	if _, err := validateTelegramMiniApp(raw, "bot-token", now.Add(telegramMiniAppMaxAge+time.Second)); err == nil {
		t.Fatal("validateTelegramMiniApp() accepted expired data")
	}
}
