package api

import (
	"math"
	"testing"
)

func TestProfileLogPageOffsetDoesNotOverflow(t *testing.T) {
	for _, tc := range []struct {
		page, size int
		want       int32
	}{{1, 20, 0}, {2, 20, 20}, {107374183, 20, 2147483640}, {107374184, 20, math.MaxInt32}, {math.MaxInt, 100, math.MaxInt32}} {
		if got := pageOffset(tc.page, tc.size); got != tc.want {
			t.Errorf("page %d size %d: %d want %d", tc.page, tc.size, got, tc.want)
		}
	}
}
