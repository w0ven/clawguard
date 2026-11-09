package api

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func signedMiniAppDataEd25519(t *testing.T, botToken string, priv ed25519.PrivateKey, now time.Time) string {
	t.Helper()
	botID, _, _ := strings.Cut(botToken, ":")
	values := url.Values{
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
		"query_id":  {"query-1"},
		"user":      {`{"id":42,"first_name":"Mini","username":"admin"}`},
	}

	edKeys := []string{"auth_date", "query_id", "user"}
	sort.Strings(edKeys)
	edParts := make([]string, 0, len(edKeys))
	for _, key := range edKeys {
		edParts = append(edParts, key+"="+values.Get(key))
	}
	edMsg := botID + ":WebAppData\n" + strings.Join(edParts, "\n")
	edSig := ed25519.Sign(priv, []byte(edMsg))
	values.Set("signature", base64.RawURLEncoding.EncodeToString(edSig))

	hmacKeys := []string{"auth_date", "query_id", "signature", "user"}
	sort.Strings(hmacKeys)
	hmacParts := make([]string, 0, len(hmacKeys))
	for _, key := range hmacKeys {
		hmacParts = append(hmacParts, key+"="+values.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(strings.Join(hmacParts, "\n")))
	values.Set("hash", hex.EncodeToString(check.Sum(nil)))
	return values.Encode()
}

func TestValidateTelegramMiniAppEd25519(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey other: %v", err)
	}

	now := time.Unix(1_720_000_000, 0)
	const botToken = "123456:TEST_BOT_TOKEN"
	raw := signedMiniAppDataEd25519(t, botToken, priv, now)

	user, digest, err := validateTelegramMiniAppEd25519(raw, botToken, pub, now)
	if err != nil || user.ID != 42 || user.Username != "admin" || digest == "" {
		t.Fatalf("validateTelegramMiniAppEd25519() user=%+v digest=%q err=%v", user, digest, err)
	}

	// Attacker knows BOT_TOKEN and recomputes valid HMAC hash, but lacks Telegram's Ed25519 private key.
	if _, _, err := validateTelegramMiniAppEd25519(raw, botToken, otherPub, now); err == nil {
		t.Fatal("validateTelegramMiniAppEd25519() accepted valid BOT_TOKEN HMAC without valid Telegram Ed25519 signature")
	}

	// Missing signature parameter despite valid BOT_TOKEN HMAC must be rejected.
	noSigValues := url.Values{
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
		"query_id":  {"query-no-sig"},
		"user":      {`{"id":42,"first_name":"Mini","username":"admin"}`},
	}
	sec := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = sec.Write([]byte(botToken))
	chk := hmac.New(sha256.New, sec.Sum(nil))
	_, _ = chk.Write([]byte("auth_date=" + noSigValues.Get("auth_date") + "\nquery_id=query-no-sig\nuser=" + noSigValues.Get("user")))
	noSigValues.Set("hash", hex.EncodeToString(chk.Sum(nil)))
	if _, _, err := validateTelegramMiniAppEd25519(noSigValues.Encode(), botToken, pub, now); err == nil {
		t.Fatal("validateTelegramMiniAppEd25519() accepted payload missing signature")
	}

	if _, _, err := validateTelegramMiniAppEd25519(raw, "999999:WRONG_TOKEN", pub, now); err == nil {
		t.Fatal("validateTelegramMiniAppEd25519() accepted wrong bot token")
	}

	if _, _, err := validateTelegramMiniAppEd25519(raw, botToken, pub, now.Add(telegramMiniAppMaxAge+time.Second)); err == nil {
		t.Fatal("validateTelegramMiniAppEd25519() accepted expired data")
	}
}
