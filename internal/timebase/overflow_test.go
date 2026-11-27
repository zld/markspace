package timebase_test

import (
	"testing"

	"archivecheck/internal/timebase"
)

func TestOverflowBoundary(t *testing.T) {
	// 2^40 frames at 25 fps is about 1.3e21 ns, far past int64.
	for _, frames := range []int64{1 << 40, 1 << 50, 1 << 62, 9223372036854775807} {
		if got, err := timebase.FPS25.TimecodeToNanos(frames); err == nil {
			t.Errorf("frames=%d accepted as %d ns", frames, got)
		}
	}
}
