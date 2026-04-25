package bot

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceStopCancelsDelayedWorkStartedAfterStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{lifecycleCtx: ctx, lifecycleCancel: cancel}
	s.Stop()

	var called int32
	s.runDelayed(10*time.Millisecond, func() { atomic.AddInt32(&called, 1) })
	s.wg.Wait()
	if got := atomic.LoadInt32(&called); got != 0 {
		t.Fatalf("delayed work called after Stop: %d", got)
	}
}

func TestServiceStopReturnsBeforeDelayedTimer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{lifecycleCtx: ctx, lifecycleCancel: cancel}
	s.runDelayed(time.Hour, func() { t.Fatal("delayed work should be canceled") })

	started := time.Now()
	s.Stop()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Stop took %v, want under 1s", elapsed)
	}
}
