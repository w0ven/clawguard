package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidWebAppData = errors.New("invalid Telegram Mini App identity")

// WebAppIdentity is derived only from the signed initData, never initDataUnsafe.
type WebAppIdentity struct {
	UserID   int64
	AuthDate time.Time
}

// VerifyWebAppData validates Telegram's bot-token HMAC and a bounded auth_date.
func VerifyWebAppData(raw, botToken string, now time.Time, maxAge time.Duration) (WebAppIdentity, error) {
	invalid := WebAppIdentity{}
	if len(raw) == 0 || len(raw) > 16384 || botToken == "" || maxAge <= 0 {
		return invalid, ErrInvalidWebAppData
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) == 0 {
		return invalid, ErrInvalidWebAppData
	}
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if key == "" || strings.ContainsAny(key, "\n\r=") || len(value) != 1 {
			return invalid, ErrInvalidWebAppData
		}
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	hash, err := hex.DecodeString(values.Get("hash"))
	if err != nil || len(hash) != sha256.Size {
		return invalid, ErrInvalidWebAppData
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	signature := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = signature.Write([]byte(strings.Join(parts, "\n")))
	if !hmac.Equal(hash, signature.Sum(nil)) {
		return invalid, ErrInvalidWebAppData
	}
	unix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || unix <= 0 {
		return invalid, ErrInvalidWebAppData
	}
	date := time.Unix(unix, 0)
	if date.After(now.Add(30*time.Second)) || now.Sub(date) > maxAge {
		return invalid, ErrInvalidWebAppData
	}
	var user struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	}
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 || user.IsBot {
		return invalid, ErrInvalidWebAppData
	}
	return WebAppIdentity{UserID: user.ID, AuthDate: date}, nil
}
