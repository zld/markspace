package recovery

import (
	"archivecheck/internal/media"
)

// span renders a millisecond interval on the layer clock for fault evidence.
func (in Input) span(startMs, endMs int64) media.TickInterval {
	return media.NewInterval(in.Clock.Ticks(startMs), in.Clock.Ticks(endMs), in.Clock)
}

func countFrames(units []Unit) (frames, parseable int) {
	for _, u := range units {
		frames++
		if !u.Truncated && u.End > u.Offset {
			parseable++
		}
	}
	return frames, parseable
}

func intervals(anchors []Anchor) (mean float64, max int64) {
	if len(anchors) < 2 {
		return 0, 0
	}
	var sum int64
	max = 0
	for i := 1; i < len(anchors); i++ {
		d := anchors[i].TimeMs - anchors[i-1].TimeMs
		if d < 0 {
			d = -d
		}
		sum += d
		if d > max {
			max = d
		}
	}
	return float64(sum) / float64(len(anchors)-1), max
}

func firstBetween(sorted []int64, lo, hi int64) (int64, bool) {
	for _, v := range sorted {
		if v > lo && v < hi {
			return v, true
		}
	}
	return 0, false
}

func firstBefore(sorted []int64, off int64) (int64, bool) {
	for _, v := range sorted {
		if v < off {
			return v, true
		}
	}
	return 0, false
}
