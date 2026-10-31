package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

func signWebAppFixture(v url.Values, token string) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+v.Get(k))
	}
	key := hmac.New(sha256.New, []byte("WebAppData"))
	key.Write([]byte(token))
	sig := hmac.New(sha256.New, key.Sum(nil))
	sig.Write([]byte(strings.Join(lines, "\n")))
	v.Set("hash", hex.EncodeToString(sig.Sum(nil)))
	return v.Encode()
}
func TestVerifyWebAppData(t *testing.T) {
	now := time.Unix(1800000000, 0)
	token := "123456:fixture"
	age := 5 * time.Minute
	values := url.Values{"auth_date": {"1800000000"}, "user": {`{"id":12345,"first_name":"Alice"}`}, "query_id": {"fake"}, "signature": {"signed-too"}}
	good := signWebAppFixture(values, token)
	if values.Get("hash") != "4dfe4ca8ba5955c4fb00c46d23a77c9818f7274a84848ec854e415d3c3186d9c" {
		t.Fatal("fixture signature does not match independently calculated vector")
	}
	if got, err := VerifyWebAppData(good, token, now, age); err != nil || got.UserID != 12345 || !got.AuthDate.Equal(now) {
		t.Fatalf("valid: %+v %v", got, err)
	}
	for name, raw := range map[string]string{"wrong token data": strings.Replace(good, "12345", "12346", 1), "duplicate": good + "&user=%7B%22id%22%3A8%7D", "missing hash": "auth_date=1800000000&user=x", "bad encoding": good + "&bad=%xx"} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyWebAppData(raw, token, now, age); err == nil {
				t.Fatal("accepted invalid")
			}
		})
	}
	if _, err := VerifyWebAppData(good, "different", now, age); err == nil {
		t.Fatal("accepted wrong secret")
	}
	if _, err := VerifyWebAppData(good, token, now.Add(age+time.Second), age); err == nil {
		t.Fatal("accepted expired")
	}
	if _, err := VerifyWebAppData(good, token, now.Add(-31*time.Second), age); err == nil {
		t.Fatal("accepted future timestamp")
	}
	for _, user := range []string{`{}`, `{"id":0}`, `{"id":42,"is_bot":true}`, `not-json`} {
		values.Set("user", user)
		raw := signWebAppFixture(values, token)
		if _, err := VerifyWebAppData(raw, token, now, age); err == nil {
			t.Fatalf("accepted user %s", user)
		}
	}
}
