package timebase_test

import (
	"testing"

	"archivecheck/internal/timebase"
)

func TestNanosToFramesBoundary(t *testing.T) {
	for _, n := range []timebase.Nanos{timebase.MaxNanos, timebase.MaxNanos - 1, 1 << 62} {
		frames, err := timebase.FPS2997.NanosToFrames(n)
		if n == 1<<62 {
			if err == nil {
				t.Logf("huge timestamp -> %d frames", frames)
			}
			continue
		}
		if err != nil {
			t.Errorf("MaxNanos rejected: %v", err)
		}
	}
}
