package bot

import (
	"errors"
	"fmt"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestIsNonRetryableUpdateError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "markdown parse failure is not retryable",
			err:  tele.NewError(400, "Bad Request: can't parse entities: Character '(' is reserved and must be escaped with the preceding '\\'"),
			want: true,
		},
		{
			name: "wrapped parse failure is still detected",
			err:  fmt.Errorf("send keyword reply: %w", tele.NewError(400, "Bad Request: can't parse entities")),
			want: true,
		},
		{
			name: "message to delete not found is not retryable",
			err:  tele.NewError(400, "Bad Request: message to delete not found"),
			want: true,
		},
		{
			name: "forbidden is not retryable",
			err:  tele.NewError(403, "Forbidden: bot was kicked from the supergroup chat"),
			want: true,
		},
		{
			name: "rate limit must stay retryable",
			err:  tele.NewError(429, "Too Many Requests: retry after 5"),
			want: false,
		},
		{
			name: "telegram server error must stay retryable",
			err:  tele.NewError(500, "Internal Server Error"),
			want: false,
		},
		{
			name: "generic error must stay retryable",
			err:  errors.New("dial tcp: connection refused"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNonRetryableUpdateError(tc.err); got != tc.want {
				t.Fatalf("IsNonRetryableUpdateError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestAlertFingerprintIsBoundedAndStable(t *testing.T) {
	err := errors.New("first line of the failure\nsecond line with detail")
	got := alertFingerprint(err)
	if got != "first line of the failure" {
		t.Fatalf("alertFingerprint() = %q, want first line only", got)
	}

	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	if fp := alertFingerprint(errors.New(string(long))); len(fp) > 160 {
		t.Fatalf("alertFingerprint() length = %d, want <= 160", len(fp))
	}
}
