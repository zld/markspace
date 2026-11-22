package metrics

import (
	"fmt"
	"strings"

	"mediacore/internal/clock"
)

// Report is the deterministic evidence bundle printed by the demo.
type Report struct {
	TargetBPS          uint64
	ActualBPS          uint64
	BitrateDeviationPC int64 // hundredths of a percent (e.g. 135 = 1.35%)
	QualitySamples     int
	DroppedSamples     uint64
	AlignmentErrorMS   int64 // absolute, integer milliseconds
	KeyframeFallbackMS int64
	PeakBufferBytes    int64
	CacheState         string
	RepairDigest       string
	OutputDigest       string
}

// Bitrate computes actual bitrate from logical wire bytes and the integer
// timeline span. Duration zero is a parameter error.
func Bitrate(logicalBytes int64, spanNanos clock.Nanos) (uint64, error) {
	if spanNanos <= 0 {
		return 0, clock.ErrInvalidArgument("metrics: non-positive timeline span")
	}
	// bytes * 1e9 / nanos, with overflow guard.
	b := uint64(logicalBytes)
	if b > (1<<63)/uint64(clock.TicksPerSecond) {
		return 0, clock.ErrOverflow("metrics.bitrate")
	}
	return (b * uint64(clock.TicksPerSecond)) / uint64(spanNanos), nil
}

// DeviationHundredths returns |actual-target|/target in hundredths of a
// percent using integer arithmetic only.
func DeviationHundredths(target, actual uint64) (int64, error) {
	if target == 0 {
		return 0, clock.ErrInvalidArgument("metrics: zero target bitrate")
	}
	diff := int64(actual) - int64(target)
	if diff < 0 {
		diff = -diff
	}
	// diff * 10000 / target
	if uint64(diff) > (1<<63)/10000 {
		return 0, clock.ErrOverflow("metrics.deviation")
	}
	return diff * 10000 / int64(target), nil
}

// AlignmentErrorMS measures |observed end - manifest end| on the integer
// timeline and converts to whole milliseconds (rounded).
func AlignmentErrorMS(observedEnd, manifestEnd clock.Nanos) int64 {
	d := int64(observedEnd) - int64(manifestEnd)
	if d < 0 {
		d = -d
	}
	// round half away from zero at the millisecond boundary
	ms := d / 1_000_000
	if rem := d % 1_000_000; rem*2 >= 1_000_000 {
		ms++
	}
	return ms
}

// Render emits the stable key=value line format used by the acceptance demo.
// Field order is part of the contract; do not reorder.
func (r Report) Render() string {
	var b strings.Builder
	write := func(k string, v any) {
		fmt.Fprintf(&b, "%s=%v\n", k, v)
	}
	write("target_bps", r.TargetBPS)
	write("actual_bps", r.ActualBPS)
	write("bitrate_deviation_pct", formatHundredths(r.BitrateDeviationPC))
	write("quality_samples", r.QualitySamples)
	write("dropped_samples", r.DroppedSamples)
	write("alignment_error_ms", r.AlignmentErrorMS)
	write("keyframe_fallback_ms", r.KeyframeFallbackMS)
	write("peak_buffer_bytes", r.PeakBufferBytes)
	write("index_cache", r.CacheState)
	if r.RepairDigest != "" {
		write("repair_digest", r.RepairDigest)
	}
	if r.OutputDigest != "" {
		write("output_digest", r.OutputDigest)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatHundredths renders an integer hundredths-of-percent as a fixed
// two-decimal value without the float formatter.
func formatHundredths(h int64) string {
	whole, frac := h/100, h%100
	return fmt.Sprintf("%d.%02d", whole, frac)
}
