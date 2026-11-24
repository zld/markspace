// Package quality computes per-segment bitrate deviations and frame
// quality statistics over the composed timeline. Every quantitative
// result is derived with integer arithmetic from effective media bytes
// (duplicate packets counted once) and exact tick durations.
package quality

import (
	"archivecheck/internal/media"
)

// SegmentBitrate holds the target/actual bitrate comparison for one
// segment. Rates are integer bps; deviation is stored in basis points
// (1 bp = 0.01%) to stay float-free.
type SegmentBitrate struct {
	TrackID          uint32 `json:"track_id"`
	Seq              uint32 `json:"seq"`
	TargetBitrateBps int    `json:"target_bitrate_bps"`
	ActualBitrateBps int    `json:"actual_bitrate_bps"`
	DeviationBPS     int    `json:"deviation_bp"`
	MediaBytes       int    `json:"media_bytes"`
	DurationTicks    int64  `json:"duration_ticks"`
}

// BitrateSummary aggregates deviation across all measured segments.
type BitrateSummary struct {
	TargetKbps  int `json:"target_bitrate_kbps"`
	ActualKbps  int `json:"actual_bitrate_kbps"`
	DeviationBP int `json:"deviation_pct_scaled"` // percentage * 100
	MeanDevBP   int `json:"mean_deviation_bp"`
	MaxDevBP    int `json:"max_deviation_bp"`
	Samples     int `json:"sample_count"`
}

// ActualBitrate computes effective bitrate from media bytes and the
// segment duration on the unified integer clock.
func ActualBitrate(mediaBytes int, durationTicks int64) int {
	if mediaBytes <= 0 || durationTicks <= 0 {
		return 0
	}
	// bps = bytes*8 * ClockRate / ticks
	return int(int64(mediaBytes)*8*media.ClockRate / durationTicks)
}

// deviationBP returns |actual-target|/target in basis points.
func deviationBP(target, actual int) int {
	if target <= 0 {
		return 0
	}
	d := actual - target
	if d < 0 {
		d = -d
	}
	return d * 10_000 / target
}

// MeasureBitrate walks timeline segments and measures each one against
// its manifest target bitrate, simulating continuously varying
// bandwidth via the delivered media bytes.
func MeasureBitrate(segments []*media.Segment, targetByTrack map[uint32]int) ([]SegmentBitrate, BitrateSummary) {
	rows := make([]SegmentBitrate, 0, len(segments))
	sum := BitrateSummary{}
	var devSum, maxDev int64
	var totalBytes int
	var totalTicks int64
	var weightedTargetNum int64
	for _, seg := range segments {
		target := targetByTrack[seg.TrackID]
		actual := ActualBitrate(seg.MediaBytes, seg.DurationTicks)
		dev := deviationBP(target, actual)
		rows = append(rows, SegmentBitrate{
			TrackID:          seg.TrackID,
			Seq:              seg.Seq,
			TargetBitrateBps: target,
			ActualBitrateBps: actual,
			DeviationBPS:     dev,
			MediaBytes:       seg.MediaBytes,
			DurationTicks:    seg.DurationTicks,
		})
		devSum += int64(dev)
		if int64(dev) > maxDev {
			maxDev = int64(dev)
		}
		totalBytes += seg.MediaBytes
		totalTicks += seg.DurationTicks
		weightedTargetNum += int64(target) * seg.DurationTicks
		sum.Samples++
	}
	if sum.Samples > 0 {
		sum.MeanDevBP = int(devSum / int64(sum.Samples))
		sum.MaxDevBP = int(maxDev)
		if totalTicks > 0 {
			targetAvg := int(weightedTargetNum / totalTicks)
			actualAvg := ActualBitrate(totalBytes, totalTicks)
			sum.TargetKbps = targetAvg / 1000
			sum.ActualKbps = actualAvg / 1000
			sum.DeviationBP = deviationBP(targetAvg, actualAvg) / 100 // bp -> pct*100
		}
	}
	return rows, sum
}
