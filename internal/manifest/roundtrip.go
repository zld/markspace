package manifest

import "mediad/internal/model"

// RoundtripConsistency verifies that every segment's unified nanosecond
// range converts back to exactly the original manifest ticks. The returned
// ratio is matching entries over total entries; a failed representation
// contributes zero matches so callers never see a fake 1.0.
func RoundtripConsistency(m *model.Manifest) (float64, int, int) {
	total := 0
	matched := 0
	for _, rep := range m.Representations {
		ts := rep.TimeScale
		for _, seg := range rep.Segments {
			total++
			start, err := model.Unscale(seg.Start, ts)
			if err != nil || start != seg.StartTicks {
				continue
			}
			dur, err := model.Unscale(seg.Duration, ts)
			if err != nil || dur != seg.DurationTicks {
				continue
			}
			matched++
		}
	}
	if total == 0 {
		return 0, matched, total
	}
	return float64(matched) / float64(total), matched, total
}
