// Package clock unwraps MPEG-TS 33-bit clocks and maps track timestamps into
// one monotonic millisecond timeline.
package clock

import (
	"sort"
	"time"
)

const (
	// Mod33 is the wrap modulus shared by PTS/DTS and PCR_base.
	Mod33 uint64 = 1 << 33
	// PTSHz is the presentation timestamp clock rate.
	PTSHz uint64 = 90_000
	// PCRHz is the full PCR clock rate including the 9-bit extension.
	PCRHz uint64 = 27_000_000
)

// Unwrapped holds a monotonically expanded clock value.
type Unwrapped struct {
	Value       uint64
	WrapCount   uint64
	WrappedThis bool
}

// Unwrap33 extends a 33-bit counter. prev is the previous unwrapped value;
// ok is false before the first observation, in which case raw is used as the
// zero-based origin. A backward jump larger than half the modulus is treated
// as a wrap. Small backward values are retained and reported by the caller as
// timestamp disorder rather than silently adjusted.
func Unwrap33(prev uint64, hasPrev bool, raw uint64) Unwrapped {
	raw &= Mod33 - 1
	if !hasPrev {
		return Unwrapped{Value: raw}
	}
	prevRaw := prev & (Mod33 - 1)
	cycles := prev / Mod33
	wrapped := false
	if raw < prevRaw {
		delta := prevRaw - raw
		if delta > Mod33/2 {
			cycles++
			wrapped = true
		}
	} else if raw-prevRaw > Mod33/2 {
		// The remote counter has lapped us after an unusually large forward
		// jump represented near zero; keep the conservative interpretation.
		if cycles > 0 {
			cycles--
		}
	}
	v := cycles*Mod33 + raw
	if wrapped {
		// Value already includes the new cycle.
	}
	return Unwrapped{Value: v, WrapCount: cycles, WrappedThis: wrapped}
}

// PTSToMS converts an unwrapped 90 kHz timestamp to integer milliseconds.
func PTSToMS(pts uint64) int64 {
	return int64(pts * 1000 / PTSHz)
}

// PCRBaseToMS converts an unwrapped 33-bit, 90 kHz PCR_base to milliseconds.
func PCRBaseToMS(base uint64) int64 { return PTSToMS(base) }

// PCRFullToMS converts a full 42+9 bit, 27 MHz PCR to milliseconds.
func PCRFullToMS(pcr uint64) int64 {
	return int64(pcr * 1000 / PCRHz)
}

// TrackMap maps raw track timestamps to an origin-shifted unified timeline.
type TrackMap struct {
	OriginMS   int64
	lastUnwrap uint64
	hasLast    bool
	LastMS     int64
	WrapCount  int
	Disorder   int
}

// MapPTS unwraps raw PTS and returns origin-shifted milliseconds. The first
// sample establishes the track origin. Non-monotonic small backward jumps are
// counted; the returned output time is clamped to the last emitted time so
// muxed sample durations remain monotonic.
func (m *TrackMap) MapPTS(raw uint64) (ms int64, wrapped bool, nonMonotonic bool) {
	u := Unwrap33(m.lastUnwrap, m.hasLast, raw)
	if u.WrappedThis {
		m.WrapCount++
		wrapped = true
	}
	m.lastUnwrap = u.Value
	m.hasLast = true
	absolute := PTSToMS(u.Value)
	if m.OriginMS == 0 && m.LastMS == 0 {
		m.OriginMS = absolute
	}
	ms = absolute - m.OriginMS
	if ms < m.LastMS {
		m.Disorder++
		nonMonotonic = true
		ms = m.LastMS
	}
	m.LastMS = ms
	return ms, wrapped, nonMonotonic
}

// SyncError returns the maximum absolute difference between corresponding
// millisecond timestamps. It compares the shorter slice and returns -1 when
// there are no common samples, preventing an empty result from masquerading
// as perfect sync.
func SyncError(videoMS, audioMS []int64) float64 {
	n := len(videoMS)
	if len(audioMS) < n {
		n = len(audioMS)
	}
	if n == 0 {
		return -1
	}
	var max float64
	for i := 0; i < n; i++ {
		d := float64(videoMS[i] - audioMS[i])
		if d < 0 {
			d = -d
		}
		if d > max {
			max = d
		}
	}
	return max
}

// Percentile95 calculates the nearest-rank P95 and reports the actual sample
// count. An empty slice returns ok=false rather than a fabricated zero.
func Percentile95(samples []time.Duration) (p95MS float64, count int, ok bool) {
	count = len(samples)
	if count == 0 {
		return 0, 0, false
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (95*count + 99) / 100 // ceil(0.95*n), 1-based rank
	if idx < 1 {
		idx = 1
	}
	if idx > count {
		idx = count
	}
	return float64(sorted[idx-1].Microseconds()) / 1000.0, count, true
}
