package worker

import (
	"testing"
	"time"
)

func TestVerificationExpiryRetryDelay(t *testing.T) {
	tests := []struct {
		attempt int32
		want    time.Duration
	}{
		{attempt: 1, want: 30 * time.Second},
		{attempt: 2, want: time.Minute},
		{attempt: 3, want: 2 * time.Minute},
		{attempt: 20, want: time.Hour},
	}
	for _, test := range tests {
		if got := verificationExpiryRetryDelay(test.attempt); got != test.want {
			t.Fatalf("attempt %d delay = %s, want %s", test.attempt, got, test.want)
		}
	}
}

func TestVerificationExpiryLeaseOwnerIsUnique(t *testing.T) {
	first := verificationExpiryLeaseOwner()
	second := verificationExpiryLeaseOwner()
	if first == "" || second == "" || first == second {
		t.Fatalf("lease owners must be non-empty and unique: %q %q", first, second)
	}
}
