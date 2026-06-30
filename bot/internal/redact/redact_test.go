package redact

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const sampleToken = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghi"

func TestTextRedactsTelegramBotTokens(t *testing.T) {
	tests := map[string]string{
		"bare token":        "request failed for [REDACTED]",
		"bot token":         "request failed for bot[REDACTED]",
		"telegram api url":  "Post \"https://api.telegram.org/bot[REDACTED]/sendMessage\": net/http: TLS handshake timeout",
		"telegram file url": "Get \"https://api.telegram.org/file/bot[REDACTED]/photos/file.jpg\": net/http: TLS handshake timeout",
		"hyphen suffix url": "Post \"https://api.telegram.org/bot[REDACTED]/sendMessage\": net/http: TLS handshake timeout",
	}

	inputs := map[string]string{
		"bare token":        "request failed for " + sampleToken,
		"bot token":         "request failed for bot" + sampleToken,
		"telegram api url":  "Post \"https://api.telegram.org/bot" + sampleToken + "/sendMessage\": net/http: TLS handshake timeout",
		"telegram file url": "Get \"https://api.telegram.org/file/bot" + sampleToken + "/photos/file.jpg\": net/http: TLS handshake timeout",
		"hyphen suffix url": "Post \"https://api.telegram.org/bot123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefgh-/sendMessage\": net/http: TLS handshake timeout",
	}

	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Text(inputs[name]); got != want {
				t.Fatalf("Text() = %q, want %q", got, want)
			}
		})
	}
}

func TestZapLoggerRedactsErrorAndFields(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	logger := ZapLogger(zap.New(core))

	logger.Info(
		"calling https://api.telegram.org/bot"+sampleToken+"/getMe",
		zap.Error(errors.New("Post \"https://api.telegram.org/bot"+sampleToken+"/getMe\": net/http: TLS handshake timeout")),
		zap.String("token", sampleToken),
		zap.ByteString("raw_update_json", []byte("{\"text\":\"https://api.telegram.org/bot"+sampleToken+"/sendMessage\"}")),
	)

	entry := logs.All()[0]
	if strings.Contains(entry.Message, sampleToken) {
		t.Fatalf("message leaked token: %q", entry.Message)
	}
	for _, field := range entry.Context {
		fieldText := field.String + fmt.Sprint(field.Interface)
		if strings.Contains(fieldText, sampleToken) {
			t.Fatalf("field %q leaked token: %q", field.Key, fieldText)
		}
	}
}
