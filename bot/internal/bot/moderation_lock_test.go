package bot

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestModerationLockSubjectIsolationAndCancellation(t *testing.T) {
	a, b := &Service{}, &Service{}
	release, err := a.AcquireModerationReview(context.Background(), -1, 42)
	if err != nil {
		t.Fatal(err)
	}
	other, err := b.AcquireModerationReview(context.Background(), -1, 43)
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r, e := b.AcquireModerationReview(ctx, -1, 42)
		if e == nil {
			r()
		}
		done <- e
	}()
	if e := <-done; !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("waiter: %v", e)
	}
	release()
	release()
	r, err := b.AcquireModerationReview(context.Background(), -1, 42)
	if err != nil {
		t.Fatal(err)
	}
	r()
	moderationGates.Lock()
	n := len(moderationGates.entries)
	moderationGates.Unlock()
	if n != 0 {
		t.Fatalf("gate leak: %d", n)
	}
}
