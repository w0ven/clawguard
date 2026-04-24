package ai

import (
	"context"
	"runtime/debug"

	"go.uber.org/zap"
)

func safeGo(ctx context.Context, logger *zap.Logger, fn func()) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if logger == nil {
					logger = zap.NewNop()
				}
				logger.Error("goroutine panic recovered",
					zap.Any("panic", recovered),
					zap.ByteString("stack", debug.Stack()))
			}
		}()

		if ctx != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
		fn()
	}()
}
