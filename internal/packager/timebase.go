package packager

import (
	"mediaqc/internal/clock"
)

// millisTB is a 1000-tick-per-second timebase used to express manifest timing.
func millisTB() clock.Timebase {
	tb, err := clock.New(1000)
	if err != nil {
		panic(err) // 1000 is always a valid timescale
	}
	return tb
}
