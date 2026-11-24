package media

// NanosPerSecond is the unified time base: all normalized timestamps are
// int64 nanoseconds on a monotonic output timeline.
const NanosPerSecond = int64(1_000_000_000)

// TicksToNanos converts ticks in the given timescale to nanoseconds.
// The multiply-then-divide order keeps sub-tick precision; int64 holds
// 2h at nanosecond resolution with ~2^20 headroom.
func TicksToNanos(ticks int64, timescale uint32) int64 {
	if timescale == 0 {
		return 0
	}
	return ticks * NanosPerSecond / int64(timescale)
}

// NanosToTicks is the inverse of TicksToNanos (truncating).
func NanosToTicks(ns int64, timescale uint32) int64 {
	if timescale == 0 {
		return 0
	}
	return ns * int64(timescale) / NanosPerSecond
}

// Unwrapper expands a wrapping unsigned timestamp (e.g. the 32-bit RTP
// timestamp) into a monotonic extended counter. It tolerates moderate
// reordering around the wrap point: a sample arriving up to half a cycle
// late is attributed to the previous cycle instead of jumping the epoch.
// The extension is signed internally so pre-wrap packets seen just after
// the wrap extend negative instead of underflowing.
type Unwrapper struct {
	bits   uint
	init   bool
	epoch  int64
	maxExt int64
}

// NewUnwrapper creates an Unwrapper for a counter of the given bit width.
func NewUnwrapper(bits uint) *Unwrapper { return &Unwrapper{bits: bits} }

// Unwrap extends raw into the full counter range.
func (u *Unwrapper) Unwrap(raw uint64) uint64 {
	mod := int64(1) << u.bits
	half := mod >> 1
	v := int64(raw) & (mod - 1)
	if !u.init {
		u.init = true
		u.maxExt = v
		return uint64(v)
	}
	ext := u.epoch*mod + v
	switch {
	case ext+half < u.maxExt:
		// Forward wrap: raw restarted below us within half a cycle.
		u.epoch++
		ext += mod
	case ext > u.maxExt+half:
		// Late / reordered packet from the previous cycle.
		ext -= mod
	}
	if ext > u.maxExt {
		u.maxExt = ext
	}
	return uint64(ext)
}

// DriftPoint pairs a local observation time with the remote media clock,
// both in nanoseconds.
type DriftPoint struct{ Local, Remote int64 }

// DriftSegment is one span of stable clock drift.
type DriftSegment struct {
	StartLocal int64
	EndLocal   int64
	SlopePPM   float64 // (slope-1)*1e6: remote-vs-local rate offset
	OffsetNs   int64   // remote - local at segment start
	Count      int
}

// DriftEstimator fits remote-vs-local clock drift piecewise: when a point
// deviates from the current segment's fit by more than the jump threshold,
// the segment is closed and a new one started, so an abrupt drift change is
// contained instead of smearing a bogus offset across the whole timeline.
type DriftEstimator struct {
	JumpThresholdNs int64
	pts             []DriftPoint
	segStart        int // index into pts where the open segment begins
	closed          []DriftSegment
}

// NewDriftEstimator creates an estimator splitting segments at residuals
// larger than jumpThresholdNs.
func NewDriftEstimator(jumpThresholdNs int64) *DriftEstimator {
	return &DriftEstimator{JumpThresholdNs: jumpThresholdNs, segStart: -1}
}

// Add feeds one (local, remote) observation.
func (d *DriftEstimator) Add(local, remote int64) {
	if d.segStart < 0 {
		d.segStart = 0
	}
	d.pts = append(d.pts, DriftPoint{Local: local, Remote: remote})
	open := d.pts[d.segStart:]
	if len(open) < 3 {
		return
	}
	// Fit the open segment without its newest point, then test the new
	// point's residual against the jump threshold.
	slope, off := fitLine(open[:len(open)-1])
	last := open[len(open)-1]
	residual := float64(last.Remote) - (slope*float64(last.Local) + off)
	thr := float64(d.JumpThresholdNs)
	if residual > thr || residual < -thr {
		// Close the segment before the outlier; the outlier alone opens
		// the next segment, so a one-off jump cannot create a spurious
		// two-point transition segment with an absurd slope.
		d.closed = append(d.closed, makeSegment(open[:len(open)-1]))
		d.segStart = len(d.pts) - 1
	}
}

// Segments returns all fitted segments, including the still-open tail.
func (d *DriftEstimator) Segments() []DriftSegment {
	out := append([]DriftSegment(nil), d.closed...)
	if d.segStart >= 0 && len(d.pts)-d.segStart >= 2 {
		out = append(out, makeSegment(d.pts[d.segStart:]))
	}
	return out
}

// DriftPPM returns the observation-count-weighted mean drift in ppm.
func (d *DriftEstimator) DriftPPM() float64 {
	segs := d.Segments()
	var sum, w float64
	for _, s := range segs {
		sum += s.SlopePPM * float64(s.Count)
		w += float64(s.Count)
	}
	if w == 0 {
		return 0
	}
	return sum / w
}

func fitLine(pts []DriftPoint) (slope, offset float64) {
	var sx, sy, sxx, sxy float64
	n := float64(len(pts))
	// Re-base on the first point to keep the sums well conditioned.
	bx, by := float64(pts[0].Local), float64(pts[0].Remote)
	for _, p := range pts {
		x, y := float64(p.Local)-bx, float64(p.Remote)-by
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 1, by
	}
	slope = (n*sxy - sx*sy) / den
	offset = by - slope*bx + (sy-slope*sx)/n
	return slope, offset
}

func makeSegment(pts []DriftPoint) DriftSegment {
	slope, off := fitLine(pts)
	return DriftSegment{
		StartLocal: pts[0].Local,
		EndLocal:   pts[len(pts)-1].Local,
		SlopePPM:   (slope - 1) * 1e6,
		OffsetNs:   int64(off),
		Count:      len(pts),
	}
}
